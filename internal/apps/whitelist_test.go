package apps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeVMDesktop(t *testing.T, cache, vm, base, content string) {
	t.Helper()
	dir := filepath.Join(cache, vm)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, base), []byte(content), 0o600))
}

const (
	firefoxDesktop = "[Desktop Entry]\nType=Application\nName=Firefox\nComment=Web Browser\nExec=firefox %U\nIcon=firefox\nTerminal=false\n"
	codeDesktop    = "[Desktop Entry]\nType=Application\nName=Code\nExec=code --no-sandbox %F\nTerminal=false\n"
	hiddenDesktop  = "[Desktop Entry]\nType=Application\nName=Hidden\nNoDisplay=true\nExec=hidden\n"
	noExecDesktop  = "[Desktop Entry]\nType=Application\nName=NoExec\nIcon=noexec\n"
)

func TestReadWhitelistAbsent(t *testing.T) {
	cache := t.TempDir()
	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Nil(t, names, "absent file means no whitelist")
}

func TestReadWhitelistSortedDeduped(t *testing.T) {
	cache := t.TempDir()
	vmDir := filepath.Join(cache, "web")
	require.NoError(t, os.MkdirAll(vmDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, WhitelistFileName),
		[]byte("z.desktop\na.desktop\nz.desktop\n"), 0o600))
	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.desktop", "z.desktop"}, names)
}

func TestReadWhitelistEmptyFile(t *testing.T) {
	cache := t.TempDir()
	vmDir := filepath.Join(cache, "web")
	require.NoError(t, os.MkdirAll(vmDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, WhitelistFileName), []byte(""), 0o600))
	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Empty(t, names)
	assert.NotNil(t, names, "empty-but-present file is distinct from absent")
}

func TestWriteWhitelistRoundTrip(t *testing.T) {
	cache := t.TempDir()
	require.NoError(t, WriteWhitelist(cache, "web", []string{"b.desktop", "a.desktop", "b.desktop"}))
	raw, err := os.ReadFile(filepath.Join(cache, "web", WhitelistFileName)) //nolint:gosec,G304 // t.TempDir path
	require.NoError(t, err)
	assert.Equal(t, "a.desktop\nb.desktop\n", string(raw), "sorted, deduped, one per line")
	info, err := os.Stat(filepath.Join(cache, "web", WhitelistFileName))
	require.NoError(t, err)
	assert.True(t, info.Mode().Perm() == 0o644, "0644 so the desktop user can read (got %o)", info.Mode().Perm())

	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.desktop", "b.desktop"}, names)
}

func TestWriteWhitelistEmptyDeletesFile(t *testing.T) {
	cache := t.TempDir()
	require.NoError(t, WriteWhitelist(cache, "web", []string{"a.desktop"}))
	require.NoError(t, WriteWhitelist(cache, "web", nil))
	_, err := os.Stat(filepath.Join(cache, "web", WhitelistFileName))
	assert.True(t, os.IsNotExist(err), "empty list deletes the file (all apps visible)")
}

func TestWriteWhitelistOverwrites(t *testing.T) {
	cache := t.TempDir()
	require.NoError(t, WriteWhitelist(cache, "web", []string{"a.desktop", "b.desktop"}))
	require.NoError(t, WriteWhitelist(cache, "web", []string{"c.desktop"}))
	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"c.desktop"}, names)
}

func TestCachedApps(t *testing.T) {
	cache := t.TempDir()
	writeVMDesktop(t, cache, "web", "Firefox.desktop", firefoxDesktop)
	writeVMDesktop(t, cache, "web", "code.desktop", codeDesktop)
	writeVMDesktop(t, cache, "web", "hidden.desktop", hiddenDesktop)
	writeVMDesktop(t, cache, "web", "noexec.desktop", noExecDesktop)

	apps, err := CachedApps(cache, "web")
	require.NoError(t, err)
	require.Len(t, apps, 2, "NoDisplay and Exec-less entries excluded")
	assert.Equal(t, "Code", apps[0].Name, "sorted by Name")
	assert.Equal(t, "firefox", apps[1].Icon)
	assert.Equal(t, "code --no-sandbox", apps[0].Exec, "field codes stripped")
	assert.Equal(t, DefaultIcon, apps[0].Icon, "missing Icon gets the default")
}

func TestCachedAppsMissingVMDir(t *testing.T) {
	cache := t.TempDir()
	apps, err := CachedApps(cache, "never-synced")
	require.NoError(t, err)
	assert.Empty(t, apps)
}

func TestVisibleAppsNoWhitelistShowsAll(t *testing.T) {
	cache := t.TempDir()
	writeVMDesktop(t, cache, "web", "Firefox.desktop", firefoxDesktop)
	writeVMDesktop(t, cache, "web", "code.desktop", codeDesktop)

	apps, err := VisibleApps(cache, "web")
	require.NoError(t, err)
	require.Len(t, apps, 2, "absent whitelist = all apps visible")
}

func TestVisibleAppsFiltered(t *testing.T) {
	cache := t.TempDir()
	writeVMDesktop(t, cache, "web", "Firefox.desktop", firefoxDesktop)
	writeVMDesktop(t, cache, "web", "code.desktop", codeDesktop)
	require.NoError(t, WriteWhitelist(cache, "web", []string{"firefox.desktop"}))

	apps, err := VisibleApps(cache, "web")
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "Firefox", apps[0].Name)
}

func TestSyncCachePreservesWhitelist(t *testing.T) {
	cache := t.TempDir()
	writeVMDesktop(t, cache, "web", "Firefox.desktop", firefoxDesktop)
	writeVMDesktop(t, cache, "web", "code.desktop", codeDesktop)
	require.NoError(t, WriteWhitelist(cache, "web", []string{"firefox.desktop"}))
	before, err := os.ReadFile(filepath.Join(cache, "web", WhitelistFileName)) //nolint:gosec,G304 // t.TempDir path
	require.NoError(t, err)

	entries := map[string]string{
		"Firefox": firefoxDesktop,
		"Code":    codeDesktop,
		"NewApp":  "[Desktop Entry]\nType=Application\nName=NewApp\nExec=newapp\n",
	}
	n, err := SyncCache(cache, "web", entries)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	after, err := os.ReadFile(filepath.Join(cache, "web", WhitelistFileName)) //nolint:gosec,G304 // t.TempDir path
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "sync must not clobber user whitelist state")

	names, err := ReadWhitelist(cache, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"firefox.desktop"}, names)

	// Cache itself refreshed: new entry present, whitelist still filters.
	visible, err := VisibleApps(cache, "web")
	require.NoError(t, err)
	assert.Len(t, visible, 1)
	apps, err := CachedApps(cache, "web")
	require.NoError(t, err)
	assert.Len(t, apps, 3)
}

func TestSyncCacheNoWhitelistCreatesNone(t *testing.T) {
	cache := t.TempDir()
	n, err := SyncCache(cache, "web", map[string]string{"Firefox": firefoxDesktop})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, statErr := os.Stat(filepath.Join(cache, "web", WhitelistFileName))
	assert.True(t, statErr != nil && os.IsNotExist(statErr), "no whitelist before sync, none after")
}

func TestVisibleAppsCaseInsensitiveMatch(t *testing.T) {
	// Cache basenames are Name-derived (Firefox.desktop); whitelist
	// entries may be lowercase (Qubes convention). Matching must not lose
	// apps over case.
	cache := t.TempDir()
	writeVMDesktop(t, cache, "web", "Firefox.desktop", firefoxDesktop)
	require.NoError(t, WriteWhitelist(cache, "web", []string{"firefox.desktop"}))
	apps, err := VisibleApps(cache, "web")
	require.NoError(t, err)
	assert.Len(t, apps, 1)
}
