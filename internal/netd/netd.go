// Package netd manages dom0 IP configuration through systemd-networkd's
// file-based config. NetworkManager is masked by external provisioning:
// its OVS plugin fights ovs-vswitchd (it blocks system ports it did not
// create and marks externally created bridges unmanaged), so OVS owns
// the topology and networkd owns the addresses.
//
// Three .network files are written: one DHCPs the physical NIC (the
// pre-migration and failback state), one DHCPs the OVS bridge (the
// migrated state) and carries the VM-supernet route via the OVN gateway
// router, and one addresses the qvm-dom0 OVS port on the work switch
// (the dom0's LSW presence, so guest→dom0 dials land on an address the
// dom0 owns). Whichever device holds the uplink carries the dom0's only
// LAN address. All of this is kernel state that dies on every dom0
// reboot, so qlvm-agent re-Ensures it on each start.
//
// The OVN side of the dom0 LSW join (the "dom0" LSP) and the qvm-dom0
// OVS port still persist in their own databases and are not reconciled
// here.
package netd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// Dir is where networkd reads its .network files.
	Dir = "/etc/systemd/network"
	// filePrefix orders these files last so host-specific drop-ins win.
	filePrefix = "90-qlvm-"
	// Dom0LSWIf is the OVS port on br-int that carries the dom0 onto the
	// work LSW (one per dom0, named after the legacy manual plumbing).
	Dom0LSWIf = "qvm-dom0"

	sdService = "org.freedesktop.systemd1"
	sdObject  = "/org/freedesktop/systemd1"
)

// UplinkTimeout bounds how long WaitUplink polls for a lease.
var UplinkTimeout = 30 * time.Second

// Manager writes networkd config and waits for leases.
type Manager struct {
	// Dir is the networkd drop-in directory (tests point it at a tmp dir).
	Dir string
	// Reload triggers a networkd config reload; nil skips it (tests).
	Reload func() error
	// UplinkOK reports whether dev holds a global IPv4 address.
	UplinkOK func(dev string) bool
}

// NewSystem wires Manager to the live dom0: real drop-in dir, a D-Bus
// daemon-reload, and a netlink address check.
func NewSystem() (*Manager, error) {
	return &Manager{
		Dir:      Dir,
		Reload:   reloadSystemd,
		UplinkOK: hasGlobalV4,
	}, nil
}

// Params is the desired dom0 network state rendered to drop-ins.
type Params struct {
	// NIC is the physical network device (DHCP).
	NIC string
	// Bridge is the OVS uplink bridge device (DHCP).
	Bridge string
	// RouterIP is the OVN gateway router's LAN IP — the next hop for the
	// VM supernet route. Empty disables the route.
	RouterIP string
	// Supernet covers all VM subnets (e.g. "10.100.0.0/16").
	Supernet string
	// Dom0LSW is qvm-dom0's per-domain LSW addresses (e.g.
	// ["10.100.1.2/24"]). Each also yields the connected /24 route that
	// gives guest→dom0 traffic a direct LSW path. Empty skips the file.
	Dom0LSW []string
}

// files returns the .network drop-ins for the NIC, the OVS bridge, and
// (when configured) the qvm-dom0 LSW join.
func (m *Manager) files(p Params) map[string]string {
	br := "[Match]\nName=" + p.Bridge + "\n\n[Network]\nDHCP=ipv4\n"
	if p.RouterIP != "" && p.Supernet != "" {
		br += "\n[Route]\nDestination=" + p.Supernet + "\nGateway=" + p.RouterIP + "\n"
	}
	out := map[string]string{
		filePrefix + "nic.network": "[Match]\nName=" + p.NIC + "\n\n[Network]\nDHCP=ipv4\n",
		filePrefix + "br-ex.network": br,
	}
	if len(p.Dom0LSW) > 0 {
		var b strings.Builder
		b.WriteString("[Match]\nName=" + Dom0LSWIf + "\n\n[Network]\n")
		for _, a := range p.Dom0LSW {
			b.WriteString("Address=" + a + "\n")
		}
		out[filePrefix+"dom0.network"] = b.String()
	}
	return out
}

// Ensure writes the drop-ins and reloads networkd, only touching files
// that changed so a re-run is a no-op.
func (m *Manager) Ensure(p Params) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil { //nolint:gosec,G301
		return fmt.Errorf("create %s: %w", m.Dir, err)
	}
	changed := false
	for path, want := range m.files(p) {
		full := filepath.Join(m.Dir, path)
		have, err := os.ReadFile(full) //nolint:gosec,G304
		if err == nil && string(have) == want {
			continue
		}
		if err := os.WriteFile(full, []byte(want), 0o644); err != nil { //nolint:gosec,G306
			return fmt.Errorf("write %s: %w", full, err)
		}
		changed = true
	}
	if changed && m.Reload != nil {
		if err := m.Reload(); err != nil {
			return fmt.Errorf("daemon-reload: %w", err)
		}
	}
	return nil
}

// Enslaved reports whether dev has a kernel master (an OVS system port
// shows "ovs-system"), i.e. its address already moved to the bridge and
// the bare device no longer carries its own lease.
func Enslaved(dev string) (bool, error) {
	target, err := os.Readlink("/sys/class/net/" + dev + "/master")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return target != "", nil
}

// WaitUplink polls until dev holds a global IPv4 address (its DHCP
// lease) or the timeout expires.
func (m *Manager) WaitUplink(ctx context.Context, dev string) error {
	deadline := time.Now().Add(UplinkTimeout)
	for {
		if m.UplinkOK(dev) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s has no IPv4 address after %s", dev, UplinkTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// reloadNetworkd restarts systemd-networkd so it picks up the drop-ins.
// A restart (not reload) is used because networkd's ExecReload is a
// plain SIGHUP that does not re-run matching for newly added files.
func reloadSystemd() error {
	bus, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	var job dbus.ObjectPath
	return bus.Object(sdService, dbus.ObjectPath(sdObject)).
		Call(sdService+".Manager.RestartUnit", 0, "systemd-networkd.service", "replace").Store(&job)
}

// hasGlobalV4 reports whether dev holds a global unicast IPv4 address.
func hasGlobalV4(dev string) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if ifc.Name != dev {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return false
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if ok && ipn.IP.To4() != nil && ipn.IP.IsGlobalUnicast() {
				return true
			}
		}
	}
	return false
}
