// Package nm manages dom0 NetworkManager connections over D-Bus. Production
// code dials the system bus directly; tests supply a fake Conn recording
// the exact method calls.
package nm

import (
	"context"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/jcpowermac/qlvm/internal/config"
)

// NetworkManager D-Bus API constants.
const (
	nmService  = "org.freedesktop.NetworkManager"
	nmObject   = "/org/freedesktop/NetworkManager"
	propsIface = "org.freedesktop.DBus.Properties"

	// OVSBridge is the dom0 OVS bridge connection name.
	OVSBridge = "br-ex"
)

// Conn is the narrow NetworkManager surface Manager uses. Method names
// follow the NM D-Bus methods they wrap.
type Conn interface {
	ConNames() ([]string, error)
	AddConnection(spec map[string]map[string]any) error
	SetConnectionValue(conName, key, value string) error
	Activate(conName, dev string) error
	Deactivate(conName string) error
}

// Manager drives NetworkManager toward the configured OVS topology.
type Manager struct {
	conn Conn
}

// New wraps a NetworkManager Conn.
func New(c Conn) *Manager { return &Manager{conn: c} }

// NewSystem wires Manager to the live NetworkManager on the system bus.
func NewSystem() (*Manager, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return New(&systemConn{bus: bus}), nil
}

// ovsSpecs is the NM connection set for the OVS topology: br-ex
// (ovs-bridge), br-ex-port, br-ex-iface (ovs-interface, ipv4 auto),
// <nic>-port, and <nic>-ovs (ethernet slave).
func ovsSpecs(cfg *config.Config) []map[string]map[string]any {
	nic := cfg.Network.NIC
	return []map[string]map[string]any{
		{
			"connection": {"id": OVSBridge, "type": "bridge"},
			"ipv4":       {"method": "link-local"},
			"ipv6":       {"method": "ignore"},
			"ovs-bridge": {},
		},
		{
			"connection": {"id": OVSBridge + "-port", "type": "ovs-interface"},
			"ipv4":       {"method": "link-local"},
			"ipv6":       {"method": "ignore"},
		},
		{
			"connection": {"id": OVSBridge + "-iface", "type": "ovs-interface"},
			"ipv4":       {"method": "auto"},
			"ipv6":       {"method": "ignore"},
		},
		{
			"connection": {"id": nic + "-port", "type": "ovs-interface"},
			"ipv4":       {"method": "link-local"},
			"ipv6":       {"method": "ignore"},
		},
		{
			"connection": {"id": nic + "-ovs", "type": "ethernet", "master": OVSBridge, "slave-type": "ovs-interface"},
		},
	}
}

// EnsureOVSConnections adds the five OVS connections, skipping any whose
// con-name already exists.
func (m *Manager) EnsureOVSConnections(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := m.conn.ConNames()
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, name := range existing {
		have[name] = true
	}
	for _, spec := range ovsSpecs(cfg) {
		id, _ := spec["connection"]["id"].(string)
		if have[id] {
			continue
		}
		if err := m.conn.AddConnection(spec); err != nil {
			return err
		}
	}
	return nil
}

// SetZone sets connection.zone on the named connection (used on
// br-ex-iface and <nic>-ovs).
func (m *Manager) SetZone(ctx context.Context, conName, zone string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.conn.SetConnectionValue(conName, "zone", zone)
}

// MigrateNIC moves the dom0 NIC from its own connection into the OVS
// topology. It refuses while inside an SSH session unless allowSSH is set
// (re-run with --skip-nic-migration), since the NIC's deactivation would
// drop the session.
func (m *Manager) MigrateNIC(ctx context.Context, cfg *config.Config, allowSSH bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("SSH_CONNECTION") != "" && !allowSSH {
		return fmt.Errorf("refusing to migrate %s during an SSH session (it would drop this session); re-run with --skip-nic-migration", cfg.Network.NIC)
	}
	if err := m.conn.Activate(OVSBridge, OVSBridge); err != nil {
		return err
	}
	return m.conn.Deactivate(cfg.Network.NICConnection)
}

// systemConn is the godbus-backed Conn for the live system bus.
type systemConn struct {
	bus *dbus.Conn
}

func (s *systemConn) root() dbus.BusObject { return s.bus.Object(nmService, dbus.ObjectPath(nmObject)) }

func (s *systemConn) propGet(p dbus.ObjectPath, iface, name string, dest any) error {
	return s.bus.Object(propsIface, p).Call(propsIface+".Get", 0, iface, name).Store(dest)
}

// connPath resolves a con-name to its NMConnection object path.
func (s *systemConn) connPath(conName string) (dbus.ObjectPath, error) {
	var paths []dbus.ObjectPath
	if err := s.root().Call(nmService+".ListConnections", 0).Store(&paths); err != nil {
		return "", err
	}
	for _, p := range paths {
		var id string
		if err := s.propGet(p, nmService+".Connection", "connection.id", &id); err != nil {
			return "", err
		}
		if id == conName {
			return p, nil
		}
	}
	return "", fmt.Errorf("connection %q not found", conName)
}

// devicePath resolves a kernel device name to its NMDevice object path.
func (s *systemConn) devicePath(dev string) (dbus.ObjectPath, error) {
	var paths []dbus.ObjectPath
	if err := s.root().Call(nmService+".GetDevices", 0).Store(&paths); err != nil {
		return "", err
	}
	for _, p := range paths {
		var iface string
		if err := s.propGet(p, nmService+".Device", "Interface", &iface); err != nil {
			return "", err
		}
		if iface == dev {
			return p, nil
		}
	}
	return "", fmt.Errorf("device %q not found", dev)
}

func (s *systemConn) ConNames() ([]string, error) {
	var paths []dbus.ObjectPath
	if err := s.root().Call(nmService+".ListConnections", 0).Store(&paths); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		var id string
		if err := s.propGet(p, nmService+".Connection", "connection.id", &id); err != nil {
			return nil, err
		}
		names = append(names, id)
	}
	return names, nil
}

func (s *systemConn) AddConnection(spec map[string]map[string]any) error {
	av := make(map[string]dbus.Variant, len(spec))
	for setting, vals := range spec {
		d := make(map[string]dbus.Variant, len(vals))
		for k, v := range vals {
			d[k] = dbus.MakeVariant(v)
		}
		av[setting] = dbus.MakeVariant(d)
	}
	var p dbus.ObjectPath
	return s.root().Call(nmService+".AddConnection", 0, av).Store(&p)
}

func (s *systemConn) SetConnectionValue(conName, key, value string) error {
	p, err := s.connPath(conName)
	if err != nil {
		return err
	}
	update := map[string]dbus.Variant{key: dbus.MakeVariant(value)}
	err = s.bus.Object(nmService, p).Call(nmService+".Connection.UpdateConnection", 0, update, true).Store()
	return err
}

func (s *systemConn) Activate(conName, dev string) error {
	connP, err := s.connPath(conName)
	if err != nil {
		return err
	}
	devP, err := s.devicePath(dev)
	if err != nil {
		return err
	}
	var p dbus.ObjectPath
	return s.root().Call(nmService+".ActivateConnection", 0, connP, devP, dbus.ObjectPath("/")).Store(&p)
}

func (s *systemConn) Deactivate(conName string) error {
	connP, err := s.connPath(conName)
	if err != nil {
		return err
	}
	var actives []dbus.ObjectPath
	if err := s.root().Call(nmService+".ListActiveConnections", 0).Store(&actives); err != nil {
		return err
	}
	for _, a := range actives {
		var activeConn dbus.ObjectPath
		if err := s.propGet(a, nmService+".Connection.Active", "connection", &activeConn); err != nil {
			return err
		}
		if activeConn != connP {
			continue
		}
		return s.bus.Object(nmService, a).Call(nmService+".Connection.Active.Deactivate", 0).Store()
	}
	return fmt.Errorf("connection %q is not active", conName)
}
