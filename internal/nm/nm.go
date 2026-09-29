// Package nm manages dom0 NetworkManager connections over D-Bus. Production
// code dials the system bus directly against the NM 1.56 API (verified live
// on this dom0: the connection CRUD API moved to the
// org.freedesktop.NetworkManager.Settings interface on
// /org/freedesktop/NetworkManager/Settings); tests supply a fake Conn
// recording the exact method calls.
package nm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/jcpowermac/qlvm/internal/config"
)

// NetworkManager 1.56 D-Bus API constants.
const (
	nmService        = "org.freedesktop.NetworkManager"
	nmObject         = "/org/freedesktop/NetworkManager"
	nmSettingsIface  = nmService + ".Settings"
	nmSettingsObject = nmObject + "/Settings"
	nmConnIface      = nmService + ".Settings.Connection"
	nmActiveIface    = nmService + ".Connection.Active"
	propsIface       = "org.freedesktop.DBus.Properties"

	// OVSBridge is the dom0 OVS bridge connection name.
	OVSBridge = "br-ex"
)

// Conn is the narrow NetworkManager 1.56 surface Manager uses. Method names
// follow the live D-Bus methods they wrap (Settings.ListConnections /
// Settings.AddConnection / Settings.Connection.Update / ActivateConnection /
// DeactivateConnection).
type Conn interface {
	ConNames() ([]string, error)
	AddConnection(spec map[string]map[string]any) error
	SetConnectionValue(conName, key, value string) error
	ConnZone(conName string) (string, error)
	Activate(conName, dev string) error
	Deactivate(conName string) error
}

// Manager drives NetworkManager toward the configured OVS topology.
type Manager struct {
	conn Conn
	// NICEnslaved reports whether a dom0 device already has a kernel
	// master (e.g. enslaved to the OVS bridge); injected for tests,
	// defaulting to /sys/class/net/<dev>/master lookup.
	NICEnslaved func(dev string) bool
}

// New wraps a NetworkManager Conn.
func New(c Conn) *Manager {
	return &Manager{conn: c, NICEnslaved: sysNICEnslaved}
}

// sysNICEnslaved reports whether dev has a kernel master; enslavement
// survives NM deactivation, so it is the durable "already migrated"
// marker.
func sysNICEnslaved(dev string) bool {
	_, err := os.Readlink("/sys/class/net/" + dev + "/master")
	return err == nil
}

// NewSystem wires Manager to the live NetworkManager on the system bus.
func NewSystem() (*Manager, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return New(&systemConn{bus: bus}), nil
}

// ovsSpecs is the NM connection set for the OVS topology. The shape is the
// one proven on this dom0 (observed via NM D-Bus, 2026-09-26): br-ex is a
// plain ovs-bridge carrying the LAN IP (ipv4 auto), ports use the ovs-port
// type as ovs-bridge slaves, and the leaves are ovs-interface — internal
// for the br-ex interface, system (with an ethernet slave) for the NIC.
// The internal leaf's con-name is the cosmetic "br-ex-iface" while its
// interface-name is the OVS interface itself, "br-ex".
func ovsSpecs(cfg *config.Config) []map[string]map[string]any {
	nic := cfg.Network.NIC
	return []map[string]map[string]any{
		{
			"connection": {"id": OVSBridge, "type": "ovs-bridge", "interface-name": OVSBridge},
			"ipv4":       {"method": "auto"},
			"ipv6":       {"method": "auto"},
		},
		{
			"connection": {"id": OVSBridge + "-port", "type": "ovs-port", "slave-type": "ovs-bridge", "master": OVSBridge, "interface-name": OVSBridge + "-port"},
		},
		{
			"connection":    {"id": OVSBridge + "-iface", "type": "ovs-interface", "slave-type": "ovs-port", "master": OVSBridge + "-port", "interface-name": OVSBridge},
			"ovs-interface": {"type": "internal"},
		},
		{
			"connection": {"id": nic + "-port", "type": "ovs-port", "slave-type": "ovs-bridge", "master": OVSBridge, "interface-name": nic + "-port"},
		},
		{
			"connection":    {"id": nic + "-ovs", "type": "ethernet", "slave-type": "ovs-port", "master": nic + "-port", "interface-name": nic},
			"ovs-interface": {"type": "system"},
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
	cur, err := m.conn.ConnZone(conName)
	if err != nil {
		return err
	}
	if cur == zone {
		return nil
	}
	return m.conn.SetConnectionValue(conName, "zone", zone)
}

// MigrateNIC moves the dom0 NIC from its own connection into the OVS
// topology. It refuses while inside an SSH session unless allowSSH is set
// (re-run with --skip-nic-migration), since the NIC's deactivation would
// drop the session. It is a no-op when the NIC already has a kernel
// master, so install can be re-run at will.
func (m *Manager) MigrateNIC(ctx context.Context, cfg *config.Config, allowSSH bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("SSH_CONNECTION") != "" && !allowSSH {
		return fmt.Errorf("refusing to migrate %s during an SSH session (it would drop this session); re-run with --skip-nic-migration", cfg.Network.NIC)
	}
	if m.NICEnslaved(cfg.Network.NIC) {
		return nil
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

func (s *systemConn) settings() dbus.BusObject {
	return s.bus.Object(nmService, dbus.ObjectPath(nmSettingsObject))
}

func (s *systemConn) propGet(p dbus.ObjectPath, iface, name string, dest any) error {
	// Destination is the owning service name (NetworkManager), not the
	// interface name — see systemd.go for why a Properties destination
	// comes back as "The name is not activatable".
	return s.bus.Object(nmService, p).Call(propsIface+".Get", 0, iface, name).Store(dest)
}

// connPath resolves a con-name to its NMConnection object path.
func (s *systemConn) connPath(conName string) (dbus.ObjectPath, error) {
	var paths []dbus.ObjectPath
	if err := s.settings().Call(nmSettingsIface+".ListConnections", 0).Store(&paths); err != nil {
		return "", err
	}
	for _, p := range paths {
		id, err := s.connID(p)
		if err != nil {
			return "", err
		}
		if id == conName {
			return p, nil
		}
	}
	return "", fmt.Errorf("connection %q not found", conName)
}

// ConnZone reads connection.zone on the named connection ("" when unset).
func (s *systemConn) ConnZone(conName string) (string, error) {
	p, err := s.connPath(conName)
	if err != nil {
		return "", err
	}
	var settings map[string]map[string]dbus.Variant
	if err := s.bus.Object(nmService, p).Call(nmConnIface+".GetSettings", 0).Store(&settings); err != nil {
		return "", err
	}
	conn, ok := settings["connection"]
	if !ok {
		return "", fmt.Errorf("connection %q has no connection setting", conName)
	}
	v, ok := conn["zone"]
	if !ok {
		return "", nil
	}
	z, _ := v.Value().(string)
	return z, nil
}

// connID reads the connection.id setting of a Settings.Connection object.
func (s *systemConn) connID(p dbus.ObjectPath) (string, error) {
	var settings map[string]map[string]dbus.Variant
	if err := s.bus.Object(nmService, p).Call(nmConnIface+".GetSettings", 0).Store(&settings); err != nil {
		return "", err
	}
	conn, ok := settings["connection"]
	if !ok {
		return "", fmt.Errorf("connection object %s has no connection setting", p)
	}
	id, ok := conn["id"].Value().(string)
	if !ok {
		return "", fmt.Errorf("connection object %s has no id", p)
	}
	return id, nil
}

func (s *systemConn) ConNames() ([]string, error) {
	var paths []dbus.ObjectPath
	if err := s.settings().Call(nmSettingsIface+".ListConnections", 0).Store(&paths); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		id, err := s.connID(p)
		if err != nil {
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
	return s.settings().Call(nmSettingsIface+".AddConnection", 0, av).Store(&p)
}

// nmConnectionsDir holds NM's keyfile-format .nmconnection files.
const nmConnectionsDir = "/etc/NetworkManager/system-connections"

// keyfilePath resolves conName to its .nmconnection keyfile.
func keyfilePath(conName string) (string, error) {
	entries, err := os.ReadDir(nmConnectionsDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nmconnection") {
			continue
		}
		p := filepath.Join(nmConnectionsDir, e.Name())
		data, err := os.ReadFile(p) // #nosec G304 — p comes from listing the fixed nmConnectionsDir
		if err != nil {
			return "", err
		}
		if keyInSection(string(data), "connection", "id") == conName {
			return p, nil
		}
	}
	return "", fmt.Errorf("connection %q keyfile not found in %s", conName, nmConnectionsDir)
}

// keyInSection returns the value of key inside the named section, or "".
func keyInSection(data, section, key string) string {
	in := false
	for _, l := range strings.Split(data, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = t == "["+section+"]"
			continue
		}
		if in {
			if v, ok := strings.CutPrefix(t, key+"="); ok {
				return v
			}
		}
	}
	return ""
}

// setKeyInSection replaces key=value inside the named section, appending
// the line at the section end when absent.
func setKeyInSection(data, section, key, value string) string {
	lines := strings.Split(data, "\n")
	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			if start >= 0 {
				end = i
				break
			}
			if t == "["+section+"]" {
				start = i + 1
			}
			continue
		}
		if start >= 0 {
			if _, ok := strings.CutPrefix(t, key+"="); ok {
				lines[i] = key + "=" + value
				return strings.Join(lines, "\n")
			}
		}
	}
	if start < 0 {
		return data
	}
	if end < 0 {
		end = len(lines)
	}
	return strings.Join(append(append(lines[:end:end], key+"="+value), lines[end:]...), "\n")
}

// SetConnectionValue edits the connection keyfile (NM's D-Bus Update takes
// full settings and re-validates the connection: partial dicts and
// structured types like ipv6.addresses a(ayuay) don't survive the
// a{sv} round-trip), then tells NM to reload its on-disk connections.
func (s *systemConn) SetConnectionValue(conName, key, value string) error {
	p, err := keyfilePath(conName)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p) // #nosec G304 — p came from keyfilePath (fixed dir listing)
	if err != nil {
		return err
	}
	out := setKeyInSection(string(data), "connection", key, value)
	tmp := p + ".qlvm-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil { // #nosec G703 — same p
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	// Reload(1): connections only — no device rescan.
	return s.root().Call(nmService+".Reload", 0, uint32(1)).Store()
}

func (s *systemConn) Activate(conName, dev string) error {
	connP, err := s.connPath(conName)
	if err != nil {
		return err
	}
	var devP dbus.ObjectPath
	if err := s.root().Call(nmService+".GetDeviceByIpIface", 0, dev).Store(&devP); err != nil {
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
	if err := s.propGet(dbus.ObjectPath(nmObject), nmService, "ActiveConnections", &actives); err != nil {
		return err
	}
	for _, a := range actives {
		var activeConn dbus.ObjectPath
		if err := s.propGet(a, nmActiveIface, "Connection", &activeConn); err != nil {
			return err
		}
		if activeConn != connP {
			continue
		}
		return s.root().Call(nmService+".DeactivateConnection", 0, a).Store()
	}
	return fmt.Errorf("connection %q is not active", conName)
}
