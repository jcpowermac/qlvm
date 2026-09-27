package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/provisioner"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// provisionDefaultDir is where dom0 looks for the layered provision
// content (base/ + per-vm/), next to the qlvm.toml config tree.
const provisionDefaultDir = "/etc/qvm/provision"

func provisionCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "provision <vm>",
		Short: "Provision a running VM: sync the layered dotfiles/ into its home over sftp",
		Long: "Syncs provision/<base|vm>/dotfiles/ into the VM user's home over sftp.\n" +
			"System packages are not provisioned: the VM root is an ostree\n" +
			"deployment built from the bootc container image, and dnf is\n" +
			"disabled on it. Extra system packages belong in the container\n" +
			"image itself (extend the image, don't provision).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, err := vm.LoadMeta(vmDirOf(name)); err != nil {
				return fmt.Errorf("provision %s: %w", name, err)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			ctx := cmd.Context()
			r := provisioner.NewSSHRunner(home)
			if err := provisioner.Provision(ctx, r, home, name, dir); err != nil {
				return fmt.Errorf("provision %s: %w", name, err)
			}
			cmd.Printf("provisioned %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", provisionDefaultDir, "provision dir with base/ and per-vm layers")
	return cmd
}

func init() {
	NewRootCmd().AddCommand(provisionCmd())
}
