package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/vm"
)

func consoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "console <name>",
		Short: "Attach to a VM's console (agent VMs)",
		Long: `Attach to a VM's primary console (hvc0). The console is available for
any existing domain, running or blocked. The local terminal is put in raw
mode: Ctrl+C and other control characters go to the VM, not the local
shell. Press Ctrl+] to detach and return to the shell.`,
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
			return x.Console(name)
		},
	}
}

func init() {
	vmCmd().AddCommand(consoleCmd())
}
