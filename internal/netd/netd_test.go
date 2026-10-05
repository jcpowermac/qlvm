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

	require.NoError(t, m.Ensure("enp3s0", "br-ex"))
	assert.Equal(t, 1, loads)
	for _, f := range []string{"90-qlvm-nic.network", "90-qlvm-br-ex.network"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		require.NoError(t, err, f)
		assert.Contains(t, string(b), "DHCP=ipv4")
	}
	assert.Contains(t, read(t, dir, "90-qlvm-nic.network"), "Name=enp3s0")
	assert.Contains(t, read(t, dir, "90-qlvm-br-ex.network"), "Name=br-ex")

	require.NoError(t, m.Ensure("enp3s0", "br-ex"))
	assert.Equal(t, 1, loads, "run 2 must not reload")
}

// TestEnsureRewritesChangedContent: a different NIC rewrites only the
// changed file and reloads.
func TestEnsureRewritesChangedContent(t *testing.T) {
	dir := t.TempDir()
	loads := 0
	m := &Manager{Dir: dir, Reload: func() error { loads++; return nil }}

	require.NoError(t, m.Ensure("enp3s0", "br-ex"))
	require.NoError(t, m.Ensure("enp5s0", "br-ex"))
	assert.Equal(t, 2, loads)
	assert.Contains(t, read(t, dir, "90-qlvm-nic.network"), "Name=enp5s0")
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

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}
