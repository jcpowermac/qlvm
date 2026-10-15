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
