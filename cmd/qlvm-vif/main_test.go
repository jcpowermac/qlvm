package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXs is an in-memory XsReader; unknown paths fail like errno 2.
type fakeXs struct {
	nodes map[string]string
}

func (f *fakeXs) Read(path string) (string, error) {
	v, ok := f.nodes[path]
	if !ok {
		return "", errors.New("enoent: " + path)
	}
	return v, nil
}

// fakeOVS records VifPorter calls and can be made to fail.
type fakeOVS struct {
	adds []string // "dev|ifaceID|vmUUID|mac"
	dels []string
	err  error
}

func (f *fakeOVS) AddVifPort(_ context.Context, dev, ifaceID, vmUUID, mac string) error {
	if f.err != nil {
		return f.err
	}
	f.adds = append(f.adds, dev+"|"+ifaceID+"|"+vmUUID+"|"+mac)
	return nil
}

func (f *fakeOVS) DelVifPort(_ context.Context, dev string) error {
	if f.err != nil {
		return f.err
	}
	f.dels = append(f.dels, dev)
	return nil
}

func (f *fakeOVS) StaleVifPorts(context.Context, string) ([]string, error) {
	return nil, nil
}

// setFlagsUp stubs bringUp's kernel link-state check; t.Cleanup restores
// the real one.
func setFlagsUp(t *testing.T, up bool) {
	t.Helper()
	old := flagsUpFn
	t.Cleanup(func() { flagsUpFn = old })
	flagsUpFn = func(string) bool { return up }
}

type fakeLinks struct{ ups []string }

func (f *fakeLinks) up(dev string) error {
	f.ups = append(f.ups, dev)
	return nil
}

func addTestXs() *fakeXs {
	return &fakeXs{nodes: map[string]string{
		"/local/domain/1/name": "web1",
		"/local/domain/1/vm": "/vm/0e9a1c55-0000-4000-8000-000000000001",
		"/local/domain/1/device/vif/0/mac": "aa:bb:cc:dd:ee:01",
	}}
}

func TestVifSkipsEmu(t *testing.T) {
	ov := &fakeOVS{}
	links := &fakeLinks{}
	for _, command := range []string{"add", "online", "remove", "offline"} {
		require.NoError(t, VifHandle(command, "vif0.0-emu", &fakeXs{}, ov, links.up), command)
	}
	assert.Empty(t, ov.adds)
	assert.Empty(t, ov.dels)
	assert.Empty(t, links.ups)

	// run-level: an -emu dev exits 0 without ever wiring the planes
	// (OVS may be down during an HVM bring-up).
	oldWire := wire
	t.Cleanup(func() { wire = oldWire })
	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return nil, nil, nil, errors.New("ovs down")
	}
	t.Setenv("vif", "vif0.0-emu")
	assert.Equal(t, 0, run([]string{"add", "type_if=vif"}))
}

// TestRunTeardownWithDeadControlPlane pins bash do_without_error parity:
// remove|offline exits 0 when the control plane cannot even be wired
// (plausible teardown: OVS restarted after the VM started), while
// bring-up paths still exit 1.
func TestRunTeardownWithDeadControlPlane(t *testing.T) {
	oldWire := wire
	t.Cleanup(func() { wire = oldWire })
	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return nil, nil, nil, errors.New("ovsdb down")
	}
	t.Setenv("vif", "vif1.0")
	assert.Equal(t, 0, run([]string{"remove"}))
	assert.Equal(t, 0, run([]string{"offline"}))
	assert.Equal(t, 1, run([]string{"online"}))
}

func TestVifAddHappyPath(t *testing.T) {
	t.Setenv("XENBUS_PATH", "backend/vif/1/0")
	xs := addTestXs()
	ov := &fakeOVS{}
	links := &fakeLinks{}
	setFlagsUp(t, true)

	require.NoError(t, VifHandle("add", "vif1.0", xs, ov, links.up))
	assert.Equal(t,
		[]string{"vif1.0|web1|0e9a1c55-0000-4000-8000-000000000001|aa:bb:cc:dd:ee:01"},
		ov.adds)
	assert.Equal(t, []string{"vif1.0"}, links.ups)

	// online converges the same way.
	require.NoError(t, VifHandle("online", "vif1.0", xs, ov, links.up))
	assert.Len(t, ov.adds, 2)
	assert.Len(t, links.ups, 2)
}

// TestBringUpRetriesUntilUp covers the ovs-vswitchd bind race: the
// first IFF_UP wins the netlink ack but ovs re-syncs the link down,
// so bringUp must re-issue until operstate reports up.
func TestBringUpRetriesUntilUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	state := false
	ups := 0
	setFlagsUp(t, false)
	flagsUpFn = func(string) bool { return state }
	linkUp := func(string) error {
		ups++
		if ups == 2 { // ovs settles after the second attempt
			state = true
		}
		return nil
	}

	require.NoError(t, bringUp(ctx, "vif9.0", linkUp))
	assert.GreaterOrEqual(t, ups, 2)

	// A link that never reports up fails at the deadline.
	state = false
	ups = 0
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	assert.Error(t, bringUp(ctx2, "vif9.0", linkUp))
}

func TestVifAddFailurePropagates(t *testing.T) {
	t.Setenv("XENBUS_PATH", "backend/vif/1/0")
	xs := addTestXs()
	setFlagsUp(t, true)

	// OVS failure: error returned, link never brought up.
	ovFail := &fakeOVS{err: errors.New("ovsdb down")}
	links := &fakeLinks{}
	err := VifHandle("add", "vif1.0", xs, ovFail, links.up)
	require.Error(t, err)
	assert.Empty(t, links.ups)

	// Xenstore failure: error returned, OVS never touched.
	ov := &fakeOVS{}
	err = VifHandle("add", "vif1.0", &fakeXs{}, ov, links.up)
	require.Error(t, err)
	assert.Empty(t, ov.adds)

	// main maps the failure to exit 1, and a healthy path exits 0.
	oldWire := wire
	t.Cleanup(func() { wire = oldWire })
	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return xs, ovFail, links.up, nil
	}
	t.Setenv("vif", "vif1.0")
	assert.Equal(t, 1, run([]string{"online", "type_if=vif"}))

	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return xs, &fakeOVS{}, links.up, nil
	}
	assert.Equal(t, 0, run([]string{"online", "type_if=vif"}))
}

// TestVifRemoveSwallows pins the teardown contract: remove|offline always
// succeeds so a dying domain can never be wedged on a vif script error.
func TestVifRemoveSwallows(t *testing.T) {
	ovFail := &fakeOVS{err: errors.New("port gone")}
	links := &fakeLinks{}
	require.NoError(t, VifHandle("remove", "vif1.0", &fakeXs{}, ovFail, links.up))
	require.NoError(t, VifHandle("offline", "vif1.0", &fakeXs{}, &fakeOVS{}, links.up))
	assert.Empty(t, links.ups)

	// main maps the swallowed teardown to exit 0.
	oldWire := wire
	t.Cleanup(func() { wire = oldWire })
	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return &fakeXs{}, ovFail, links.up, nil
	}
	t.Setenv("vif", "vif1.0")
	assert.Equal(t, 0, run([]string{"remove"}))
}

func TestVifUnknownCommand(t *testing.T) {
	err := VifHandle("reboot", "vif1.0", addTestXs(), &fakeOVS{}, (&fakeLinks{}).up)
	require.Error(t, err)
}

// TestRunUsage pins exit 1 on a missing command, and a missing `vif` env
// device name fails the bring-up path (XENBUS_PATH then never resolves).
func TestRunUsage(t *testing.T) {
	assert.Equal(t, 1, run(nil))

	oldWire := wire
	t.Cleanup(func() { wire = oldWire })
	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return addTestXs(), &fakeOVS{}, (&fakeLinks{}).up, nil
	}
	assert.Equal(t, 1, run([]string{"online"})) // no `vif` env set
}
