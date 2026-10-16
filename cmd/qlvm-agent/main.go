// Package main is the qlvm-agent D-Bus daemon entry point.
// It runs as root under systemd, exposes io.qlvm.Agent on the system bus,
// and delegates to qlvm's internal packages.
//
// Subcommands:
//   - (no subcommand) run the daemon (default)
//   - install-system-files extract embedded service files to system paths
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// configPath is where the daemon reads its configuration.
const configPath = "/etc/qlvm/qlvm.yaml"

// stateRoot is the qlvm state root where VM and template state lives.
const stateRoot = "/var/lib/qlvm"

// System paths for embedded service files.
const (
	serviceFilePath = "/etc/systemd/system/qlvm-agent.service"
	dbusPolicyPath  = "/etc/dbus-1/system.d/io.qlvm.agent.conf"
	polkitActionPath = "/usr/share/polkit-1/actions/io.qlvm.agent.policy"
)

// commandRunner runs an external command. Used to allow mocking in tests.
type commandRunner func(name string, args ...string) error

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install-system-files":
			if err := installSystemFiles(serviceFilePath, dbusPolicyPath, polkitActionPath, runExternalCommand); err != nil {
				log.Fatalf("install-system-files: %v", err)
			}
			return
		default:
			log.Fatalf("unknown subcommand: %s", os.Args[1]) //nolint:gosec,G706
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		cancel()
	}()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	x, err := xenctl.New()
	if err != nil {
		log.Fatalf("xen: %v", err)
	}
	defer func() { _ = x.Close() }()

	nb, err := ovn.NewLive(ctx)
	if err != nil {
		log.Fatalf("ovn: %v", err)
	}
	defer nb.Close()
	br, err := ovs.NewLive(ctx)
	if err != nil {
		log.Fatalf("ovs: %v", err)
	}
	defer br.Close()

	a := agent.New(x, nb, br, cfg, coremap.DefaultPath, stateRoot)
	_, err = a.Run(ctx)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
}

// runExternalCommand runs a command and returns its error, capturing
// combined output for error messages.
func runExternalCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec,G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, joinStrings(args), err, string(out))
	}
	return nil
}

// installSystemFiles checks for root privileges and extracts the embedded
// service files to the given paths, then runs systemctl to reload and enable
// the service. It does not start the service.
func installSystemFiles(servicePath, dbusPath, polkitPath string, runCmd commandRunner) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install-system-files requires root (run with sudo)")
	}
	return doInstallSystemFiles(servicePath, dbusPath, polkitPath, runCmd)
}

// doInstallSystemFiles extracts the embedded service files to the given paths
// and runs systemctl to reload and enable the service. It does not start the
// service. It does not check for root privileges (the caller must do that).
func doInstallSystemFiles(servicePath, dbusPath, polkitPath string, runCmd commandRunner) error {
	if err := agent.WriteServiceFile(servicePath); err != nil {
		return fmt.Errorf("writing service file to %s: %w", servicePath, err)
	}
	log.Printf("wrote %s", servicePath)

	if err := agent.WriteDBusPolicy(dbusPath); err != nil {
		return fmt.Errorf("writing D-Bus policy to %s: %w", dbusPath, err)
	}
	log.Printf("wrote %s", dbusPath)

	if err := agent.WritePolkitAction(polkitPath); err != nil {
		return fmt.Errorf("writing polkit action to %s: %w", polkitPath, err)
	}
	log.Printf("wrote %s", polkitPath)

	if err := runCmd("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	log.Println("systemctl daemon-reload done")

	if err := runCmd("systemctl", "enable", "qlvm-agent"); err != nil {
		return fmt.Errorf("systemctl enable qlvm-agent: %w", err)
	}
	log.Println("qlvm-agent enabled")

	log.Println("installation complete; start the service with: systemctl start qlvm-agent")
	return nil
}

// joinStrings joins args with spaces.
func joinStrings(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}
