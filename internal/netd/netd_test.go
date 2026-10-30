package netd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureWritesAndIsIdempotent: run 1 writes both drop-ins and reloads;
// run 2 with identical content touches nothing.
func TestEnsureWritesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	loads := 0
	m := &Manager{Dir: dir, Reload: func() error { loads++; return nil }}

	require.NoError(t, m.Ensure(Params{NIC: "enp3s0", Bridge: "br-ex"}))
	assert.Equal(t, 1, loads)
	for _, f := range []string{"90-qlvm-nic.network", "90-qlvm-br-ex.network"} {
		b, err := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec,G304
		require.NoError(t, err, f)
		assert.Contains(t, string(b), "DHCP=ipv4")
	}
	assert.Contains(t, read(t, dir, "90-qlvm-nic.network"), "Name=enp3s0")
	assert.Contains(t, read(t, dir, "90-qlvm-br-ex.network"), "Name=br-ex")
	_, err := os.ReadFile(filepath.Join(dir, "90-qlvm-dom0.network"))
	assert.True(t, os.IsNotExist(err), "no dom0 drop-in without Dom0LSW")

	require.NoError(t, m.Ensure(Params{NIC: "enp3s0", Bridge: "br-ex"}))
	assert.Equal(t, 1, loads, "run 2 must not reload")
}

// TestEnsureRewritesChangedContent: a different NIC rewrites only the
// changed file and reloads.
func TestEnsureRewritesChangedContent(t *testing.T) {
	dir := t.TempDir()
	loads := 0
	m := &Manager{Dir: dir, Reload: func() error { loads++; return nil }}

	require.NoError(t, m.Ensure(Params{NIC: "enp3s0", Bridge: "br-ex"}))
	require.NoError(t, m.Ensure(Params{NIC: "enp5s0", Bridge: "br-ex"}))
	assert.Equal(t, 2, loads)
	assert.Contains(t, read(t, dir, "90-qlvm-nic.network"), "Name=enp5s0")
}

// TestEnsureDom0LSW: RouterIP+Supernet add the supernet route to the
// bridge drop-in, and Dom0LSW addresses create the qvm-dom0 drop-in with
// one Address per domain subnet. A second identical run is a no-op.
func TestEnsureDom0LSW(t *testing.T) {
	dir := t.TempDir()
	loads := 0
	m := &Manager{Dir: dir, Reload: func() error { loads++; return nil }}

	p := Params{
		NIC:      "enp3s0",
		Bridge:   "br-ex",
		RouterIP: "172.31.12.200",
		Supernet: "10.100.0.0/16",
		Dom0LSW:  []string{"10.100.1.2/24", "10.100.2.2/24"},
	}
	require.NoError(t, m.Ensure(p))

	br := read(t, dir, "90-qlvm-br-ex.network")
	assert.Contains(t, br, "[Route]")
	assert.Contains(t, br, "Destination=10.100.0.0/16")
	assert.Contains(t, br, "Gateway=172.31.12.200")

	dom0 := read(t, dir, "90-qlvm-dom0.network")
	assert.Contains(t, dom0, "Name=qvm-dom0")
	assert.Contains(t, dom0, "Address=10.100.1.2/24")
	assert.Contains(t, dom0, "Address=10.100.2.2/24")

	require.NoError(t, m.Ensure(p))
	assert.Equal(t, 1, loads, "identical second run must not reload")
}

// TestEnsureDom0LSWOmitted: empty RouterIP/Supernet/Dom0LSW reproduce the
// legacy two-file output exactly.
func TestEnsureDom0LSWOmitted(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{Dir: dir, Reload: func() error { return nil }}

	require.NoError(t, m.Ensure(Params{NIC: "enp3s0", Bridge: "br-ex"}))
	assert.NotContains(t, read(t, dir, "90-qlvm-br-ex.network"), "[Route]")
}

// TestWaitUplinkFailsWithoutAddress: the poller times out while the
// device has no global IPv4 and passes the moment it does.
func TestWaitUplinkFailsWithoutAddress(t *testing.T) {
	old := UplinkTimeout
	UplinkTimeout = 300 * time.Millisecond
	t.Cleanup(func() { UplinkTimeout = old })

	up := false
	m := &Manager{UplinkOK: func(string) bool { return up }}
	require.Error(t, m.WaitUplink(context.Background(), "br-ex"))

	up = true
	require.NoError(t, m.WaitUplink(context.Background(), "br-ex"))
}

// TestWaitUplinkHonorsContextCancel.
func TestWaitUplinkHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &Manager{UplinkOK: func(string) bool { return false }}
	assert.ErrorIs(t, m.WaitUplink(ctx, "br-ex"), context.Canceled)
}

// TestEnslavedAbsent reports false (not an error) for a device with no
// sysfs entry.
func TestEnslavedAbsent(t *testing.T) {
	enslaved, err := Enslaved("no-such-iface-42")
	require.NoError(t, err)
	require.False(t, enslaved)
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec,G304
	require.NoError(t, err)
	return string(b)
}
