// Package fw manages dom0 firewalld state over D-Bus. Production code
// dials the system bus directly against the firewalld 2.x config-manager
// API (verified live on this dom0, firewalld 2.4.4); tests supply a fake
// Conn recording the exact method calls.
package fw

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/jcpowermac/qlvm/internal/config"
)

// firewalld 2.x D-Bus API constants (config-manager layout).
const (
	fwService      = "org.fedoraproject.FirewallD1"
	fwConfigIface  = fwService + ".config"
	fwZoneIface    = fwService + ".config.zone"
	fwPolicyIface  = fwService + ".config.policy"
	fwConfigObject = "/org/fedoraproject/FirewallD1/config"

	// Dom0Zone is the permanent zone holding dom0.
	Dom0Zone = "dom0"
	// Dom0Policy is the rich-rule egress policy applied to dom0.
	Dom0Policy = "dom0-egress"
)

// Conn is the narrow firewalld 2.x surface Manager uses. Method names
// follow the live D-Bus methods they wrap (org.fedoraproject.FirewallD1
// .config / .config.zone / .config.policy); zone and policy arguments are
// the object paths returned by ZoneByName / PolicyByName.
type Conn interface {
	ZoneByName(zone string) (string, error)
	AddZone(zone string) (string, error)
	ZoneQueryService(zonePath, svc string) (bool, error)
	ZoneAddService(zonePath, svc string) error
	PolicyByName(name string) (string, error)
	AddPolicy(name, target string, priority int32, ingressZones, egressZones []string) (string, error)
	PolicyRichRules(policyPath string) ([]string, error)
	PolicySetRichRules(policyPath string, rules []string) error
	Reload() error
}

// Manager drives firewalld toward the configured dom0 firewall state.
type Manager struct {
	conn Conn
}

// New wraps a firewalld Conn.
func New(c Conn) *Manager { return &Manager{conn: c} }

// NewSystem wires Manager to the live firewalld on the system bus.
func NewSystem() (*Manager, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return New(&systemConn{bus: bus}), nil
}

// EgressRules is the pure rendering of an Egress config as ordered firewalld
// rich-rule strings: dns (53 udp+tcp), https (443 tcp), ssh-to-VMs (22 tcp
// to the VM supernet), icmp, then ExtraRules in order.
func (m *Manager) EgressRules(e config.Egress, supernet string) []string {
	var rules []string
	if e.AllowDNS {
		rules = append(rules,
			`rule family="ipv4" port port="53" protocol="udp" accept`,
			`rule family="ipv4" port port="53" protocol="tcp" accept`,
		)
	}
	if e.AllowHTTPS {
		rules = append(rules, `rule family="ipv4" port port="443" protocol="tcp" accept`)
	}
	if e.AllowSSHToVMs {
		rules = append(rules, fmt.Sprintf(
			`rule family="ipv4" destination address=%q port port="22" protocol="tcp" accept`, supernet))
	}
	if e.AllowICMP {
		rules = append(rules, `rule family="ipv4" protocol value="icmp" accept`)
	}
	return append(rules, e.ExtraRules...)
}

// Ensure creates the dom0 zone (ssh service) and the dom0-egress policy
// (target DROP, priority 100, ingress host, egress any, egress rich rules),
// skipping anything that already exists, then reloads if anything changed
// (config-manager changes are permanent and take effect on reload).
func (m *Manager) Ensure(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	changed := false

	zpath, err := m.conn.ZoneByName(Dom0Zone)
	if err != nil {
		return err
	}
	if zpath == "" {
		zpath, err = m.conn.AddZone(Dom0Zone)
		if err != nil {
			return err
		}
		changed = true
	}
	hasSSH, err := m.conn.ZoneQueryService(zpath, "ssh")
	if err != nil {
		return err
	}
	if !hasSSH {
		if err := m.conn.ZoneAddService(zpath, "ssh"); err != nil {
			return err
		}
		changed = true
	}

	ppath, err := m.conn.PolicyByName(Dom0Policy)
	if err != nil {
		return err
	}
	if ppath == "" {
		ppath, err = m.conn.AddPolicy(Dom0Policy, "DROP", 100, []string{"host"}, []string{"any"})
		if err != nil {
			return err
		}
		changed = true
	}

	have, err := m.conn.PolicyRichRules(ppath)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(have))
	for _, r := range have {
		present[r] = true
	}
	var missing []string
	for _, rule := range m.EgressRules(cfg.Firewall.Egress, cfg.VMSupernet()) {
		if !present[rule] {
			missing = append(missing, rule)
		}
	}
	if len(missing) > 0 {
		if err := m.conn.PolicySetRichRules(ppath, append(have, missing...)); err != nil {
			return err
		}
		changed = true
	}

	if changed {
		return m.conn.Reload()
	}
	return nil
}

// systemConn is the godbus-backed Conn for the live system bus. Config
// objects hold permanent state; Reload applies it to the runtime.
type systemConn struct {
	bus *dbus.Conn
}

func (s *systemConn) cfg() dbus.BusObject {
	return s.bus.Object(fwService, dbus.ObjectPath(fwConfigObject))
}

// zoneSettingsDict mirrors the a{sv} the firewalld 2.x client sends to
// addZone2 (keys/types from firewall.core settings_dbus_type).
func zoneSettingsDict(zone string) map[string]dbus.Variant {
	var ports [][2]string
	var forwardPorts [][4]string
	var sourcePorts [][2]string
	return map[string]dbus.Variant{
		"version":              dbus.MakeVariant(""),
		"short":                dbus.MakeVariant(zone),
		"description":          dbus.MakeVariant(""),
		"target":               dbus.MakeVariant("{chain}_{zone}"),
		"services":             dbus.MakeVariant([]string{}),
		"ports":                dbus.MakeVariant(ports),
		"icmp_blocks":          dbus.MakeVariant([]string{}),
		"masquerade":           dbus.MakeVariant(false),
		"forward_ports":        dbus.MakeVariant(forwardPorts),
		"interfaces":           dbus.MakeVariant([]string{}),
		"sources":              dbus.MakeVariant([]string{}),
		"rules_str":            dbus.MakeVariant([]string{}),
		"protocols":            dbus.MakeVariant([]string{}),
		"source_ports":         dbus.MakeVariant(sourcePorts),
		"icmp_block_inversion": dbus.MakeVariant(false),
		"forward":              dbus.MakeVariant(false),
		"ingress_priority":     dbus.MakeVariant(int32(0)),
		"egress_priority":      dbus.MakeVariant(int32(0)),
	}
}

// policySettingsDict mirrors the a{sv} the firewalld 2.x client sends to
// addPolicy (keys/types from FirewallClientPolicySettings).
func policySettingsDict(name, target string, priority int32, ingressZones, egressZones []string) map[string]dbus.Variant {
	var ports [][2]string
	var forwardPorts [][4]string
	var sourcePorts [][2]string
	return map[string]dbus.Variant{
		"description":   dbus.MakeVariant(""),
		"disable":       dbus.MakeVariant(false),
		"egress_zones":  dbus.MakeVariant(egressZones),
		"forward_ports": dbus.MakeVariant(forwardPorts),
		"icmp_blocks":   dbus.MakeVariant([]string{}),
		"ingress_zones": dbus.MakeVariant(ingressZones),
		"masquerade":    dbus.MakeVariant(false),
		"ports":         dbus.MakeVariant(ports),
		"priority":      dbus.MakeVariant(priority),
		"protocols":     dbus.MakeVariant([]string{}),
		"rich_rules":    dbus.MakeVariant([]string{}),
		"services":      dbus.MakeVariant([]string{}),
		"short":         dbus.MakeVariant(name),
		"source_ports":  dbus.MakeVariant(sourcePorts),
		"target":        dbus.MakeVariant(target),
		"version":       dbus.MakeVariant(""),
	}
}

func (s *systemConn) ZoneByName(zone string) (string, error) {
	var p dbus.ObjectPath
	err := s.cfg().Call(fwConfigIface+".getZoneByName", 0, zone).Store(&p)
	return string(p), err
}

func (s *systemConn) AddZone(zone string) (string, error) {
	var p dbus.ObjectPath
	err := s.cfg().Call(fwConfigIface+".addZone2", 0, zone, zoneSettingsDict(zone)).Store(&p)
	return string(p), err
}

func (s *systemConn) ZoneQueryService(zonePath, svc string) (bool, error) {
	var has bool
	err := s.bus.Object(fwService, dbus.ObjectPath(zonePath)).
		Call(fwZoneIface+".queryService", 0, svc).Store(&has)
	return has, err
}

func (s *systemConn) ZoneAddService(zonePath, svc string) error {
	return s.bus.Object(fwService, dbus.ObjectPath(zonePath)).
		Call(fwZoneIface+".addService", 0, svc).Store()
}

func (s *systemConn) PolicyByName(name string) (string, error) {
	var p dbus.ObjectPath
	err := s.cfg().Call(fwConfigIface+".getPolicyByName", 0, name).Store(&p)
	return string(p), err
}

func (s *systemConn) AddPolicy(name, target string, priority int32, ingressZones, egressZones []string) (string, error) {
	var p dbus.ObjectPath
	err := s.cfg().Call(fwConfigIface+".addPolicy", 0, name, policySettingsDict(name, target, priority, ingressZones, egressZones)).Store(&p)
	return string(p), err
}

// policySettings fetches the full a{sv} settings of a policy config object.
func (s *systemConn) policySettings(policyPath string) (map[string]dbus.Variant, error) {
	var m map[string]dbus.Variant
	err := s.bus.Object(fwService, dbus.ObjectPath(policyPath)).
		Call(fwPolicyIface+".getSettings", 0).Store(&m)
	return m, err
}

func (s *systemConn) PolicyRichRules(policyPath string) ([]string, error) {
	settings, err := s.policySettings(policyPath)
	if err != nil {
		return nil, err
	}
	v, ok := settings["rich_rules"]
	if !ok {
		return nil, nil
	}
	rules, ok := v.Value().([]string)
	if !ok {
		return nil, fmt.Errorf("rich_rules: unexpected type %T", v.Value())
	}
	return rules, nil
}

func (s *systemConn) PolicySetRichRules(policyPath string, rules []string) error {
	settings, err := s.policySettings(policyPath)
	if err != nil {
		return err
	}
	settings["rich_rules"] = dbus.MakeVariant(rules)
	return s.bus.Object(fwService, dbus.ObjectPath(policyPath)).
		Call(fwPolicyIface+".update", 0, settings).Store()
}

func (s *systemConn) Reload() error {
	return s.bus.Object(fwService, dbus.ObjectPath("/org/fedoraproject/FirewallD1")).
		Call(fwService+".reload", 0).Store()
}
