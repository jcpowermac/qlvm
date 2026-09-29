// Package mounts holds the dom0-side disk helpers shared by create/delete.
package mounts

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// errSameFS marks "clone not possible" failures (EXDEV across filesystems,
// or EINVAL/EOPNOTSUPP where this filesystem does not support clones).
var errSameFS = errors.New("same filesystem required (clone not supported across or on this filesystem)")

// ficloneFn is the FICLONE ioctl seam, injectable for tests. FICLONE clones
// the whole file and is what coreutils' cp --reflink uses; btrfs rejects
// the clone_file_range(2) syscall form (EINVAL), so the ioctl is the
// supported path on the dom0.
var ficloneFn = func(dstFD, srcFD uintptr) error {
	return unix.IoctlFileClone(int(dstFD), int(srcFD))
}

// Reflink copies src onto dst with the FICLONE ioctl (shared extents, no
// byte copy). Requires dst and src on the same clone-capable filesystem —
// on EXDEV/EINVAL/EOPNOTSUPP it fails with a "same filesystem" error
// instead of falling back to a byte copy (that would silently break the
// btrfs space model the VM storage assumes).
func Reflink(dst, src string) error {
	srcF, err := os.Open(src) // #nosec G304 -- src is the template raw disk under the qlvm root
	if err != nil {
		return err
	}
	// #nosec G304 -- dst is the VM disk path under the qlvm state root
	dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = srcF.Close()
		return err
	}
	// dst close errors (e.g. EIO) are data-loss signals, not ignorable.
	closeAll := func() error {
		errD := dstF.Close()
		_ = srcF.Close()
		return errD
	}
	if err := ficloneFn(dstF.Fd(), srcF.Fd()); err != nil {
		_ = closeAll()
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return fmt.Errorf("reflink %s -> %s: %w", src, dst, errSameFS)
		}
		return fmt.Errorf("reflink %s -> %s: %w", src, dst, err)
	}
	return closeAll()
}
