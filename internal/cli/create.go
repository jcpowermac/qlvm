package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func createCmd() *cobra.Command {
	var domain, typ, tplRef string
	var memory, vcpus int
	var mountFlags []string
	var coresFlag string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Prepare a VM from a baked template (OVN port, reflinked disk, meta.yaml)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			opts := &agent.CreateVMOptions{
				Name:     args[0],
				Domain:   domain,
				Template: tplRef,
				Type:     typ,
				MemoryMB: uint64(memory), //nolint:gosec,G115
				VCPUs:    uint8(vcpus),    //nolint:gosec,G115
				Mounts:   mountFlags,
				Cores:    coresFlag,
			}
			name, err := c.CreateVM(opts)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "isolation domain (config section name; default: first in config)")
	cmd.Flags().StringVar(&typ, "type", "app", "VM type: app, disposable, or agent (auto-detected from template)")
	cmd.Flags().StringVar(&tplRef, "template", "", "template dir to reference (exact dir name or unique prefix of `qlvm template list`'s TEMPLATE column; required)")
	cmd.Flags().IntVar(&memory, "memory", 0, "memory in MB (minimum 64; default: per-type config)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPUs (default: per-type config)")
	cmd.Flags().StringArrayVar(&mountFlags, "mount", nil, "p9 mount host:guest (repeatable)")
	cmd.Flags().StringVar(&coresFlag, "cores", "", "CPU cores to pin (e.g. \"6-7\" or \"6,7,8\"); empty = auto-assign (E-first)")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}

// parseCoresFlag parses the --cores flag value into a string that is
// passed directly to the daemon. The daemon handles the parsing and
// validation. Kept for backward compatibility with tests.
func parseCoresFlag(s string) string {
	return s
}

func init() {
	vmCmd().AddCommand(createCmd())
}
