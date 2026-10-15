package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// guiQlvmRoot is the per-VM/template state tree (spec §5.2). The GUI reads
// meta.yaml from here as the user (vms/<name>/meta.yaml is 0644). A var
// (not const) so tests can redirect it.
var guiQlvmRoot = "/var/lib/qvm"

// guiConfigPath is the qlvm config file path. A var (not const) so tests
// can redirect it.
var guiConfigPath = "/etc/qvm/qlvm.yaml"

// guiRunWaypipe execs waypipe — the only local process qlvm is allowed to
// launch (design spec). The GUI runs as the user in a Wayland session, so
// WAYLAND_DISPLAY is set naturally.
var guiRunWaypipe = func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("WAYLAND_DISPLAY is empty: the GUI must run in a Wayland session")
	}
	cmd := exec.Command("waypipe", args...) // #nosec G204 -- waypipe is the single permitted local exec (design spec)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// GuiRunApp projects a guest-side exec onto the dom0 Wayland session from
// the GUI (running as the user). It reads the VM's meta.yaml directly
// (user-readable) and calls projection.Run with the user's Wayland
// environment. The VM running check is skipped (the GUI has no Xen access);
// a down VM surfaces as waypipe/ssh's own failure, matching the CLI's
// unprivileged path.
func GuiRunApp(ctx context.Context, vmName, exec string, connect projection.Connect, out, errW io.Writer) error {
	m, err := vm.LoadMeta(filepath.Join(guiQlvmRoot, "vms", vmName))
	if err != nil {
		return fmt.Errorf("run %s: %w", vmName, err)
	}
	if connect != projection.ConnectSSH && m.Token == "" {
		return fmt.Errorf("run %s: meta has no control token for --connect %s — the VM predates the waypipe control channel; delete and recreate it (or re-bake the template)", vmName, connect)
	}
	dom0IP := ""
	if connect == projection.ConnectTCP {
		cfg, err := config.Load(guiConfigPath)
		if err != nil {
			return err
		}
		if cfg.Network.Dom0IP == "" {
			return fmt.Errorf("run %s: --connect tcp needs the dom0's uplink address — set network.dom0_ip in %s (the IP the VM dials for waypipe data; router_ip is the OVN gateway and does not forward to dom0 host ports)", vmName, guiConfigPath)
		}
		dom0IP = cfg.Network.Dom0IP
	}
	return projection.Run(ctx, projection.Deps{Waypipe: guiRunWaypipe},
		m, connect, dom0IP, exec,
		os.Stdin, out, errW)
}
