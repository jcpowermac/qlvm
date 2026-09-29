package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// runWaypipe execs waypipe — the only local process qlvm is allowed to
// launch (design spec): `waypipe ssh <vm> <app>...` carries the app's GUI
// over the caller's Wayland session, so dom0's WAYLAND_DISPLAY must be set.
var runWaypipe = func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("WAYLAND_DISPLAY is empty: qlvm vm run is a session command — run it as your desktop user from the Wayland login, not via sudo (sudo drops the session environment)")
	}
	cmd := exec.Command("waypipe", args...) // #nosec G204 -- waypipe is the single permitted local exec (design spec)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <vm> [app...]",
		Short: "Run an app in a VM's GUI via waypipe ssh (dom0 Wayland session required)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return fmt.Errorf("run %s: %w", name, err)
			}
			// Connect by the meta IP, not the name: disposable VMs have no
			// ssh-config entry (spec §4) and every VM generation reuses the IP
			// with fresh host keys — known-hosts bypass mirrors sshx's policy.
			wpArgs := waypipeSSHArgs(m.IP, args[1:])
			if err := runWaypipe(wpArgs, os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("waypipe: %w", err)
			}
			return nil
		},
	}
}

// waypipeSSHArgs assembles the ssh half of the waypipe invocation: run
// connects user@IP directly (the VM name only resolves for app VMs through
// ~/.ssh/config, which disposables deliberately lack).
func waypipeSSHArgs(ip string, app []string) []string {
	return append([]string{"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		vm.SSHUser + "@" + ip}, app...)
}

func init() {
	vmCmd().AddCommand(runCmd())
}
