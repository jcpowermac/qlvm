package ostree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uuidFS fakes a reflinked template disk: loop9p1 is /boot, loop9p2 is the
// ostree root.
func uuidFS() *fakeFS {
	f := newFakeFS()
	f.loops = []string{"/dev/loop9"}
	f.parts["/dev/loop9"] = []string{"loop9p1", "loop9p2"}
	f.partRoots = map[string]string{"loop9p1": "b", "loop9p2": "r"}
	f.dirs = map[string][]string{
		"b":                   {"ostree"},
		"b/ostree":            {"os1"},
		"b/ostree/os1":        {"vmlinuz-6.1.0"},
		"r":                   {"ostree"},
		"r/ostree":            {"boot.1", "deploy", "repo"},
		"r/ostree/boot.1":     {"os1"},
		"r/ostree/boot.1/os1": {"abc123"},
	}
	return f
}

// recordXFS replaces xfsAdminFn for the duration of the test and records the
// device args (and any failure) it is called with.
func recordXFS(t *testing.T, calls *[]string, err error) {
	t.Helper()
	old := xfsAdminFn
	xfsAdminFn = func(dev string) error {
		*calls = append(*calls, dev)
		return err
	}
	t.Cleanup(func() { xfsAdminFn = old })
}

func TestUniqueXFSRegeneratesBootAndRoot(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk.img")
	require.NoError(t, os.WriteFile(disk, []byte("raw"), 0o600))
	f := uuidFS()
	var calls []string
	recordXFS(t, &calls, nil)

	require.NoError(t, UniqueXFS(context.Background(), f, disk, "xfs"))

	assert.Equal(t, []string{"/dev/loop9p2", "/dev/loop9p1"}, calls,
		"xfs_admin must run exactly once on the root partition then the boot partition")
	assert.Equal(t, []string{disk}, f.attached, "the VM disk must be loop-attached")
	assert.Equal(t, []string{"/dev/loop9"}, f.detached, "the loop must be detached")
	assert.Len(t, f.umounted, 2, "the probe mounts must be unmounted before xfs_admin")
}

func TestUniqueXFSNonXFSIsNoop(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk.img")
	require.NoError(t, os.WriteFile(disk, []byte("raw"), 0o600))
	f := uuidFS()
	var calls []string
	recordXFS(t, &calls, nil)

	require.NoError(t, UniqueXFS(context.Background(), f, disk, "ext4"))

	assert.Empty(t, calls, "non-XFS disks must not be touched")
	assert.Empty(t, f.attached, "non-XFS disks must not be loop-attached")
	assert.Empty(t, f.detached)
}

func TestUniqueXFSAdminFailureDetaches(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk.img")
	require.NoError(t, os.WriteFile(disk, []byte("raw"), 0o600))
	f := uuidFS()
	var calls []string
	recordXFS(t, &calls, errors.New("cannot modify a mounted filesystem"))

	err := UniqueXFS(context.Background(), f, disk, "xfs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loop9p2", "the failing partition must be named")
	assert.Equal(t, []string{"/dev/loop9p2"}, calls, "no further partition after the first failure")
	assert.Equal(t, []string{"/dev/loop9"}, f.detached, "the loop must be detached on failure")
}
