package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/apps"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// appsCacheDir is the dom0 desktop cache: one subdir per VM with the
// cached .desktop files (design §9). A var (not const) so tests can
// redirect it.
var appsCacheDir = "/var/lib/qvm/desktop-cache"

// desktopGlob is the remote stream apps sync fetches: the VM shell
// expands the glob and cat concatenates the files (sshx.FetchFile).
const desktopGlob = "/usr/share/applications/*.desktop"

// appsCmd is the rofi mode — bare `apps` driven by the ROFI_RETV/ROFI_INFO
// env like the former bash appmenu helper — and the parent of `apps sync`.
func appsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Desktop app launcher: serve a rofi menu of the cached VM desktops (run rofi with -field 4, so ROFI_INFO carries the selected <vm>|<exec>)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch os.Getenv("ROFI_RETV") {
			case "0":
				out, err := apps.EmitRofi(appsCacheDir)
				if err != nil {
					return err
				}
				_, _ = io.WriteString(cmd.OutOrStdout(), out)
			case "1":
				if err := launchApp(cmd); err != nil {
					return err
				}
			default:
				return errors.New("apps: ROFI_RETV not set (rofi mode: 0=menu, 1=launch); use 'qlvm apps sync' to refresh the desktop cache")
			}
			return nil
		},
	}
	cmd.AddCommand(appsSyncCmd())
	return cmd
}

// launchApp runs the ROFI_INFO selection "<vm>|<exec>", starting the VM
// first when it is stopped, waiting for the guest's control socket, then
// projecting the app (no SSH in the app path; the control channel is
// token-authenticated).
func launchApp(cmd *cobra.Command) error {
	ctx := cmd.Context()
	x, err := xenctl.New()
	if err != nil {
		return err
	}
	defer func() { _ = x.Close() }()
	return apps.Launch(os.Getenv("ROFI_INFO"), appsCacheDir,
		func(name string) bool {
			running, _ := x.Running(name)
			return running
		},
		func(name string) error { return startVM(ctx, name) },
		func(name string) error {
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return err
			}
			if m.Token == "" {
				return fmt.Errorf("vm %s predates the waypipe control channel: delete and recreate it (or re-bake the template)", name)
			}
			return projection.WaitControl(ctx, m.IP, time.Second, 60*time.Second)
		},
		func(name, exec string) error {
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return err
			}
			cfg, err := config.Load(defaultConfigPath)
			if err != nil {
				return err
			}
			return projection.Run(ctx, projection.Deps{Waypipe: runWaypipe}, m, cfg.Network.RouterIP, exec,
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
				n, err := apps.WriteCache(appsCacheDir, name, entries)
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
// (no argument) all running Xen domains that have local qlvm meta, sorted.
func appsSyncTargets(args []string) ([]string, error) {
	if len(args) > 0 {
		if _, err := vm.LoadMeta(vmDirOf(args[0])); err != nil {
			return nil, fmt.Errorf("no such VM %s: %w", args[0], err)
		}
		return []string{args[0]}, nil
	}
	x, err := xenctl.New()
	if err != nil {
		return nil, err
	}
	defer func() { _ = x.Close() }()
	infos, err := x.List()
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, d := range infos {
		if _, err := vm.LoadMeta(vmDirOf(d.Name)); err != nil {
			continue // running domain without qlvm meta: not a qlvm VM
		}
		targets = append(targets, d.Name)
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
	rc, err := sshx.FetchFile(ctx, c, desktopGlob)
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
