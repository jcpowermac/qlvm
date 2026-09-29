package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/mounts"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

func createCmd() *cobra.Command {
	var domain, typ, tplRef string
	var memory, vcpus int
	var mountFlags []string
	var configPath string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Prepare a VM from a baked template (OVN port, reflinked disk, meta.toml)",
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
			if domain == "" {
				domain = cfg.Domains[0].Name // first configured domain is the default
			}
			tpl, err := loadRefTemplate(installRoot, tplRef)
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
				FSType:  fstype,
			}, cfg, vm.Spec{
				Name:     name,
				Domain:   domain,
				Type:     typ,
				Image:    tpl.Image,
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
	cmd.Flags().StringVar(&domain, "domain", "", "isolation domain (config section name; default: first in config)")
	cmd.Flags().StringVar(&typ, "type", "app", "VM type: app or disposable")
	cmd.Flags().StringVar(&tplRef, "template", "", "template dir to reference (exact dir name or unique prefix of `qlvm template list`'s TEMPLATE column; required)")
	cmd.Flags().IntVar(&memory, "memory", 0, "memory in MB (default: per-type config)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPUs (default: per-type config)")
	cmd.Flags().StringArrayVar(&mountFlags, "mount", nil, "p9 mount host:guest (repeatable)")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath, "path to qlvm.toml")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}

// resolveTemplate maps a --template reference — an exact template dir name,
// or a unique prefix of one — to the dir under <root>/templates. Pure
// filesystem lookup: zero or multiple candidates is a hard error listing
// what exists.
func resolveTemplate(root, name string) (string, error) {
	tplRoot := filepath.Join(root, "templates")
	entries, err := os.ReadDir(tplRoot)
	var have []string
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no templates under %s; bake one first with qlvm template create <ref>", tplRoot)
	}
	if err != nil {
		return "", err
	}
	var match []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		have = append(have, e.Name())
		if e.Name() == name || strings.HasPrefix(e.Name(), name) {
			match = append(match, e.Name())
		}
	}
	switch len(match) {
	case 0:
		return "", fmt.Errorf("no template dir matching %q; existing: %s", name, strings.Join(have, ", "))
	case 1:
		return filepath.Join(tplRoot, match[0]), nil
	default:
		return "", fmt.Errorf("template ref %q is ambiguous (matches: %s)", name, strings.Join(match, ", "))
	}
}

// loadRefTemplate resolves --template under root to a COMPLETE template dir
// (META + template.raw) and returns its META. This is create's only
// template touchpoint: no podman, no pull, no bake — a VM is a reference to
// a bake `qlvm template create` already made.
func loadRefTemplate(root, name string) (*template.Template, error) {
	dir, err := resolveTemplate(root, name)
	if err != nil {
		return nil, err
	}
	tpl, err := template.LoadMeta(dir)
	if err != nil {
		return nil, fmt.Errorf("template %s has no loadable META: %w; bake a complete one with qlvm template create <ref>", filepath.Base(dir), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "template.raw")); err != nil {
		return nil, fmt.Errorf("template %s is incomplete (no template.raw); bake it with qlvm template create <ref>", filepath.Base(dir))
	}
	return tpl, nil
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
	vmCmd().AddCommand(createCmd())
}
