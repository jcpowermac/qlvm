package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func coresCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cores",
		Short: "Show CPU core topology and VM assignments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			cm, err := c.GetCoremap()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Topology: %d cores (%d P, %d E)\n",
				cm.TotalCores, len(cm.PCores), len(cm.ECores))
			_, _ = fmt.Fprintf(out, "  P cores: %s\n", coresList(cm.PCores))
			_, _ = fmt.Fprintf(out, "  E cores: %s\n", coresList(cm.ECores))
			_, _ = fmt.Fprintf(out, "dom0: cores %s\n", coresList(cm.Dom0Cores))
			if len(cm.Assignments) == 0 {
				_, _ = fmt.Fprintf(out, "No VMs assigned.\n")
				return nil
			}
			_, _ = fmt.Fprintf(out, "VMs:\n")
			// Sort assignments by VM name for stable output.
			sorted := make([]agent.CoremapAssignmentInfo, len(cm.Assignments))
			copy(sorted, cm.Assignments)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].VM < sorted[j].VM })
			for _, a := range sorted {
				_, _ = fmt.Fprintf(out, "  %-20s cores %s\n", a.VM, coresList(a.Cores))
			}
			// Show free cores: all cores not assigned to dom0 or any VM.
			assigned := make(map[int]bool)
			for _, c := range cm.Dom0Cores {
				assigned[c] = true
			}
			for _, a := range cm.Assignments {
				for _, c := range a.Cores {
					assigned[c] = true
				}
			}
			var freeList []int
			for i := 0; i < cm.TotalCores; i++ {
				if !assigned[i] {
					freeList = append(freeList, i)
				}
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
