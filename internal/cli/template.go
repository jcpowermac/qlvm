package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage baked templates (list, create, delete)",
	}
	cmd.AddCommand(templateListCmd(), templateCreateCmd(), templateDeleteCmd())
	return cmd
}

func templateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List baked templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			tpls, err := c.ListTemplates()
			if err != nil {
				return err
			}
			if len(tpls) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no templates")
				return nil
			}
			sort.Slice(tpls, func(i, j int) bool { return tpls[i].Dir < tpls[j].Dir })
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-40s %-8s %-6s %-10s %-8s\n",
				"TEMPLATE", "IMAGE", "KERNEL", "VMs", "SIZE", "COMPLETE")
			for _, t := range tpls {
				size := formatSize(t.SizeBytes)
				complete := "yes"
				if !t.Complete {
					complete = "no"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-40s %-8s %-6d %-10s %-8s\n",
					t.Dir, t.Image, t.Kernel, t.VMCount, size, complete)
			}
			return nil
		},
	}
}

func templateCreateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "create <ref>",
		Short: "Bake a template from a container image",
		Long: `Pulls the named container image, extracts its bootc rootfs into a
sparse template.raw, and records the image ref and digest in META. The
template dir is named from the image ref (e.g. fedora@sha256:...).

Use --force to overwrite an existing template dir with the same name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			// Wire progress to print to stderr during the bake.
			cancel := c.SubscribeProgress(func(id, message string) {
				if message == "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "bake %s: done\n", id)
				} else {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "bake %s: %s\n", id, message)
				}
			})
			defer cancel()

			dir, err := c.CreateTemplate(args[0], force)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "baked template %s\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing template dir with the same name")
	return cmd
}

func templateDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <dir>",
		Short: "Delete a template directory",
		Long: `Deletes the named template directory. VMs that reference the template
are not affected: they keep their own reflinked root disk. The template
dir itself is removed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := getClient()
			if err != nil {
				return err
			}
			if err := c.DeleteTemplate(args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "deleted template %s\n", args[0])
			return nil
		},
	}
}

// formatSize renders a byte count as a human-readable string (e.g. "1.2G").
func formatSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	exp := 0
	for value >= float64(unit) {
		value /= float64(unit)
		exp++
		if exp >= len("KMGTPE") {
			break
		}
	}
	// The suffix index is exp-1 because exp counts the number of divisions,
	// and the first division (exp=1) corresponds to "K" (index 0).
	if exp > 0 {
		return fmt.Sprintf("%.1f%c", value, "KMGTPE"[exp-1])
	}
	return fmt.Sprintf("%.1f", value)
}

func init() {
	NewRootCmd().AddCommand(templateCmd())
}
