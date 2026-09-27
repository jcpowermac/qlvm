// Package setup is the qlvm install orchestration: it drives the dom0 to
// the state declared by the config file. No packages are installed (Xen,
// OVS and OVN ship in the dom0 image); every step is idempotent, so
// install can be re-run at will.
package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/fw"
	"github.com/jcpowermac/qlvm/internal/nm"
	"github.com/jcpowermac/qlvm/internal/systemd"
)

// OVNPlanes is the OVN side of install, implemented by *ovn.Reconciler.
type OVNPlanes interface {
	Apply(ctx context.Context, cfg *config.Config) error
}

// OVSPlanes is the OVS side of install, implemented by *ovs.Reconciler.
type OVSPlanes interface {
	Apply(ctx context.Context, nic string) error
}

// Plan is the set of control planes install drives toward the configured
// state.
type Plan struct {
	OVN       OVNPlanes
	OVS       OVSPlanes
	FW        *fw.Manager
	NM        *nm.Manager
	SD        *systemd.Manager
	Storage   func() error // /var/lib/qvm + tree
	VifScript func() error // qlvm-vif at /etc/xen/scripts/vif-ovn
	SaveCfg   func() error // write the config file (final step)

	// Dom0Check reports whether this host is dom0; defaults to IsDom0.
	Dom0Check func() (bool, error)
	// Out receives the "=== step ===" progress lines; defaults to stdout.
	Out io.Writer
}

// Options tunes install behavior.
type Options struct {
	// SkipNICMigration allows the NIC migration to proceed while inside an
	// SSH session (the migration deactivates the NIC's own connection and
	// would otherwise drop the session).
	SkipNICMigration bool
}

// dom0CapPath is the Xen capability file; var so tests can point it
// elsewhere.
var dom0CapPath = "/proc/xen/capabilities"

// services enabled+started on the services step (spec §5.3).
var services = []string{
	"openvswitch.service",
	"ovn-northd.service",
	"ovn-controller.service",
}

// IsDom0 reports whether this host is dom0 by looking for the control_d
// capability in /proc/xen/capabilities.
func IsDom0() (bool, error) {
	b, err := os.ReadFile(dom0CapPath)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), "control_d"), nil
}

// Run drives the dom0 toward the configured state in spec §5 order:
// dom0 → storage → services → OVS → OVN → firewall → vif → config save.
// Each step prints a "=== step ===" line before running.
func Run(ctx context.Context, p *Plan, cfg *config.Config, opts Options) error {
	out := p.Out
	if out == nil {
		out = os.Stdout
	}
	check := p.Dom0Check
	if check == nil {
		check = IsDom0
	}

	step := func(name string, fn func() error) error {
		_, _ = fmt.Fprintf(out, "=== %s ===\n", name) // progress line; loss is cosmetic
		return fn()
	}

	if err := step("dom0", func() error {
		ok, err := check()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("not running on dom0: no control_d capability in %s", dom0CapPath)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := step("storage", p.Storage); err != nil {
		return err
	}

	if err := step("services", func() error {
		for _, unit := range services {
			if err := p.SD.EnableStart(ctx, unit); err != nil {
				return fmt.Errorf("%s: %w", unit, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := step("ovs", func() error {
		// The NIC migration deactivates the NIC's own NM connection and
		// would drop an SSH session; refuse before any plane is mutated.
		if os.Getenv("SSH_CONNECTION") != "" && !opts.SkipNICMigration {
			return fmt.Errorf("refusing to migrate %s during an SSH session (it would drop this session); re-run with --skip-nic-migration", cfg.Network.NIC)
		}
		if err := p.OVS.Apply(ctx, cfg.Network.NIC); err != nil {
			return err
		}
		if err := p.NM.EnsureOVSConnections(ctx, cfg); err != nil {
			return err
		}
		// The guard above already handled the SSH refusal; MigrateNIC is a
		// no-op when the NIC is already enslaved.
		return p.NM.MigrateNIC(ctx, cfg, true)
	}); err != nil {
		return err
	}

	if err := step("ovn", func() error {
		return p.OVN.Apply(ctx, cfg)
	}); err != nil {
		return err
	}

	if err := step("firewall", func() error {
		if err := p.FW.Ensure(ctx, cfg); err != nil {
			return err
		}
		// Bind the dom0 zone to the bridge-facing connections (spec §5.6).
		if err := p.NM.SetZone(ctx, nm.OVSBridge+"-iface", fw.Dom0Zone); err != nil {
			return err
		}
		return p.NM.SetZone(ctx, cfg.Network.NIC+"-ovs", fw.Dom0Zone)
	}); err != nil {
		return err
	}

	if err := step("vif", p.VifScript); err != nil {
		return err
	}

	return step("config", p.SaveCfg)
}
