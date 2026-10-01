package vm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	return b
}

func TestEnsureKernelSeedsMissing(t *testing.T) {
	tpl, vmDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "vmlinuz"), []byte("K"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "initramfs"), []byte("I"), 0o600))

	require.NoError(t, EnsureKernel(vmDir, tpl))
	require.Equal(t, []byte("K"), readBytes(t, filepath.Join(vmDir, "vmlinuz")))
	require.Equal(t, []byte("I"), readBytes(t, filepath.Join(vmDir, "initramfs")))
}

func TestEnsureKernelKeepsSyncedKernel(t *testing.T) {
	tpl, vmDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "vmlinuz"), []byte("OLD"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "initramfs"), []byte("I"), 0o600))
	// The VM's own vmlinuz (a sync-kernel'd upgrade) must survive the seed.
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "vmlinuz"), []byte("NEW"), 0o600))

	require.NoError(t, EnsureKernel(vmDir, tpl))
	require.Equal(t, []byte("NEW"), readBytes(t, filepath.Join(vmDir, "vmlinuz")))
	require.Equal(t, []byte("I"), readBytes(t, filepath.Join(vmDir, "initramfs")))
}

func TestEnsureKernelMissingSource(t *testing.T) {
	require.Error(t, EnsureKernel(t.TempDir(), t.TempDir()), "no kernel in the template dir must fail the seed")
}
