package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/apps"
	"github.com/jcpowermac/qlvm/internal/config"
)

const (
	appsTestFirefox = "[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n"
	appsTestCode    = "[Desktop Entry]\nType=Application\nName=Code\nExec=code\n"
)

// setupAppsAgent builds an Agent with a temp state root, one VM meta, and a
// per-VM desktop cache populated with two apps.
func setupAppsAgent(t *testing.T) *Agent {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "vms", "web"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "vms", "web", "meta.yaml"),
		[]byte("name: web\ntype: app\nip: 10.100.1.9\n"), 0o600))
	cache := filepath.Join(root, "desktop-cache", "web")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "Firefox.desktop"), []byte(appsTestFirefox), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "code.desktop"), []byte(appsTestCode), 0o644))
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{},
		&config.Config{}, filepath.Join(root, "coremap.yaml"), root)
	return a
}

func TestAgentListApps(t *testing.T) {
	a := setupAppsAgent(t)

	// No whitelist file: everything reported and visible (all checked).
	out, err := a.ListApps("web")
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "Code", out[0].Name)
	assert.True(t, out[0].Whitelisted)
	assert.True(t, out[1].Whitelisted)

	// With a whitelist: flags reflect it.
	require.NoError(t, apps.WriteWhitelist(a.appCacheDir(), "web", []string{"firefox.desktop"}))
	out, err = a.ListApps("web")
	require.NoError(t, err)
	require.Len(t, out, 2)
	byName := map[string]bool{}
	for _, i := range out {
		byName[i.Name] = i.Whitelisted
	}
	assert.True(t, byName["Firefox"])
	assert.False(t, byName["Code"])

	// No cache dir: empty, no error.
	out, err = a.ListApps("web")
	require.NoError(t, err)
	// (same VM, cache present) — use a VM without a cache:
	require.NoError(t, os.MkdirAll(filepath.Join(a.root, "vms", "plain"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(a.root, "vms", "plain", "meta.yaml"),
		[]byte("name: plain\nip: 10.100.1.10\n"), 0o600))
	out, err = a.ListApps("plain")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestAgentSetAppWhitelist(t *testing.T) {
	a := setupAppsAgent(t)
	require.NoError(t, a.SetAppWhitelist("web", []string{"firefox.desktop", "code.desktop"}))
	wl, err := apps.ReadWhitelist(a.appCacheDir(), "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"code.desktop", "firefox.desktop"}, wl)

	// Empty list deletes the file (all apps visible).
	require.NoError(t, a.SetAppWhitelist("web", nil))
	wl, err = apps.ReadWhitelist(a.appCacheDir(), "web")
	require.NoError(t, err)
	assert.Nil(t, wl)
}

func TestAgentSyncAppsIdentityOrdering(t *testing.T) {
	a := setupAppsAgent(t)
	rootHome := t.TempDir()  // first candidate, has no key
	wheelHome := t.TempDir() // second candidate, works
	oldWheels := wheelGroupMembersFn
	wheelGroupMembersFn = func() []string { return []string{"desktopuser"} }
	t.Cleanup(func() { wheelGroupMembersFn = oldWheels })
	// Fake user lookup via os/user needs real /etc/passwd; instead stub the
	// home resolution directly through appSyncHomes' user lookup seam.
	oldLookup := userLookupFn
	userLookupFn = func(name string) (string, error) {
		if name == "desktopuser" {
			return wheelHome, nil
		}
		return "", errors.New("no such user")
	}
	t.Cleanup(func() { userLookupFn = oldLookup })

	var tried []string
	a.homeDir = rootHome
	a.appSyncFetch = func(_ context.Context, home, host string) (map[string]string, error) {
		tried = append(tried, home)
		if home == wheelHome {
			return map[string]string{"Firefox": appsTestFirefox, "Code": appsTestCode, "Extra": "[Desktop Entry]\nType=Application\nName=Extra\nExec=extra\n"}, nil
		}
		return nil, errors.New("no ssh identity in " + home)
	}

	n, err := a.SyncApps("web")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{rootHome, wheelHome}, tried, "root home first, then wheel members")

	// Sync must have preserved nothing (no prior whitelist) and written 3 entries.
	all, err := apps.CachedApps(a.appCacheDir(), "web")
	require.NoError(t, err)
	assert.Len(t, all, 3)
	wl, err := apps.ReadWhitelist(a.appCacheDir(), "web")
	require.NoError(t, err)
	assert.Nil(t, wl)
}

func TestAgentSyncAppsPreservesWhitelist(t *testing.T) {
	a := setupAppsAgent(t)
	require.NoError(t, apps.WriteWhitelist(a.appCacheDir(), "web", []string{"code.desktop"}))
	a.homeDir = t.TempDir()
	a.appSyncFetch = func(_ context.Context, _, _ string) (map[string]string, error) {
		return map[string]string{"Firefox": appsTestFirefox, "Code": appsTestCode}, nil
	}
	n, err := a.SyncApps("web")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	wl, err := apps.ReadWhitelist(a.appCacheDir(), "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"code.desktop"}, wl, "sync must not clobber the whitelist")
}

func TestAgentSyncAppsNoIdentity(t *testing.T) {
	a := setupAppsAgent(t)
	a.homeDir = t.TempDir()
	oldWheels := wheelGroupMembersFn
	wheelGroupMembersFn = func() []string { return nil }
	t.Cleanup(func() { wheelGroupMembersFn = oldWheels })
	a.appSyncFetch = func(_ context.Context, _, _ string) (map[string]string, error) {
		return nil, errors.New("no ssh key")
	}
	_, err := a.SyncApps("web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no working ssh identity")
	assert.Contains(t, err.Error(), "no ssh key")
}

func TestAgentSyncAppsUnknownVM(t *testing.T) {
	a := setupAppsAgent(t)
	_, err := a.SyncApps("nosuchvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nosuchvm")
}
