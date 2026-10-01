package vm

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// bootFiles are the per-VM boot kernel and initramfs the domain config boots
// (domcfg). They live in the VM's state dir, not the template dir: each
// VM's `sync-kernel` writes here, so one VM's in-guest upgrade never
// re-points a sibling VM's boot at its kernel.
var bootFiles = []string{"vmlinuz", "initramfs"}

// EnsureKernel seeds the VM's boot files from the template dir if missing.
// Existing files are never replaced: a sync-kernel'd kernel stays the VM's
// boot even though the template dir still holds the bake-time kernel.
func EnsureKernel(vmDir, tplDir string) error {
	for _, name := range bootFiles {
		dst := filepath.Join(vmDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := copyFile(filepath.Join(tplDir, name), dst); err != nil {
			return fmt.Errorf("seed %s from %s: %w", name, tplDir, err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- state-dir paths with fixed file names
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}
