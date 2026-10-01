package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// runWaypipe execs waypipe — the only local process qlvm is allowed to
// launch (design spec): `waypipe --socket <sock> client` receives the
// guest's app GUI on the caller's Wayland session, so dom0's
// WAYLAND_DISPLAY must be set.
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
		Short: "Run an app in a VM on the dom0 Wayland session (no SSH: token-authenticated control channel)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return fmt.Errorf("run %s: %w", name, err)
			}
			if m.Token == "" {
				return fmt.Errorf("run %s: meta has no control token — the VM predates the waypipe control channel; delete and recreate it (or re-bake the template)", name)
			}
			cfg, err := config.Load(defaultConfigPath)
			if err != nil {
				return err
			}
			// args[1:] joined verbatim is the guest-side command line;
			// empty means the guest defaults to a login shell (parity with
			// the old `waypipe ssh` behavior).
			if err := projection.Run(cmd.Context(), projection.Deps{Waypipe: runWaypipe},
				m, cfg.Network.RouterIP, strings.Join(args[1:], " "),
				os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
			return nil
		},
	}
}

func init() {
	vmCmd().AddCommand(runCmd())
}
