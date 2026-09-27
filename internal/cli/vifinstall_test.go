package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallVifScript(t *testing.T) {
	src := filepath.Join(t.TempDir(), "qlvm-vif")
	require.NoError(t, os.WriteFile(src, []byte("vifbin"), 0o600))
	dst := filepath.Join(t.TempDir(), "etc", "xen", "scripts", "vif-ovn")

	require.NoError(t, InstallVifScript(dst, src))
	b, err := os.ReadFile(dst) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, []byte("vifbin"), b)
	fi, err := os.Stat(dst) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())

	// re-run overwrites in place
	require.NoError(t, os.WriteFile(src, []byte("vifbin2"), 0o600))
	require.NoError(t, InstallVifScript(dst, src))
	b, err = os.ReadFile(dst) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, []byte("vifbin2"), b)

	// missing source binary is a clear error
	err = InstallVifScript(dst, filepath.Join(t.TempDir(), "qlvm-vif"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qlvm-vif")
}
