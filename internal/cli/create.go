package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/mounts"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

func createCmd() *cobra.Command {
	var domain, typ, tplRef string
	var memory, vcpus, workspaceSize int
	var mountFlags []string
	var coresFlag string
	var configPath string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Prepare a VM from a baked template (OVN port, reflinked disk, meta.yaml)",
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
			// Auto-detect agent type from template.
			if template.IsAgent(tpl) {
				if typ == "app" || typ == "disposable" {
					return fmt.Errorf("--type %s is incompatible with agent template %q (agent templates require --type agent)", typ, tplRef)
				}
				typ = "agent"
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
			cores, err := parseCoresFlag(coresFlag)
			if err != nil {
				return err
			}
			meta, err := vm.Create(cmd.Context(), vm.CreateDeps{
				OVN:         nb,
				Tpl:         tpl,
				FS:          ostree.NewFS(nil),
				Reflink:     mounts.Reflink,
				Root:        installRoot,
				FSType:      fstype,
				CoremapPath: coremap.DefaultPath,
				Dom0VCPUs:   cfg.Dom0.VCPUs,
			}, cfg, vm.Spec{
				Name:            name,
				Domain:          domain,
				Type:            typ,
				Image:           tpl.Image,
				MemoryMB:        memory,
				VCPUs:           vcpus,
				Mounts:          ms,
				WorkspaceSizeMB: workspaceSize,
				Cores:           cores,
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
	cmd.Flags().StringVar(&typ, "type", "app", "VM type: app, disposable, or agent (auto-detected from template)")
	cmd.Flags().StringVar(&tplRef, "template", "", "template dir to reference (exact dir name or unique prefix of `qlvm template list`'s TEMPLATE column; required)")
	cmd.Flags().IntVar(&memory, "memory", 0, "memory in MB (minimum 64; default: per-type config)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPUs (default: per-type config)")
	cmd.Flags().IntVar(&workspaceSize, "workspace-size", 0, "workspace disk size in MB for agent VMs (default 4096, 0 = ephemeral)")
	cmd.Flags().StringArrayVar(&mountFlags, "mount", nil, "p9 mount host:guest (repeatable)")
	cmd.Flags().StringVar(&coresFlag, "cores", "", "CPU cores to pin (e.g. \"6-7\" or \"6,7,8\"); empty = auto-assign (E-first)")
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath, "path to qlvm.yaml")
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
		// name != "": `--template ""` passes cobra's required check (the flag
		// is changed) but would prefix-match every dir; treat it as missing.
		if name != "" && (e.Name() == name || strings.HasPrefix(e.Name(), name)) {
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
// and returns its META. Ostree templates require META + template.raw; agent
// templates require META + vmlinuz + initramfs (no raw). This is create's
// only template touchpoint: no podman, no pull, no bake — a VM is a
// reference to a bake `qlvm template create` already made.
func loadRefTemplate(root, name string) (*template.Template, error) {
	dir, err := resolveTemplate(root, name)
	if err != nil {
		return nil, err
	}
	tpl, err := template.LoadMeta(dir)
	if err != nil {
		return nil, fmt.Errorf("template %s has no loadable META: %w; bake a complete one with qlvm template create <ref>", filepath.Base(dir), err)
	}
	// Agent templates carry vmlinuz/initramfs, not template.raw.
	if template.IsAgent(tpl) {
		for _, f := range []string{"vmlinuz", "initramfs"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				return nil, fmt.Errorf("template %s is incomplete (no %s); bake it with qlvm template create <ref>", filepath.Base(dir), f)
			}
		}
		return tpl, nil
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

// parseCoresFlag parses the --cores flag value into a slice of core IDs.
// Accepts comma-separated values and/or ranges (e.g. "6,7" or "6-8").
func parseCoresFlag(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var cores []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			// Range: start-end
			startStr, endStr, ok := strings.Cut(part, "-")
			if !ok {
				return nil, fmt.Errorf("--cores %q: invalid range %q", s, part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(startStr))
			if err != nil {
				return nil, fmt.Errorf("--cores %q: invalid start %q", s, startStr)
			}
			end, err := strconv.Atoi(strings.TrimSpace(endStr))
			if err != nil {
				return nil, fmt.Errorf("--cores %q: invalid end %q", s, endStr)
			}
			if start > end {
				return nil, fmt.Errorf("--cores %q: range start %d > end %d", s, start, end)
			}
			for c := start; c <= end; c++ {
				cores = append(cores, c)
			}
		} else {
			c, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("--cores %q: invalid core ID %q", s, part)
			}
			cores = append(cores, c)
		}
	}
	if len(cores) == 0 {
		return nil, fmt.Errorf("--cores %q: no cores specified", s)
	}
	return cores, nil
}

func init() {
	vmCmd().AddCommand(createCmd())
}
