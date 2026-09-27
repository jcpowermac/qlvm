package vm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeSSHConfig(t *testing.T, home, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(content), 0o600))
}

func readSSHConfig(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "config")) // #nosec G304 -- t.TempDir path

	require.NoError(t, err)
	return string(b)
}

func TestAddSSHConfigIdempotent(t *testing.T) {
	t.Run("append leaves existing blocks untouched; second add is a no-op", func(t *testing.T) {
		home := t.TempDir()
		pre := "Host other\n  HostName 1.2.3.4\n  Port 2222\n"
		writeSSHConfig(t, home, pre)

		m := &Meta{Name: "vm1", IP: "10.100.1.10"}
		require.NoError(t, AddSSHConfig(home, m))
		want := pre + "\nHost vm1\n  HostName 10.100.1.10\n  User user\n"
		require.Equal(t, want, readSSHConfig(t, home))

		require.NoError(t, AddSSHConfig(home, m))
		require.Equal(t, want, readSSHConfig(t, home), "idempotent: second add changes nothing")
	})

	t.Run("existing Host block is replaced in place", func(t *testing.T) {
		home := t.TempDir()
		pre := "Host other\n  HostName 1.2.3.4\n\nHost vm1\n  HostName 10.0.0.1\n  User stale\n\nHost last\n  HostName 5.6.7.8\n"
		writeSSHConfig(t, home, pre)

		require.NoError(t, AddSSHConfig(home, &Meta{Name: "vm1", IP: "10.100.1.11"}))
		want := "Host other\n  HostName 1.2.3.4\n\nHost vm1\n  HostName 10.100.1.11\n  User user\n\nHost last\n  HostName 5.6.7.8\n"
		require.Equal(t, want, readSSHConfig(t, home))
	})

	t.Run("creates .ssh/config when missing", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, AddSSHConfig(home, &Meta{Name: "vm1", IP: "10.100.1.10"}))
		require.Equal(t, "Host vm1\n  HostName 10.100.1.10\n  User user\n", readSSHConfig(t, home))
	})
}

func TestRemoveSSHConfig(t *testing.T) {
	t.Run("removes only the named block", func(t *testing.T) {
		home := t.TempDir()
		pre := "Host other\n  HostName 1.2.3.4\n\nHost vm1\n  HostName 10.100.1.10\n  User user\n\nHost last\n  HostName 5.6.7.8\n"
		writeSSHConfig(t, home, pre)

		require.NoError(t, RemoveSSHConfig(home, "vm1"))
		require.Equal(t, "Host other\n  HostName 1.2.3.4\n\nHost last\n  HostName 5.6.7.8\n", readSSHConfig(t, home))
	})

	t.Run("missing name or missing file is a no-op", func(t *testing.T) {
		home := t.TempDir()
		pre := "Host other\n  HostName 1.2.3.4\n"
		writeSSHConfig(t, home, pre)
		require.NoError(t, RemoveSSHConfig(home, "vm1"))
		require.Equal(t, pre, readSSHConfig(t, home))

		require.NoError(t, RemoveSSHConfig(t.TempDir(), "vm1"))
	})
}
