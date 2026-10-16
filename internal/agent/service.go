// Package agent provides embedded system service files for the qlvm-agent
// daemon: the systemd unit, D-Bus policy, and polkit action file. These are
// written to system paths by an external provisioning script (see the
// qlvm-agent install-system-files subcommand, planned in Task 10).
//
// Polkit enforcement is a TODO: the embedded polkit action file defines the
// authorization actions, but the daemon does not yet call polkit before
// executing mutation methods. D-Bus policy allows all local users to call
// all methods; authorization is expected to be enforced inside the daemon
// in a future task.
package agent

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jcpowermac/qlvm/internal/config"
)

//go:embed embedded/qlvm-agent.service
var serviceFile []byte

//go:embed embedded/io.qlvm.agent.conf
var dbusPolicy []byte

//go:embed embedded/io.qlvm.agent.policy
var polkitAction []byte

// ServiceFile returns the embedded systemd unit file.
func ServiceFile() []byte {
	return serviceFile
}

// DBusPolicy returns the embedded D-Bus policy file.
func DBusPolicy() []byte {
	return dbusPolicy
}

// PolkitAction returns the embedded polkit action file.
func PolkitAction() []byte {
	return polkitAction
}

//go:embed embedded
var embeddedFS embed.FS

// WriteServiceFile writes the embedded systemd unit file to path.
func WriteServiceFile(path string) error {
	return writeEmbedded(path, serviceFile)
}

// WriteDBusPolicy writes the embedded D-Bus policy file to path.
func WriteDBusPolicy(path string) error {
	return writeEmbedded(path, dbusPolicy)
}

// WritePolkitAction writes the embedded polkit action file to path.
func WritePolkitAction(path string) error {
	return writeEmbedded(path, polkitAction)
}

// WriteDom0Params writes the Xen dom0 boot parameters (dom0_mem,
// dom0_max_vcpus, and dom0_vcpus_pin when pinning is enabled) as
// space-separated key=value pairs. The xen-dom0-refresh script (os repo)
// reads this file and appends the parameters to the GRUB multiboot2 line,
// constraining dom0 resources at boot.
func WriteDom0Params(cfg *config.Config, path string) error {
	var params []string
	params = append(params, fmt.Sprintf("dom0_mem=%dM", cfg.Dom0.MemoryMB))
	params = append(params, fmt.Sprintf("dom0_max_vcpus=%d", cfg.Dom0.VCPUs))
	if cfg.Dom0.Pinned() {
		params = append(params, fmt.Sprintf("dom0_vcpus_pin=%d", cfg.Dom0.VCPUs))
	}
	content := strings.Join(params, " ") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// writeEmbedded writes content to path, creating parent directories as needed.
func writeEmbedded(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec,G301
		return err
	}
	return os.WriteFile(path, content, 0o644) //nolint:gosec,G306
}
