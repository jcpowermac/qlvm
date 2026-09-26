package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// installVifScript copies the qlvm-vif binary from srcDir to
// destDir/vif-ovn (spec §5.7) and marks it executable. Task 14
// consolidates this into vifinstall.go; keep this file small so that swap
// stays a one-line rewire.
func installVifScript(srcDir, destDir string) error {
	src := filepath.Join(srcDir, "qlvm-vif")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("qlvm-vif binary not found in %s: %w", srcDir, err)
	}
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	// src sits next to the running executable (caller-provided dir).
	in, err := os.Open(src) // #nosec G304
	if err != nil {
		return err
	}
	defer func() {
		if cerr := in.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	dest := filepath.Join(destDir, "vif-ovn")
	// #nosec G302,G304 -- vif-ovn must carry the exec bit: libxl (root) execs
	// it as the Xen vif hotplug script, nothing else needs access.
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o750)
	if err != nil {
		return err
	}
	if _, cerr := io.Copy(out, in); cerr != nil {
		_ = out.Close()
		return cerr
	}
	if cerr := out.Close(); cerr != nil {
		return cerr
	}
	// umask may have stripped the exec bit from the OpenFile mode.
	return os.Chmod(dest, 0o750) // #nosec G302 -- exec bit is the point
}
