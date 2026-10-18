package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/apps"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// appsCacheDir is the dom0 desktop cache: one subdir per VM with the
// cached .desktop files (design §9). A var (not const) so tests can
// redirect it.
var appsCacheDir = "/var/lib/qlvm/desktop-cache"


// appsCmd is the rofi mode — bare `apps` driven by the ROFI_RETV/ROFI_INFO
// env like the former bash appmenu helper — and the parent of `apps sync`.
func appsCmd() *cobra.Command {
	var connect string
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Desktop app launcher: serve a rofi menu of the cached VM desktops (run rofi with -field 4, so ROFI_INFO carries the selected <vm>|<exec>)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := projection.ParseConnect(connect)
			if err != nil {
				return err
			}
			switch os.Getenv("ROFI_RETV") {
			case "0":
				out, err := apps.EmitRofi(appsCacheDir)
				if err != nil {
					return err
				}
				_, _ = io.WriteString(cmd.OutOrStdout(), out)
			case "1":
				if err := launchApp(cmd, c); err != nil {
					return err
				}
			default:
				return errors.New("apps: ROFI_RETV not set (rofi mode: 0=menu, 1=launch); use 'qlvm apps sync' to refresh the desktop cache")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&connect, "connect", string(projection.ConnectSSH),
		"waypipe channel for launched apps: ssh (default), tcp, vsock (see 'qlvm vm run')")
	cmd.AddCommand(appsSyncCmd())
	cmd.AddCommand(appsWhitelistCmd())
	return cmd
}

// appsWhitelistCmd manages the per-VM app whitelist (Qubes appmenus-style):
// <cacheDir>/<vm>/whitelisted-appmenus.list, one .desktop basename per
// line. Absent file = all apps visible; an empty list deletes the file.
func appsWhitelistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whitelist",
		Short: "Per-VM app whitelist (no file = all apps; first edit pins the full set)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list <vm>",
			Short: "Show the VM's visible apps and whitelist state",
			Args:  cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				vm := args[0]
				if err := checkVMExists(vm); err != nil {
					return err
				}
				visible, err := apps.VisibleApps(appsCacheDir, vm)
				if err != nil {
					return err
				}
				all, err := apps.CachedApps(appsCacheDir, vm)
				if err != nil {
					return err
				}
				wl, err := apps.ReadWhitelist(appsCacheDir, vm)
				if err != nil {
					return err
				}
				if wl != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "# whitelist active (%d/%d apps)\n", len(visible), len(all))
				}
				for _, a := range visible {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", a.Base, a.Name)
				}
				return nil
			},
		},
		whitelistEditCmd("add <vm> <app...>", "Add apps to the VM's whitelist (materializes the full set first when absent)"),
		whitelistEditCmd("remove <vm> <app...>", "Remove apps from the VM's whitelist (removing the last one = all apps visible)"),
		&cobra.Command{
			Use:   "clear <vm>",
			Short: "Delete the VM's whitelist (all apps visible again)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				vm := args[0]
				if err := checkVMExists(vm); err != nil {
					return err
				}
				if err := apps.WriteWhitelist(appsCacheDir, vm, nil); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "whitelist %s: cleared (all apps visible)\n", vm)
				return nil
			},
		},
	)
	return cmd
}

// whitelistEditCmd builds the shared add/remove implementation. Each app
// argument is matched case-insensitively against the cached apps' .desktop
// basenames and Names; a unique prefix (e.g. "firef") is accepted.
func whitelistEditCmd(use, short string) *cobra.Command {
	isAdd := strings.HasPrefix(use, "add")
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, appArgs := args[0], args[1:]
			if err := checkVMExists(vm); err != nil {
				return err
			}
			all, err := apps.CachedApps(appsCacheDir, vm)
			if err != nil {
				return err
			}
			wl, err := apps.ReadWhitelist(appsCacheDir, vm)
			if err != nil {
				return err
			}
			if wl == nil {
				wl = basenames(all) // absent = all: materialize the full set first
			}
			matched, err := matchApps(all, appArgs)
			if err != nil {
				return err
			}
			cur := map[string]bool{}
			for _, n := range wl {
				cur[n] = true
			}
			for _, a := range matched {
				if isAdd {
					cur[a.Base] = true
				} else {
					delete(cur, a.Base)
				}
			}
			var next []string
			for n := range cur {
				next = append(next, n)
			}
			if err := apps.WriteWhitelist(appsCacheDir, vm, next); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "whitelist %s: %s %d app(s), %d now visible\n", vm, useSpace(use), len(appArgs), len(cur))
			return nil
		},
	}
}

func useSpace(use string) string { return strings.TrimSpace(strings.SplitN(use, " ", 2)[0]) }

func basenames(all []apps.AppEntry) []string {
	out := make([]string, 0, len(all))
	for _, a := range all {
		out = append(out, a.Base)
	}
	return out
}

// checkVMExists rejects typos before writing cache state for a VM that
// doesn't exist.
func checkVMExists(name string) error {
	if _, err := vm.LoadMeta(vmDirOf(name)); err != nil {
		return fmt.Errorf("no such VM %s: %w", name, err)
	}
	return nil
}

// matchApps resolves each argument to a cached app entry: case-insensitive
// exact match on the .desktop basename or the app Name first, then a
// unique prefix over both. 0 or >1 candidates is an error.
func matchApps(all []apps.AppEntry, args []string) ([]apps.AppEntry, error) {
	var out []apps.AppEntry
	for _, arg := range args {
		q := strings.ToLower(arg)
		var exact, prefix []apps.AppEntry
		for _, a := range all {
			base, name := strings.ToLower(a.Base), strings.ToLower(a.Name)
			switch {
			case base == q || name == q:
				exact = append(exact, a)
			case strings.HasPrefix(base, q) || strings.HasPrefix(name, q):
				prefix = append(prefix, a)
			}
		}
		switch {
		case len(exact) == 1:
			out = append(out, exact[0])
		case len(exact) > 1:
			return nil, fmt.Errorf("app %q is ambiguous (%s): match it by its full name", arg, joinBases(exact))
		case len(prefix) == 1:
			out = append(out, prefix[0])
		case len(prefix) > 1:
			return nil, fmt.Errorf("app prefix %q is ambiguous (%s): match it by its full name", arg, joinBases(prefix))
		default:
			return nil, fmt.Errorf("no app matching %q in the cache (%d apps; run 'qlvm apps sync' to refresh)", arg, len(all))
		}
	}
	return out, nil
}

func joinBases(es []apps.AppEntry) string {
	bases := make([]string, len(es))
	for i, a := range es {
		bases[i] = a.Base
	}
	sort.Strings(bases)
	return strings.Join(bases, ", ")
}

// launchApp runs the ROFI_INFO selection "<vm>|<exec>", starting the VM
// first when it is stopped, waiting for the guest's control socket, then
// projecting the app (no SSH in the app path; the control channel is
// token-authenticated).
func launchApp(cmd *cobra.Command, c projection.Connect) error {
	ctx := cmd.Context()
	cl, err := getClient()
	if err != nil {
		return err
	}
	return apps.Launch(os.Getenv("ROFI_INFO"), appsCacheDir,
		func(name string) bool {
			vms, err := cl.ListVMs()
			if err != nil {
				return false
			}
			for _, v := range vms {
				if v.Name == name {
					return v.State == "running"
				}
			}
			return false
		},
		func(name string) error { return cl.StartVM(name) },
		func(name string) error {
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return err
			}
			if c != projection.ConnectSSH && m.Token == "" {
				return fmt.Errorf("vm %s predates the waypipe control channel: delete and recreate it (or re-bake the template)", name)
			}
			return projection.WaitControl(ctx, m.IP, c, time.Second, 60*time.Second)
		},
		func(name, exec string) error {
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return err
			}
			dom0IP := ""
			if c == projection.ConnectTCP {
				cfg, err := config.Load(defaultConfigPath)
				if err != nil {
					return err
				}
				dom0IP = cfg.Network.RouterIP
			}
			return projection.Run(ctx, projection.Deps{Waypipe: runWaypipe}, m, c, dom0IP, exec,
				os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	)
}

// appsSyncCmd refreshes the desktop cache: `apps sync [vm]` — the named
// VM, or all running VMs when no argument is given.
func appsSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [vm]",
		Short: "Fetch the VM's /usr/share/applications into the desktop cache",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			targets, err := appsSyncTargets(args)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			for _, name := range targets {
				entries, err := fetchDesktops(ctx, home, name)
				if err != nil {
					return fmt.Errorf("sync %s: %w", name, err)
				}
				n, err := apps.SyncCache(appsCacheDir, name, entries)
				if err != nil {
					return fmt.Errorf("sync %s: %w", name, err)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cached %d desktop entries from %s\n", n, name)
			}
			return nil
		},
	}
}

// appsSyncTargets resolves the sync targets: the explicit vm argument, or
// (no argument) all running qlvm VMs, sorted. The daemon's ListVMs only
// returns qlvm-managed VMs, so no local meta check is needed.
func appsSyncTargets(args []string) ([]string, error) {
	if len(args) > 0 {
		if _, err := vm.LoadMeta(vmDirOf(args[0])); err != nil {
			return nil, fmt.Errorf("no such VM %s: %w", args[0], err)
		}
		return []string{args[0]}, nil
	}
	cl, err := getClient()
	if err != nil {
		return nil, err
	}
	vms, err := cl.ListVMs()
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, v := range vms {
		if v.State == "running" {
			targets = append(targets, v.Name)
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("no running VMs to sync: start one or pass a VM name")
	}
	sort.Strings(targets)
	return targets, nil
}

// fetchDesktops pulls one VM's concatenated desktop stream over SSH and
// splits it into per-app entries.
func fetchDesktops(ctx context.Context, home, name string) (map[string]string, error) {
	m, err := vm.LoadMeta(vmDirOf(name))
	if err != nil {
		return nil, err
	}
	c, err := sshx.Connect(ctx, home, m.IP, vm.SSHUser)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	rc, err := sshx.FetchFile(ctx, c, apps.DesktopGlob)
	if err != nil {
		return nil, err
	}
	data, rerr := io.ReadAll(rc)
	cerr := rc.Close()
	if rerr != nil {
		return nil, rerr
	}
	if cerr != nil {
		return nil, cerr
	}
	return apps.SplitDesktops(data)
}

func init() {
	NewRootCmd().AddCommand(appsCmd())
}
