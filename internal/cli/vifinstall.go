package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InstallVifScript copies the prebuilt qlvm-vif binary from src to dst
// (spec §5.7: /etc/xen/scripts/vif-ovn) and marks it executable. This
// consolidates Task 6's dir-based installVifScript.
func InstallVifScript(dst, src string) error {
	// #nosec G304 -- src is the caller-provided binary path (next to the
	// qlvm executable), not user input.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("qlvm-vif binary %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	// #nosec G302,G304 -- vif-ovn must carry the exec bit: libxl (root)
	// execs it as the Xen vif hotplug script, nothing else needs access.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
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
	// #nosec G302 -- consumed only by the Xen toolstack as root.
	return os.Chmod(dst, 0o755)
}
