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
	"os"
	"path/filepath"
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

// writeEmbedded writes content to path, creating parent directories as needed.
func writeEmbedded(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
