package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func syncKernelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync-kernel <vm>",
		Short: "Copy the VM's current kernel + initramfs into its template dir",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			if err := c.SyncKernel(args[0]); err != nil {
				return fmt.Errorf("sync-kernel %s: %w", args[0], err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "synced kernel to %s\n", args[0])
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restart to boot it: qlvm vm stop %s && qlvm vm start %s\n", args[0], args[0])
			return nil
		},
	}
}

func init() {
	vmCmd().AddCommand(syncKernelCmd())
}
