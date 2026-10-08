// Package netd manages dom0 IP configuration through systemd-networkd's
// file-based config. NetworkManager is masked during install: its OVS
// plugin fights ovs-vswitchd (it blocks system ports it did not create
// and marks externally created bridges unmanaged), so OVS owns the
// topology and networkd owns the addresses.
//
// Two .network files are written: one DHCPs the physical NIC (the
// pre-migration and failback state) and one DHCPs the OVS bridge (the
// migrated state). Whichever device holds the uplink carries the dom0's
// only LAN address.
package netd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// Dir is where networkd reads its .network files.
	Dir = "/etc/systemd/network"
	// filePrefix orders these files last so host-specific drop-ins win.
	filePrefix = "90-qlvm-"

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

// files returns the .network drop-ins for the NIC and the OVS bridge.
func (m *Manager) files(nic, br string) map[string]string {
	return map[string]string{
		filePrefix + "nic.network": "[Match]\nName=" + nic + "\n\n[Network]\nDHCP=ipv4\n",
		filePrefix + "br-ex.network": "[Match]\nName=" + br + "\n\n[Network]\nDHCP=ipv4\n",
	}
}

// Ensure writes the drop-ins and reloads networkd, only touching files
// that changed so a re-run is a no-op.
func (m *Manager) Ensure(nic, br string) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", m.Dir, err)
	}
	changed := false
	for path, want := range m.files(nic, br) {
		full := filepath.Join(m.Dir, path)
		have, err := os.ReadFile(full)
		if err == nil && string(have) == want {
			continue
		}
		if err := os.WriteFile(full, []byte(want), 0o644); err != nil {
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
