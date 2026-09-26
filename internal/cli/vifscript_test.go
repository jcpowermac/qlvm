package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallVifScript(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "qlvm-vif"), []byte("vifbin"), 0o600))
	destDir := filepath.Join(t.TempDir(), "etc", "xen", "scripts")

	require.NoError(t, installVifScript(srcDir, destDir))
	b, err := os.ReadFile(filepath.Join(destDir, "vif-ovn")) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, []byte("vifbin"), b)
	fi, err := os.Stat(filepath.Join(destDir, "vif-ovn"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), fi.Mode().Perm())

	// re-run overwrites in place
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "qlvm-vif"), []byte("vifbin2"), 0o600))
	require.NoError(t, installVifScript(srcDir, destDir))
	b, err = os.ReadFile(filepath.Join(destDir, "vif-ovn")) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, []byte("vifbin2"), b)

	// missing source binary is a clear error
	err = installVifScript(t.TempDir(), destDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qlvm-vif")
}
