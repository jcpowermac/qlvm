package main

import (
	"context"
	"errors"
	"testing"

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

type fakeLinks struct{ ups []string }

func (f *fakeLinks) up(dev string) error {
	f.ups = append(f.ups, dev)
	return nil
}

func addTestXs() *fakeXs {
	return &fakeXs{nodes: map[string]string{
		"/local/domain/0/device/vif/vif1.0/backend/frontend-id": "1",
		"/local/domain/1/name": "web1",
		"/local/domain/1/uuid": "0e9a1c55-0000-4000-8000-000000000001",
		"/local/domain/0/device/vif/vif1.0/backend/frontend/mac": "aa:bb:cc:dd:ee:01",
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
}

func TestVifAddHappyPath(t *testing.T) {
	t.Setenv("XENBUS_PATH", "/local/domain/0/device/vif/vif1.0/backend")
	xs := addTestXs()
	ov := &fakeOVS{}
	links := &fakeLinks{}

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

func TestVifAddFailurePropagates(t *testing.T) {
	t.Setenv("XENBUS_PATH", "/local/domain/0/device/vif/vif1.0/backend")
	xs := addTestXs()

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
	assert.Equal(t, 1, run([]string{"add", "vif1.0", "1", "aa:bb:cc:dd:ee:01", "0"}))

	wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
		return xs, &fakeOVS{}, links.up, nil
	}
	assert.Equal(t, 0, run([]string{"add", "vif1.0", "1", "aa:bb:cc:dd:ee:01", "0"}))
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
	assert.Equal(t, 0, run([]string{"remove", "vif1.0", "1", "aa:bb:cc:dd:ee:01", "0"}))
}

func TestVifUnknownCommand(t *testing.T) {
	err := VifHandle("reboot", "vif1.0", addTestXs(), &fakeOVS{}, (&fakeLinks{}).up)
	require.Error(t, err)
}

// TestRunUsage pins exit 1 on a malformed libxl invocation.
func TestRunUsage(t *testing.T) {
	assert.Equal(t, 1, run([]string{"add"}))
	assert.Equal(t, 1, run(nil))
}
