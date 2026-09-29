package cli

import (
	"fmt"
	"os"
	"os/user"
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

const podmanSocket = "unix:///run/podman/podman.sock"

// fstype is the filesystem for both the template bake and the VM disk
// (ruling 4: the CLI passes it explicitly; one spelling so the two can't drift).
const fstype = "xfs"

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
			if domain == "" {
				domain = cfg.Domains[0].Name // first configured domain is the default
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
					keys, err := sshAuthKeys()
					if err != nil {
						return err
					}
					return ostree.BakeTemplate(cmd.Context(), ostree.NewFS(nil), dir, fstype, keys)
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
				FSType:  fstype,
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
	cmd.Flags().StringVar(&domain, "domain", "", "isolation domain (config section name; default: first in config)")
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

// sshAuthKeys collects the id_*.pub identities (the same names sshx offers:
// ed25519, ecdsa, rsa) of the calling user — root under `sudo qlvm create`
// — and of SUDO_USER, the desktop user who will run `qlvm run` without
// sudo. Baked into every VM's authorized_keys; empty result is a hard
// error: a VM without SSH identities is unrunnable.
func sshAuthKeys() (string, error) {
	var homes []string
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			homes = append(homes, u.HomeDir)
		}
	}
	seen := map[string]bool{}
	var keys []string
	for _, h := range homes {
		for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
			b, err := os.ReadFile(filepath.Join(h, ".ssh", n)) // #nosec G304 G703 -- homes from $HOME/passwd, fixed file names
			if err != nil {
				continue
			}
			k := strings.TrimSpace(string(b))
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("no ssh identity: run `ssh-keygen -t ed25519` as your desktop user before create (qlvm run sshes into VMs with it)")
	}
	return strings.Join(keys, "\n") + "\n", nil
}
