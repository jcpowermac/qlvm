package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/mounts"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

const podmanSocket = "unix:///run/podman/podman.sock"

func createCmd() *cobra.Command {
	var domain, typ, image string
	var memory, vcpus int
	var mountFlags []string
	var configPath string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Prepare a VM (template, OVN port, reflinked disk, meta.toml)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			pod, err := template.NewPodman(cmd.Context(), podmanSocket)
			if err != nil {
				return err
			}
			tpl, err := template.Ensure(cmd.Context(), pod, template.EnsureOpts{
				Root: installRoot,
				Ref:  image,
				Log:  cmd.OutOrStdout(),
				Bake: func(dir string) error {
					return ostree.BakeTemplate(cmd.Context(), ostree.NewFS(nil), dir, "xfs")
				},
			})
			if err != nil {
				return err
			}
			nb, err := ovn.NewLive(cmd.Context())
			if err != nil {
				return err
			}
			var ms []vm.Mount
			for _, f := range mountFlags {
				host, guest, ok := strings.Cut(f, ":")
				if !ok || host == "" || guest == "" {
					return fmt.Errorf("--mount %q: want host:guest", f)
				}
				ms = append(ms, vm.Mount{Host: host, Guest: guest})
			}
			meta, err := vm.Create(cmd.Context(), vm.CreateDeps{
				OVN:     nb,
				Tpl:     tpl,
				FS:      ostree.NewFS(nil),
				Reflink: mounts.Reflink,
				Root:    installRoot,
				FSType:  "xfs",
			}, cfg, vm.Spec{
				Name:     name,
				Domain:   domain,
				Type:     typ,
				Image:    image,
				MemoryMB: memory,
				VCPUs:    vcpus,
				Mounts:   ms,
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created %s: %s %s (%dMB, %d vcpu%s)\n",
				meta.Name, meta.IP, meta.MAC, meta.MemoryMB, meta.VCPUs,
				mountSuffix(meta.Mounts))
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "isolation domain (config section name)")
	cmd.Flags().StringVar(&typ, "type", "app", "VM type: app or disposable")
	cmd.Flags().StringVar(&image, "image", "", "bootc image reference (required)")
	cmd.Flags().IntVar(&memory, "memory", 0, "memory in MB (default: per-type config)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPUs (default: per-type config)")
	cmd.Flags().StringArrayVar(&mountFlags, "mount", nil, "p9 mount host:guest (repeatable)")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath, "path to qlvm.toml")
	_ = cmd.MarkFlagRequired("image")
	return cmd
}

func mountSuffix(ms []vm.Mount) string {
	if len(ms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, m.Guest+"->"+m.Host)
	}
	return ", mounts " + strings.Join(parts, ", ")
}

func init() {
	NewRootCmd().AddCommand(createCmd())
}
