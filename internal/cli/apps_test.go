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

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/apps"
)

func TestAppsRofiMenu(t *testing.T) {
	t.Setenv("ROFI_RETV", "0")
	old := appsCacheDir
	appsCacheDir = t.TempDir()
	t.Cleanup(func() { appsCacheDir = old })
	web := filepath.Join(appsCacheDir, "web")
	require.NoError(t, os.MkdirAll(web, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(web, "firefox.desktop"),
		[]byte("[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n"), 0o600))

	cmd := appsCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "[web] Firefox\x00icon\x1ffirefox\x1finfo\x1fweb|firefox\n", buf.String())
}

func TestAppsRequiresRofiEnv(t *testing.T) {
	t.Setenv("ROFI_RETV", "")
	cmd := appsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ROFI_RETV")
}

func TestAppsSyncTargetsRunningVMs(t *testing.T) {
	fc := &fakeClient{
		listVMsResult: []agent.VMInfo{
			{Name: "vm2", Type: "app", State: "running"},
			{Name: "vm1", Type: "disposable", State: "stopped"},
			{Name: "vm3", Type: "app", State: "running"},
		},
	}
	useFakeClient(t, fc)

	targets, err := appsSyncTargets(nil)
	require.NoError(t, err)
	// Only running VMs, sorted.
	assert.Equal(t, []string{"vm2", "vm3"}, targets)
}

func TestAppsSyncTargetsNoRunningVMs(t *testing.T) {
	fc := &fakeClient{
		listVMsResult: []agent.VMInfo{
			{Name: "vm1", Type: "app", State: "stopped"},
		},
	}
	useFakeClient(t, fc)

	_, err := appsSyncTargets(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running VMs to sync")
}

func TestAppsSyncTargetsDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	_, err := appsSyncTargets(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestAppsSyncTargetsListVMsError(t *testing.T) {
	fc := &fakeClient{
		listVMsErr: errors.New("list failed"),
	}
	useFakeClient(t, fc)

	_, err := appsSyncTargets(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list failed")
}

// --- apps whitelist (Task 2) ---

// setupWhitelistVM builds a VM (meta present) with a two-app desktop cache.
func setupWhitelistVM(t *testing.T) (cache, root string) {
	t.Helper()
	cache = t.TempDir()
	root = t.TempDir()
	oldCache, oldRoot := appsCacheDir, installRoot
	appsCacheDir, installRoot = cache, root
	t.Cleanup(func() { appsCacheDir, installRoot = oldCache, oldRoot })
	require.NoError(t, os.MkdirAll(filepath.Join(root, "vms", "web"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "vms", "web", "meta.yaml"),
		[]byte("name: web\ntype: app\nip: 10.0.0.9\n"), 0o600))
	for base, content := range map[string]string{
		"firefox.desktop": "[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %U\nIcon=firefox\n",
		"code.desktop":    "[Desktop Entry]\nType=Application\nName=Code\nExec=code\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(cache, "web"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(cache, "web", base), []byte(content), 0o600))
	}
	return cache, root
}

func runWhitelist(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := appsWhitelistCmd()
	cmd.SetArgs(args)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return buf.String(), err
}

func TestWhitelistListNoFile(t *testing.T) {
	setupWhitelistVM(t)
	out, err := runWhitelist(t, "list", "web")
	require.NoError(t, err)
	assert.NotContains(t, out, "whitelist active")
	assert.Contains(t, out, "code.desktop  Code")
	assert.Contains(t, out, "firefox.desktop  Firefox")
}

func TestWhitelistListActive(t *testing.T) {
	setupWhitelistVM(t)
	require.NoError(t, apps.WriteWhitelist(appsCacheDir, "web", []string{"firefox.desktop"}))
	out, err := runWhitelist(t, "list", "web")
	require.NoError(t, err)
	assert.Contains(t, out, "whitelist active (1/2 apps)")
	assert.Contains(t, out, "firefox.desktop  Firefox")
	assert.NotContains(t, out, "code.desktop")
}

func TestWhitelistAddMaterializesFullSet(t *testing.T) {
	setupWhitelistVM(t)
	_, err := runWhitelist(t, "add", "web", "code")
	require.NoError(t, err)
	wl, err := apps.ReadWhitelist(appsCacheDir, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"code.desktop", "firefox.desktop"}, wl,
		"first add materializes the full set (absent = all)")
}

func TestWhitelistAddByPrefix(t *testing.T) {
	setupWhitelistVM(t)
	require.NoError(t, apps.WriteWhitelist(appsCacheDir, "web", []string{"code.desktop"}))
	_, err := runWhitelist(t, "add", "web", "firef")
	require.NoError(t, err, "unique basename prefix must resolve")
	wl, err := apps.ReadWhitelist(appsCacheDir, "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"code.desktop", "firefox.desktop"}, wl)
}

func TestWhitelistAddAmbiguousPrefix(t *testing.T) {
	setupWhitelistVM(t)
	require.NoError(t, os.WriteFile(filepath.Join(appsCacheDir, "web", "codeium.desktop"),
		[]byte("[Desktop Entry]\nType=Application\nName=Codeium\nExec=codeium\n"), 0o600))
	_, err := runWhitelist(t, "add", "web", "co")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code.desktop")
	assert.Contains(t, err.Error(), "codeium.desktop")
}

func TestWhitelistRemoveToEmptyDeletesFile(t *testing.T) {
	setupWhitelistVM(t)
	require.NoError(t, apps.WriteWhitelist(appsCacheDir, "web", []string{"firefox.desktop"}))
	_, err := runWhitelist(t, "remove", "web", "firefox")
	require.NoError(t, err)
	wl, err := apps.ReadWhitelist(appsCacheDir, "web")
	require.NoError(t, err)
	assert.Nil(t, wl, "removing the last app deletes the file (all apps visible)")
}

func TestWhitelistClearIdempotent(t *testing.T) {
	setupWhitelistVM(t)
	require.NoError(t, apps.WriteWhitelist(appsCacheDir, "web", []string{"firefox.desktop"}))
	_, err := runWhitelist(t, "clear", "web")
	require.NoError(t, err)
	_, err = runWhitelist(t, "clear", "web")
	require.NoError(t, err, "clear with no file present still succeeds")
	wl, err := apps.ReadWhitelist(appsCacheDir, "web")
	require.NoError(t, err)
	assert.Nil(t, wl)
}

func TestWhitelistUnknownVM(t *testing.T) {
	setupWhitelistVM(t)
	_, err := runWhitelist(t, "list", "nosuchvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nosuchvm")
}
