package apps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStream is a concatenated /usr/share/applications/*.desktop fetch:
// three entries, the last without a Name (fallback key expected).
const testStream = "[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n\n" +
	"[Desktop Entry]\nType=Application\nName=Code\nExec=code %F\n\n" +
	"[Desktop Entry]\nType=Application\nNoDisplay=true\nExec=hidden\n"

func TestSplitDesktops(t *testing.T) {
	entries, err := SplitDesktops([]byte(testStream))
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n\n", entries["Firefox"])
	assert.Equal(t, "[Desktop Entry]\nType=Application\nName=Code\nExec=code %F\n\n", entries["Code"])
	assert.Equal(t, "[Desktop Entry]\nType=Application\nNoDisplay=true\nExec=hidden\n", entries["entry-3"])
	_, hasEmpty := entries[""]
	assert.False(t, hasEmpty, "entry without Name must not take the empty key")

	_, err = SplitDesktops([]byte("no markers here"))
	require.Error(t, err)
}

func writeCacheFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

func TestEmitRofiGolden(t *testing.T) {
	cache := t.TempDir()
	vmDir := filepath.Join(cache, "web")
	writeCacheFile(t, vmDir, "firefox.desktop", "[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n")
	writeCacheFile(t, vmDir, "hidden.desktop", "[Desktop Entry]\nType=Application\nName=Hidden\nExec=hidden\nNoDisplay=true\n")
	writeCacheFile(t, vmDir, "mail.desktop", "[Desktop Entry]\nType=Service\nName=Mail Agent\nExec=maild\n")
	writeCacheFile(t, vmDir, "noexec.desktop", "[Desktop Entry]\nType=Application\nName=No Exec\n")

	out, err := EmitRofi(cache)
	require.NoError(t, err)
	// Exactly one line: Type=Application + non-hidden + Name and Exec;
	// exec field code %U stripped, icon kept.
	assert.Equal(t, "[web] Firefox\x00icon\x1ffirefox\x1finfo\x1fweb|firefox\n", out)

	// A missing cache dir is an empty menu, not an error.
	out, err = EmitRofi(filepath.Join(cache, "nope"))
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestWriteCache(t *testing.T) {
	cache := t.TempDir()
	n, err := WriteCache(cache, "web", map[string]string{"Firefox": "c1", "Code": "c2"})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	require.Equal(t, "c1", readCache(t, filepath.Join(cache, "web", "Firefox.desktop")))
	require.Equal(t, "c2", readCache(t, filepath.Join(cache, "web", "Code.desktop")))

	// A resync replaces the previous cache: dropped entries disappear.
	n, err = WriteCache(cache, "web", map[string]string{"Code": "c2b"})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, err = os.Stat(filepath.Join(cache, "web", "Firefox.desktop"))
	assert.True(t, os.IsNotExist(err))
	require.Equal(t, "c2b", readCache(t, filepath.Join(cache, "web", "Code.desktop")))

	// A failed write (unwritable cache dir) leaves the previous set intact
	// and no temp dir behind.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}
	require.NoError(t, os.Chmod(cache, 0o500)) // #nosec G302 -- t.TempDir fixture, restored below
	t.Cleanup(func() { _ = os.Chmod(cache, 0o700) }) //nolint:gosec // t.TempDir fixture restore
	_, err = WriteCache(cache, "web", map[string]string{"Code": "c3"})
	require.Error(t, err)
	require.Equal(t, "c2b", readCache(t, filepath.Join(cache, "web", "Code.desktop")))
	_, serr := os.Stat(filepath.Join(cache, "web.tmp"))
	assert.True(t, os.IsNotExist(serr), "failed write must not leave a temp dir")
}

func readCache(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	return string(data)
}

func TestLaunchStartsStoppedVM(t *testing.T) {
	const info = "web|firefox"
	var calls []string
	err := Launch(info, t.TempDir(),
		func(_ string) bool { calls = append(calls, "running"); return false },
		func(vm string) error { calls = append(calls, "start:"+vm); return nil },
		func(vm string) error { calls = append(calls, "waitSSH:"+vm); return nil },
		func(vm, exec string) error { calls = append(calls, "run:"+vm+":"+exec); return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"running", "start:web", "waitSSH:web", "run:web:firefox"}, calls)

	// A running VM is never started nor waited on.
	calls = nil
	err = Launch(info, t.TempDir(),
		func(_ string) bool { calls = append(calls, "running"); return true },
		func(_ string) error { calls = append(calls, "start"); return nil },
		func(_ string) error { calls = append(calls, "waitSSH"); return nil },
		func(vm, exec string) error { calls = append(calls, "run:"+vm+":"+exec); return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"running", "run:web:firefox"}, calls)

	// Malformed info (no vm|exec split) is an error, no callbacks.
	calls = nil
	err = Launch("no-pipe", t.TempDir(),
		func(_ string) bool { return true },
		func(_ string) error { return nil },
		func(_ string) error { return nil },
		func(_, _ string) error { calls = append(calls, "run"); return nil },
	)
	require.Error(t, err)
	assert.Empty(t, calls)

	// Empty exec ("web|") is rejected the same way: no waypipe with no app.
	calls = nil
	err = Launch("web|", t.TempDir(),
		func(_ string) bool { return true },
		func(_ string) error { return nil },
		func(_ string) error { return nil },
		func(_, _ string) error { calls = append(calls, "run"); return nil },
	)
	require.Error(t, err)
	assert.Empty(t, calls)
}
