package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func provisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "provision <vm>",
		Short: "Provision a running VM: sync the layered dotfiles/ into its home over sftp",
		Long: "Syncs provision/<base|vm>/dotfiles/ into the VM user's home over sftp.\n" +
			"System packages are not provisioned: the VM root is an ostree\n" +
			"deployment built from the bootc container image, and dnf is\n" +
			"disabled on it. Extra system packages belong in the container\n" +
			"image itself (extend the image, don't provision).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			if err := c.ProvisionVM(args[0]); err != nil {
				return fmt.Errorf("provision %s: %w", args[0], err)
			}
			cmd.Printf("provisioned %s\n", args[0])
			return nil
		},
	}
}

func init() {
	vmCmd().AddCommand(provisionCmd())
}
