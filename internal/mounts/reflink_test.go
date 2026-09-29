package mounts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestReflinkError(t *testing.T) {
	old := ficloneFn
	defer func() { ficloneFn = old }()
	ficloneFn = func(_, _ uintptr) error { return unix.EXDEV }

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	err := Reflink(filepath.Join(dir, "dst"), src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "same filesystem")
}

func TestReflinkCopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i * 7)
	}
	require.NoError(t, os.WriteFile(src, data, 0o600))

	dst := filepath.Join(dir, "dst")
	err := Reflink(dst, src)
	// tmpfs answers FICLONE with ENOTTY (and some kernels with
	// EOPNOTSUPP/EFAULT); the real dom0 btrfs supports it.
	if err != nil && (errors.Is(err, errSameFS) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EFAULT)) {
		t.Skipf("filesystem does not support FICLONE: %v", err)
	}
	require.NoError(t, err)
	got, rerr := os.ReadFile(dst) // #nosec G304 -- t.TempDir path
	require.NoError(t, rerr)
	require.True(t, bytes.Equal(data, got), "dst must be byte-identical to src")
}
