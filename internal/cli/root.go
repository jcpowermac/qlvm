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

var vmRoot *cobra.Command

// vmCmd returns the shared `vm` parent: every VM-facing command (create,
// start, stop, kill, delete, list, run, provision, sync-kernel) registers
// under it from its own init(); apps and install stay on the root.
func vmCmd() *cobra.Command {
	if vmRoot == nil {
		vmRoot = &cobra.Command{
			Use:   "vm",
			Short: "Manage VMs (create, start, stop, kill, delete, list, run, provision, sync-kernel)",
		}
		NewRootCmd().AddCommand(vmRoot)
	}
	return vmRoot
}
