// Package cli holds the qlvm cobra command tree.
// Later commands register via init() { NewRootCmd().AddCommand(...) }
// and never edit this file; NewRootCmd returns the shared root so those
// registrations reach the command executed by main.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rootCmd *cobra.Command

// NewRootCmd returns the shared qlvm root command.
func NewRootCmd() *cobra.Command {
	if rootCmd == nil {
		rootCmd = &cobra.Command{
			Use:   "qlvm",
			Short: "Qubes-like VM isolation on dom0",
			Run: func(cmd *cobra.Command, _ []string) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no subcommand yet")
			},
		}
	}
	return rootCmd
}
