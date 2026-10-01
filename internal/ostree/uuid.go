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

// UniqueXFS gives a reflinked template copy a unique XFS filesystem UUID
// on the ROOT partition only, so dom0 can loop-mount a VM disk and its
// template simultaneously without colliding. Non-XFS fstypes are a no-op.
//
// Root-only is deliberate (live regression, 2026-09-30): the template image
// pins /boot by its XFS UUID in the baked /etc/fstab, so regenerating the
// boot partition's UUID made boot.mount time out and the guest drop to
// EMERGENCY with sshd never starting. The uniqueness need is dom0-side:
// dom0 surgery loop-mounts partitions by device path (never by UUID),
// baking runs in the builder container, and nothing on dom0 mounts a VM
// /boot by UUID — a shared /boot UUID across VMs is the pre-Task-1 state,
// which booted fine. Only the root UUID must differ per VM.
func UniqueXFS(ctx context.Context, fs FS, disk, fstype string) error {
	if fstype != "xfs" {
		return nil
	}
	loop, err := fs.LoopAttach(disk)
	if err != nil {
		return err
	}
	defer func() { _ = fs.LoopDetach(loop) }()
	root, boot, rootPart, _, err := findParts(ctx, fs, loop, true, true, fstype, "uuid")
	if err != nil {
		return err
	}
	for _, pm := range []partMount{root, boot} { // unmount before xfs_admin
		_ = fs.Umount(pm.target)
		pm.cleanup()
	}
	// findParts errors when no root is found, so rootPart is non-empty here;
	// the boot partition is intentionally left at the template's UUID (see
	// the doc comment — the image fstab pins it).
	if err := xfsAdminFn("/dev/" + rootPart); err != nil {
		return fmt.Errorf("xfs uuid %s: %w", rootPart, err)
	}
	return nil
}
