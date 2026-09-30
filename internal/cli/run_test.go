package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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

func TestWaypipeSSHArgs(t *testing.T) {
	got := waypipeSSHArgs("10.100.1.10", []string{"firefox", "--no-remote"})
	want := []string{"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"user@10.100.1.10", "firefox", "--no-remote"}
	assert.Equal(t, want, got)
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
