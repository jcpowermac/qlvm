package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXen is a no-op Xen for construction tests.
type fakeXen struct{}

func (f *fakeXen) CreateDomain(_ *vm.DomainSpec) error { return nil }
func (f *fakeXen) Destroy(_ string) error              { return nil }
func (f *fakeXen) Shutdown(_ string) error             { return nil }
func (f *fakeXen) List() ([]xenctl.DomainInfo, error)  { return nil, nil }
func (f *fakeXen) Running(_ string) (bool, error)      { return false, nil }
func (f *fakeXen) Console(_ string) error              { return nil }
func (f *fakeXen) Close() error                        { return nil }

// fakeOVNPorter is a no-op OVNPorter for construction tests.
type fakeOVNPorter struct{}

func (f *fakeOVNPorter) AddLSPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeOVNPorter) DelLSPort(_ context.Context, _ string) error          { return nil }

// fakeVifPorter is a no-op VifPorter for construction tests.
type fakeVifPorter struct{}

func (f *fakeVifPorter) AddVifPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeVifPorter) DelVifPort(_ context.Context, _ string) error          { return nil }
func (f *fakeVifPorter) StaleVifPorts(_ context.Context, _ string) ([]string, error) { return nil, nil }

func TestNew(t *testing.T) {
	x := &fakeXen{}
	o := &fakeOVNPorter{}
	v := &fakeVifPorter{}
	cfg := &config.Config{
		Domains: []config.Domain{{Name: "alpha", Subnet: "10.100.1"}},
		Network: config.Network{NIC: "eth0", Gateway: "192.0.2.1", RouterIP: "192.0.2.1", DNS: []string{"1.1.1.1"}},
		VM:      config.VMDefaults{MemoryMB: 4096, VCPUs: 2},
	}

	a := New(x, o, v, cfg, "/var/lib/qvm/coremap.yaml", "/var/lib/qvm")

	require.NotNil(t, a)
	assert.Equal(t, x, a.xen)
	assert.Equal(t, o, a.ovn)
	assert.Equal(t, v, a.ovs)
	assert.Equal(t, cfg, a.cfg)
	assert.Equal(t, "/var/lib/qvm/coremap.yaml", a.coremapPath)
	assert.Equal(t, "/var/lib/qvm", a.root)
	assert.NotNil(t, a.progress)
	assert.Empty(t, a.progress)
	assert.Nil(t, a.conn)
}

func TestDBusConstants(t *testing.T) {
	assert.Equal(t, "io.qlvm.Agent", ServiceName)
	assert.Equal(t, "/io/qlvm/Agent", ObjectPath)
	assert.Equal(t, "io.qlvm.Agent", Iface)
}

func TestMethodStubs(t *testing.T) {
	// Only the Task 5-8 methods should still be stubs; the Task 4 read
	// methods are implemented.
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	_, err := a.CreateVM(&CreateVMOptions{Name: "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.DeleteVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.StartVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.StopVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.KillVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.RestartVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	_, err = a.CreateTemplate("quay.io/fedora/fedora:42", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.DeleteTemplate("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.ProvisionVM("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = a.SyncKernel("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}

func TestProgressEmitWithoutBus(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	// emitProgress should not panic without a bus connection.
	a.emitProgress("op1", "starting")
	a.mu.Lock()
	tracker, ok := a.progress["op1"]
	a.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "op1", tracker.ID)
	assert.Equal(t, "starting", tracker.Message)
	assert.False(t, tracker.Done)

	// Update the message.
	a.emitProgress("op1", "working")
	a.mu.Lock()
	tracker = a.progress["op1"]
	a.mu.Unlock()
	assert.Equal(t, "working", tracker.Message)

	// Mark as done.
	a.emitProgressDone("op1")
	a.mu.Lock()
	tracker = a.progress["op1"]
	a.mu.Unlock()
	assert.True(t, tracker.Done)

	// emitProgressDone for unknown id should not panic.
	a.emitProgressDone("unknown")
}

// TestRunRegistersBus verifies that Run attempts to register on the bus.
// The full registration test requires a live D-Bus system bus; here we
// verify the error path when the bus is unavailable and that the constants
// are used correctly. Integration tests (Task 9) cover the live bus case.
func TestRunRegistersBus(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")
	// Timeout prevents an indefinite hang if the system bus is available
	// and the name is acquired (Run would otherwise block until the bus
	// closes).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run attempts to register on the system bus. Three outcomes are
	// possible:
	// 1. No bus available: error mentions "system bus".
	// 2. Bus available, name acquired: returns the service name after timeout.
	// 3. Bus available, name refused by policy: error mentions the name.
	name, err := a.Run(ctx)
	switch {
	case err == nil:
		assert.Equal(t, ServiceName, name)
		assert.Nil(t, a.conn)
	case errors.Is(err, context.DeadlineExceeded):
		// ctx timed out after acquiring the name — the bus was available
		// and the test environment doesn't have a live bus to close on its own.
		// This is acceptable: it means registration succeeded.
		assert.Nil(t, a.conn)
	case errors.Is(err, context.Canceled):
		// ctx was cancelled (should not happen in this test).
		t.Fatal("unexpected context cancellation")
	default:
		// Verify the error is from the expected path.
		assert.True(t,
			strings.Contains(err.Error(), "system bus") ||
				strings.Contains(err.Error(), ServiceName),
			"unexpected error: %v", err)
		assert.Equal(t, "", name)
		assert.Nil(t, a.conn)
	}
}

// Verify fake types satisfy the interfaces they're used for.
var _ xenctl.Xen = &fakeXen{}

var _ vm.OVNPorter = &fakeOVNPorter{}

var _ ovs.VifPorter = &fakeVifPorter{}
