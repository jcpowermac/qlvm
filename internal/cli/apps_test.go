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
