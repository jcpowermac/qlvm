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
	"github.com/jcpowermac/qlvm/internal/netd"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/systemd"
)

// OVNPlanes is the OVN side of install, implemented by *ovn.Reconciler.
type OVNPlanes interface {
	Apply(ctx context.Context, cfg *config.Config) error
}

// OVSPlanes is the OVS side of install, implemented by *ovs.Reconciler.
type OVSPlanes interface {
	Apply(ctx context.Context, nic string) error
	DropEx(ctx context.Context, nic string) error
}

// Plan is the set of control planes install drives toward the configured
// state.
type Plan struct {
	OVN     OVNPlanes
	OVS     OVSPlanes
	FW      *fw.Manager
	Netd    *netd.Manager
	SD      *systemd.Manager
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
	// SkipNICMigration allows the uplink migration to proceed while inside
	// an SSH session. The migration moves the dom0's address from the NIC
	// to the OVS bridge (a new DHCP lease, new IP), which drops the
	// session.
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
		// The migration moves the dom0's address to the OVS bridge (new IP,
		// new lease) and would drop an SSH session; refuse before any plane
		// is mutated.
		if os.Getenv("SSH_CONNECTION") != "" && !opts.SkipNICMigration {
			return fmt.Errorf("refusing to migrate %s during an SSH session (the uplink would move to %s with a new IP, dropping this session); re-run with --skip-nic-migration", cfg.Network.NIC, ovs.BrEx)
		}
		// 1. networkd is DISABLED by the distro preset: a plain start (which
		//    the drop-in reload below does) never survives a reboot, so the
		//    first reboot after install left the dom0 without an uplink.
		//    Enable+start it before anything else in this step.
		if err := p.SD.EnableStart(ctx, "systemd-networkd.service"); err != nil {
			return err
		}
		// 2. networkd owns the addresses from here: DHCP on the bare NIC
		//    (current + failback state) and on the OVS bridge (migrated
		//    state). Files must exist before NM releases the NIC.
		if err := p.Netd.Ensure(cfg.Network.NIC, ovs.BrEx); err != nil {
			return err
		}
		// 3. NM's OVS plugin blocks system ports it did not create and marks
		//    external bridges unmanaged, so it must be gone before OVS.Apply
		//    enslaves the NIC. Masked: nothing can pull it back.
		if err := p.SD.EnsureMasked(ctx, "NetworkManager.service"); err != nil {
			return err
		}
		// 4. networkd must hold the NIC's lease before the NIC is enslaved.
		//    (Failure here means the uplink is down and NM is masked; manual
		//    recovery: unmask + start NetworkManager.)
		if err := p.Netd.WaitUplink(ctx, cfg.Network.NIC); err != nil {
			return fmt.Errorf("%w (NetworkManager is masked; unmask + start it to restore the uplink)", err)
		}
		// 5. OVS creates br-ex and the NIC's system port; ovs-vswitchd
		//    enslaves the NIC itself. No-op when the topology already
		//    exists (re-run).
		if err := p.OVS.Apply(ctx, cfg.Network.NIC); err != nil {
			return err
		}
		// 6. The bridge must reach the LAN. If it does not, drop the OVS
		//    topology: the NIC is released back to networkd, which re-DHCPs
		//    it — install must never leave the dom0 without an uplink.
		if err := p.Netd.WaitUplink(ctx, ovs.BrEx); err != nil {
			if rb := p.OVS.DropEx(ctx, cfg.Network.NIC); rb != nil {
				return fmt.Errorf("%w; failback also failed to drop the OVS topology: %v", err, rb)
			}
			return fmt.Errorf("%w; failback: %s topology removed, %s back on its own DHCP lease", err, ovs.BrEx, cfg.Network.NIC)
		}
		return nil
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
		// Bind the dom0 zone to the devices that carry the dom0's address:
		// the bridge (migrated state) and the NIC (pre-migration/failback).
		// Without this they fall into the default zone and inbound ssh to
		// either address is filtered.
		return p.FW.ZoneInterfaces(ctx, fw.Dom0Zone, []string{ovs.BrEx, cfg.Network.NIC})
	}); err != nil {
		return err
	}

	if err := step("vif", p.VifScript); err != nil {
		return err
	}

	return step("config", p.SaveCfg)
}
