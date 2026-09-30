package ostree

import (
	"context"
	"fmt"
	"os/exec"
)

// xfsAdminFn is the seam tests replace. xfs_admin is a filesystem
// utility (not a management CLI) with no Go binding.
var xfsAdminFn = func(dev string) error {
	return exec.Command("xfs_admin", "-U", "generate", dev).Run()
}

// UniqueXFS gives a reflinked template copy unique XFS filesystem UUIDs
// (boot + root partitions) so dom0 can loop-mount a VM disk and its
// template simultaneously. Non-XFS fstypes are a no-op.
func UniqueXFS(ctx context.Context, fs FS, disk, fstype string) error {
	if fstype != "xfs" {
		return nil
	}
	loop, err := fs.LoopAttach(disk)
	if err != nil {
		return err
	}
	defer func() { _ = fs.LoopDetach(loop) }()
	root, boot, rootPart, bootPart, err := findParts(ctx, fs, loop, true, true, fstype, "uuid")
	if err != nil {
		return err
	}
	for _, pm := range []partMount{root, boot} { // unmount before xfs_admin
		_ = fs.Umount(pm.target)
		pm.cleanup()
	}
	for _, part := range []string{rootPart, bootPart} {
		if part == "" {
			continue
		}
		if err := xfsAdminFn("/dev/" + part); err != nil {
			return fmt.Errorf("xfs uuid %s: %w", part, err)
		}
	}
	return nil
}
