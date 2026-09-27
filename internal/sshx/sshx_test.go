package sshx

import (
	"context"
	"errors"
	"io"
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

func TestNormalizeHostPort(t *testing.T) {
	assert.Equal(t, "10.100.1.10:22", normalizeHostPort("10.100.1.10"))
	assert.Equal(t, "[::1]:22", normalizeHostPort("[::1]"))
	assert.Equal(t, "10.100.1.10:2222", normalizeHostPort("10.100.1.10:2222"))
	assert.Equal(t, "10.100.1.10:22", normalizeHostPort("10.100.1.10:22"))
}

func TestFetchReadSecondReadAfterEOF(t *testing.T) {
	pr, pw := io.Pipe()
	waitCh := make(chan error, 1)
	waitCh <- nil
	close(waitCh)
	f := &fetchRead{pr: pr, wait: waitCh}
	_ = pw.Close()

	n, err := f.Read(make([]byte, 8))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)

	// A second Read after EOF must return io.EOF, not block on the
	// consumed wait channel.
	done := make(chan struct{})
	var err2 error
	go func() { _, err2 = f.Read(make([]byte, 8)); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Read blocked past EOF")
	}
	assert.ErrorIs(t, err2, io.EOF)
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
