package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/provisioner"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// provisionDefaultDir is where dom0 looks for the layered provision
// content (base/ + per-vm/), next to the qlvm.toml config tree.
const provisionDefaultDir = "/etc/qvm/provision"

// provisionMode maps the CLI flags to a provisioner mode; the flags are
// mutually exclusive.
func provisionMode(packagesOnly, dotfilesOnly bool) (provisioner.Mode, error) {
	if packagesOnly && dotfilesOnly {
		return 0, errors.New("--packages-only and --dotfiles-only are mutually exclusive")
	}
	switch {
	case packagesOnly:
		return provisioner.ModePackages, nil
	case dotfilesOnly:
		return provisioner.ModeDotfiles, nil
	default:
		return provisioner.ModeAll, nil
	}
}

func provisionCmd() *cobra.Command {
	var packagesOnly, dotfilesOnly bool
	var dir string
	cmd := &cobra.Command{
		Use:   "provision <vm>",
		Short: "Provision a running VM: dnf packages and dotfiles over SSH",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, err := vm.LoadMeta(vmDirOf(name)); err != nil {
				return fmt.Errorf("provision %s: %w", name, err)
			}
			mode, err := provisionMode(packagesOnly, dotfilesOnly)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			ctx := cmd.Context()
			r := provisioner.NewSSHRunner(home)
			if err := provisioner.Provision(ctx, r, home, name, dir, mode); err != nil {
				return fmt.Errorf("provision %s: %w", name, err)
			}
			cmd.Printf("provisioned %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&packagesOnly, "packages-only", false, "install only the layered packages.txt")
	cmd.Flags().BoolVar(&dotfilesOnly, "dotfiles-only", false, "sync only the layered dotfiles/")
	cmd.Flags().StringVar(&dir, "dir", provisionDefaultDir, "provision dir with base/ and per-vm layers")
	return cmd
}

func init() {
	NewRootCmd().AddCommand(provisionCmd())
}
