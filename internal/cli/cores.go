package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/coremap"
)

func coresCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cores",
		Short: "Show CPU core topology and VM assignments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cm, err := coremap.Load(coremap.DefaultPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no coremap at %s (run qlvm vm create first, or qlvm install)", coremap.DefaultPath)
				}
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Topology: %d cores (%d P, %d E)\n",
				cm.Topology.TotalCores, len(cm.Topology.PCores), len(cm.Topology.ECores))
			_, _ = fmt.Fprintf(out, "  P cores: %s\n", coresList(cm.Topology.PCores))
			_, _ = fmt.Fprintf(out, "  E cores: %s\n", coresList(cm.Topology.ECores))
			_, _ = fmt.Fprintf(out, "dom0: cores %s\n", coresList(cm.Dom0Cores))
			if len(cm.Assignments) == 0 {
				_, _ = fmt.Fprintf(out, "No VMs assigned.\n")
				return nil
			}
			_, _ = fmt.Fprintf(out, "VMs:\n")
			// Sort assignments by VM name for stable output.
			sorted := make([]coremap.Assignment, len(cm.Assignments))
			copy(sorted, cm.Assignments)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].VM < sorted[j].VM })
			for _, a := range sorted {
				_, _ = fmt.Fprintf(out, "  %-20s cores %s\n", a.VM, coresList(a.Cores))
			}
			// Show free cores.
			free := cm.FreeCores()
			var freeList []int
			for c := range free {
				freeList = append(freeList, c)
			}
			sort.Ints(freeList)
			_, _ = fmt.Fprintf(out, "Free: %s\n", coresList(freeList))
			return nil
		},
	}
}

// coresList formats a list of core IDs, grouping contiguous ranges.
func coresList(cores []int) string {
	if len(cores) == 0 {
		return "(none)"
	}
	sort.Ints(cores)
	var parts []string
	start := cores[0]
	end := cores[0]
	for i := 1; i < len(cores); i++ {
		if cores[i] == end+1 {
			end = cores[i]
		} else {
			parts = append(parts, rangeStr(start, end))
			start = cores[i]
			end = cores[i]
		}
	}
	parts = append(parts, rangeStr(start, end))
	return joinParts(parts)
}

func rangeStr(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func joinParts(parts []string) string {
	if len(parts) == 0 {
		return "(none)"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + ", " + joinParts(parts[1:])
}

func init() {
	NewRootCmd().AddCommand(coresCmd())
}
