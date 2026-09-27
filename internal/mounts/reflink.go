// Package mounts holds the dom0-side disk helpers shared by create/delete.
package mounts

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// errSameFS marks "dst and src must be on the same filesystem" failures
// (EXDEV/EOPNOTSUPP from clone_file_range).
var errSameFS = errors.New("same filesystem required (clone not supported across or on this filesystem)")

// cloneFileRangeSyscall is the clone_file_range(2) number, which x/sys does
// not export as a helper; 439 on both amd64 and arm64 (the two dom0 archs).
const cloneFileRangeSyscall = 439

// cloneFn is the clone_file_range(2) seam, injectable for tests. Offsets are
// NULL (0): both fds are fresh and the kernel advances the file offsets.
var cloneFn = func(dstFD, srcFD uintptr) (uint64, error) {
	n, _, err := unix.Syscall6(cloneFileRangeSyscall, dstFD, 0, srcFD, 0, 1<<30, 0)
	if err == 0 {
		return uint64(n), nil
	}
	return 0, err
}

// Reflink copies src onto dst with clone_file_range(2) (FICLONE semantics:
// same filesystem, shared extents). Requires dst and src on the same
// filesystem — on EXDEV/EOPNOTSUPP it fails with a "same filesystem" error
// instead of falling back to a byte copy (that would silently break the
// btrfs quota/space model the VM storage assumes).
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
	var off uint64
	const chunk = uint64(1) << 30
	for {
		n, err := cloneFn(dstF.Fd(), srcF.Fd())
		if err != nil {
			_ = closeAll()
			if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EOPNOTSUPP) {
				return fmt.Errorf("reflink %s -> %s: %w", src, dst, errSameFS)
			}
			return fmt.Errorf("reflink %s -> %s (offset %d): %w", src, dst, off, err)
		}
		off += n
		if n < chunk {
			return closeAll()
		}
	}
}
