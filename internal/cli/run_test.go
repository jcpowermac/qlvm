package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWaypipeGuard(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	err := runWaypipe([]string{"ssh", "alpha"}, bytes.NewReader(nil), io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WAYLAND_DISPLAY")
}

func TestAssertVMRunningIsNoOp(t *testing.T) {
	// assertVMRunning is now a no-op: the CLI runs as the user and has no
	// Xen state source at all (libxl needs root and /run/xenstored/socket
	// is 0600 root on the dom0). A down VM surfaces as waypipe/ssh's own
	// failure instead.
	require.NoError(t, assertVMRunning("any-vm"))
}
