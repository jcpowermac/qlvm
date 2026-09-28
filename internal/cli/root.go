// Package cli holds the qlvm cobra command tree.
// Later commands register via init() { NewRootCmd().AddCommand(...) }
// and never edit this file; NewRootCmd returns the shared root so those
// registrations reach the command executed by main.
package cli

import "github.com/spf13/cobra"

var rootCmd *cobra.Command

// NewRootCmd returns the shared qlvm root command.
func NewRootCmd() *cobra.Command {
	if rootCmd == nil {
		// No Run: bare `qlvm` prints help (cobra default).
		rootCmd = &cobra.Command{
			Use:   "qlvm",
			Short: "Qubes-like VM isolation on dom0",
		}
	}
	return rootCmd
}
