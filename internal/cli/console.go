package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// consoleCmd attaches to a VM's Xen console. EXCEPTION: this command
// requires root (it opens a libxl context and the Xen console TTY
// directly). All other CLI commands work without root via the daemon.
func consoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "console <name>",
		Short: "Attach to a VM's console (agent VMs; requires root)",
		Long: `Attach to a VM's primary console (hvc0). The console is available for
any existing domain, running or blocked. The local terminal is put in raw
mode: Ctrl+C and other control characters go to the VM, not the local
shell. Press Ctrl+] to detach and return to the shell.

This command requires root: it opens a libxl context and the Xen console
TTY directly. Run it with sudo.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dir := vmDirOf(name)
			m, err := vm.LoadMeta(dir)
			if err != nil {
				return fmt.Errorf("load %s: %w", name, err)
			}
			if m.Type != "agent" {
				return fmt.Errorf("%s is not an agent VM (type=%s); console is for agent VMs", name, m.Type)
			}
			x, err := xenctlNew()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()

			// Auto-start if not running. The agent VM's init waits for
			// console connection before starting pi, so we must start the
			// VM first — otherwise the user has to manually `qlvm vm start`
			// then `qlvm vm console` (racy: pi might start before the
			// console connects and get the wrong terminal size).
			running, err := x.Running(name)
			if err != nil {
				return fmt.Errorf("check %s: %w", name, err)
			}
			if !running {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "starting %s...\n", name); err != nil {
					return fmt.Errorf("write: %w", err)
				}
				if err := startVM(cmd.Context(), name); err != nil {
					return fmt.Errorf("start %s: %w", name, err)
				}
				// Wait for the domain to actually be running. CreateDomain
				// returns before the guest boots; connecting to the TTY too
				// early fails.
				deadline := time.Now().Add(30 * time.Second)
				for {
					running, err = x.Running(name)
					if err != nil {
						return fmt.Errorf("check %s: %w", name, err)
					}
					if running {
						break
					}
					if !time.Now().Add(500 * time.Millisecond).Before(deadline) {
						return fmt.Errorf("timed out waiting for %s to start", name)
					}
					time.Sleep(500 * time.Millisecond)
				}
			}

			return x.Console(name)
		},
	}
}

func init() {
	vmCmd().AddCommand(consoleCmd())
}
