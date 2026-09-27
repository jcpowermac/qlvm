package sshx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func writeSSHConfig(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".ssh", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestResolve(t *testing.T) {
	home := t.TempDir()
	writeSSHConfig(t, home, "Host alpha\n  HostName 10.100.1.10\n  User user\n\nHost beta\n  HostName 10.100.1.11\n")

	host, user, err := Resolve("alpha", home)
	require.NoError(t, err)
	assert.Equal(t, "10.100.1.10", host)
	assert.Equal(t, "user", user)

	// No User line -> fallback user.
	host, user, err = Resolve("beta", home)
	require.NoError(t, err)
	assert.Equal(t, "10.100.1.11", host)
	assert.Equal(t, "user", user)

	_, _, err = Resolve("gamma", home)
	require.Error(t, err)
}

func TestKernelVersion(t *testing.T) {
	ver, err := KernelVersion("kernel-core-6.11.9-300.fc44.x86_64\n")
	require.NoError(t, err)
	assert.Equal(t, "6.11.9-300.fc44", ver)

	_, err = KernelVersion("package kernel-core is not installed\n")
	require.Error(t, err)

	_, err = KernelVersion("")
	require.Error(t, err)
}

func TestWaitForSSH(t *testing.T) {
	t.Run("recovers after failures", func(t *testing.T) {
		tries := 0
		err := WaitForSSH(context.Background(), func() (*ssh.Client, error) {
			tries++
			if tries < 3 {
				return nil, errors.New("connection refused")
			}
			return nil, nil
		}, 3, time.Millisecond)
		require.NoError(t, err)
		assert.Equal(t, 3, tries)
	})

	t.Run("exhausts tries", func(t *testing.T) {
		tries := 0
		err := WaitForSSH(context.Background(), func() (*ssh.Client, error) {
			tries++
			return nil, errors.New("connection refused")
		}, 3, time.Millisecond)
		require.Error(t, err)
		assert.Equal(t, 3, tries)
	})
}
