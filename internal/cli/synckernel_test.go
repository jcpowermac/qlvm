package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeKernelHost struct {
	cmds  []string
	files map[string][]byte
}

func (f *fakeKernelHost) Run(_ context.Context, cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return "kernel-core-6.11.9-300.fc44.x86_64\n", nil
}

func (f *fakeKernelHost) FetchFile(_ context.Context, path string) (io.ReadCloser, error) {
	body, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("no such file: %s", path)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func TestSyncKernelFetchFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()

	// Kernel fetch succeeds, initramfs fetch fails mid-flow.
	host := &fakeKernelHost{files: map[string][]byte{
		"/boot/vmlinuz-6.11.9-300.fc44": []byte("KERNEL"),
	}}

	_, err := syncKernel(context.Background(), host, dir)
	require.Error(t, err)

	// No partial copy: not a single boot file may exist after a failure.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, "vmlinuz", e.Name())
		assert.NotEqual(t, "vmlinuz-6.11.9-300.fc44", e.Name())
		assert.NotEqual(t, "initramfs", e.Name())
		assert.NotEqual(t, "initramfs-6.11.9-300.fc44.img", e.Name())
	}
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	return body
}

func TestSyncKernelFlow(t *testing.T) {
	dir := t.TempDir()

	host := &fakeKernelHost{files: map[string][]byte{
		"/boot/vmlinuz-6.11.9-300.fc44":       []byte("KERNEL"),
		"/boot/initramfs-6.11.9-300.fc44.img": []byte("INITRAMFS"),
	}}

	ver, err := syncKernel(context.Background(), host, dir)
	require.NoError(t, err)
	assert.Equal(t, "6.11.9-300.fc44", ver)
	assert.Equal(t, []string{"rpm -q kernel-core --last | head -1"}, host.cmds)

	// Exact fetched names, bodies intact...
	assert.Equal(t, []byte("KERNEL"), readAll(t, filepath.Join(dir, "vmlinuz-6.11.9-300.fc44")))
	assert.Equal(t, []byte("INITRAMFS"), readAll(t, filepath.Join(dir, "initramfs-6.11.9-300.fc44.img")))
	// ...and the unversioned aliases the VM's domain config boots.
	assert.Equal(t, []byte("KERNEL"), readAll(t, filepath.Join(dir, "vmlinuz")))
	assert.Equal(t, []byte("INITRAMFS"), readAll(t, filepath.Join(dir, "initramfs")))
}
