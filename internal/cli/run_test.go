package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/xenctl"
)

func TestRunWaypipeGuard(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	err := runWaypipe([]string{"ssh", "alpha"}, bytes.NewReader(nil), io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WAYLAND_DISPLAY")
}

func TestAssertVMRunning(t *testing.T) {
	old := xenctlNew
	t.Cleanup(func() { xenctlNew = old })
	// The check only runs for privileged callers (unprivileged users have
	// no Xen state source); pretend to be root so the path is exercised
	// in CI and on the dom0 alike.
	oldPriv := privileged
	t.Cleanup(func() { privileged = oldPriv })
	privileged = func() bool { return true }
	domains := []xenctl.DomainInfo{
		{Name: "alpha", State: "running"},
		{Name: "beta", State: "blocked"}, // idle vCPU — the guest is alive
		{Name: "gamma", State: "dying"},  // the ---sr- zombie
		{Name: "delta", State: "paused"},
	}
	xenctlNew = func() (xenctl.Xen, error) {
		return &fakeXen{domains: domains}, nil
	}
	require.NoError(t, assertVMRunning("alpha"), "running domain is allowed")
	require.NoError(t, assertVMRunning("beta"), "blocked (idle) domain is alive")

	err := assertVMRunning("zeta")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
	assert.Contains(t, err.Error(), "qlvm vm start zeta")

	err = assertVMRunning("gamma")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zombie")
	assert.Contains(t, err.Error(), "qlvm vm restart gamma")

	err = assertVMRunning("delta")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paused")

	// Stub build (New fails) skips the check — waypipe-over-ssh still works.
	xenctlNew = func() (xenctl.Xen, error) {
		return nil, errors.New("no libxl")
	}
	require.NoError(t, assertVMRunning("zeta"))

	// Unprivileged caller: libxl cannot open the context (dom0 gives a
	// non-root process no Xen state source), so the check is skipped.
	xenctlNew = func() (xenctl.Xen, error) {
		t.Fatal("libxl must not be opened for an unprivileged caller")
		return nil, nil
	}
	privileged = func() bool { return false }
	require.NoError(t, assertVMRunning("zeta"))
}

func TestSSHAuthKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")

	// No identities: hard error (a VM without SSH is unrunnable).
	_, err := sshAuthKeys()
	require.Error(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"),
		[]byte("ssh-ed25519 AAAAfirst desktop@dom0\n"), 0o600))
	keys, err := sshAuthKeys()
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519 AAAAfirst desktop@dom0\n", keys)
}
