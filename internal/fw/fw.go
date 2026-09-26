// Package fw manages dom0 firewalld state over D-Bus. Production code
// dials the system bus directly; tests supply a fake Conn recording the
// exact method calls.
package fw

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/jcpowermac/qlvm/internal/config"
)

// firewalld D-Bus API constants.
const (
	fwService    = "org.fedoraproject.FirewallD1"
	fwObject     = "/org/fedoraproject/FirewallD1"
	zoneObject   = fwObject + "/Zone/%s"
	policyObject = fwObject + "/Policy/%s"

	// Dom0Zone is the permanent zone holding dom0.
	Dom0Zone = "dom0"
	// Dom0Policy is the rich-rule egress policy applied to dom0.
	Dom0Policy = "dom0-egress"
)

// Conn is the narrow firewalld surface Manager uses. Method names follow
// the firewalld D-Bus methods they wrap.
type Conn interface {
	ZoneExists(zone string) (bool, error)
	NewZone(zone string) error
	ZoneHasService(zone, svc string) (bool, error)
	ZoneAddService(zone, svc string) error
	Reload() error
	PolicyExists(name string) (bool, error)
	NewPolicy(name string) error
	PolicySetTarget(name, target string) error
	PolicySetPriority(name string, priority int32) error
	PolicyAddIngressZone(name, zone string) error
	PolicyAddEgressZone(name, zone string) error
	PolicyAddRichRule(name, rule string) error
	PolicyRichRules(name string) ([]string, error)
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
// skipping anything that already exists, then reloads if anything changed.
func (m *Manager) Ensure(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	changed := false

	zoneExists, err := m.conn.ZoneExists(Dom0Zone)
	if err != nil {
		return err
	}
	if !zoneExists {
		if err := m.conn.NewZone(Dom0Zone); err != nil {
			return err
		}
		changed = true
	}
	sshExists, err := m.conn.ZoneHasService(Dom0Zone, "ssh")
	if err != nil {
		return err
	}
	if !sshExists {
		if err := m.conn.ZoneAddService(Dom0Zone, "ssh"); err != nil {
			return err
		}
		changed = true
	}

	policyExists, err := m.conn.PolicyExists(Dom0Policy)
	if err != nil {
		return err
	}
	if !policyExists {
		if err := m.conn.NewPolicy(Dom0Policy); err != nil {
			return err
		}
		for _, call := range []func() error{
			func() error { return m.conn.PolicySetTarget(Dom0Policy, "DROP") },
			func() error { return m.conn.PolicySetPriority(Dom0Policy, 100) },
			func() error { return m.conn.PolicyAddIngressZone(Dom0Policy, "host") },
			func() error { return m.conn.PolicyAddEgressZone(Dom0Policy, "any") },
		} {
			if err := call(); err != nil {
				return err
			}
		}
		changed = true
	}

	have, err := m.conn.PolicyRichRules(Dom0Policy)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(have))
	for _, r := range have {
		present[r] = true
	}
	for _, rule := range m.EgressRules(cfg.Firewall.Egress, cfg.VMSupernet()) {
		if present[rule] {
			continue
		}
		if err := m.conn.PolicyAddRichRule(Dom0Policy, rule); err != nil {
			return err
		}
		changed = true
	}

	if changed {
		return m.conn.Reload()
	}
	return nil
}

// systemConn is the godbus-backed Conn for the live system bus. All calls
// use permanent=true so state survives the Reload that follows.
type systemConn struct {
	bus *dbus.Conn
}

func (s *systemConn) root() dbus.BusObject { return s.bus.Object(fwService, dbus.ObjectPath(fwObject)) }

func (s *systemConn) zone(zone string) dbus.BusObject {
	return s.bus.Object(fwService, dbus.ObjectPath(fmt.Sprintf(zoneObject, zone)))
}

func (s *systemConn) policy(name string) dbus.BusObject {
	return s.bus.Object(fwService, dbus.ObjectPath(fmt.Sprintf(policyObject, name)))
}

func (s *systemConn) ZoneExists(zone string) (bool, error) {
	var exists bool
	err := s.root().Call(fwService+".QueryZone", 0, zone).Store(&exists)
	return exists, err
}

func (s *systemConn) NewZone(zone string) error {
	var p dbus.ObjectPath
	return s.root().Call(fwService+".NewZone", 0, zone).Store(&p)
}

func (s *systemConn) ZoneHasService(zone, svc string) (bool, error) {
	var has bool
	err := s.root().Call(fwService+".QueryService", 0, zone, svc).Store(&has)
	return has, err
}

func (s *systemConn) ZoneAddService(zone, svc string) error {
	var i int32
	return s.zone(zone).Call(fwService+".AddService", 0, svc, true).Store(&i)
}

func (s *systemConn) Reload() error {
	var i int32
	return s.root().Call(fwService+".Reload", 0).Store(&i)
}

func (s *systemConn) PolicyExists(name string) (bool, error) {
	var exists bool
	err := s.root().Call(fwService+".QueryPolicy", 0, name).Store(&exists)
	return exists, err
}

func (s *systemConn) NewPolicy(name string) error {
	var p dbus.ObjectPath
	return s.root().Call(fwService+".NewPolicy", 0, name).Store(&p)
}

func (s *systemConn) PolicySetTarget(name, target string) error {
	var i int32
	return s.policy(name).Call(fwService+".SetTarget", 0, target, true).Store(&i)
}

func (s *systemConn) PolicySetPriority(name string, priority int32) error {
	var i int32
	return s.policy(name).Call(fwService+".SetPriority", 0, priority, true).Store(&i)
}

func (s *systemConn) PolicyAddIngressZone(name, zone string) error {
	var i int32
	return s.policy(name).Call(fwService+".AddIngressZone", 0, zone, true).Store(&i)
}

func (s *systemConn) PolicyAddEgressZone(name, zone string) error {
	var i int32
	return s.policy(name).Call(fwService+".AddEgressZone", 0, zone, true).Store(&i)
}

func (s *systemConn) PolicyAddRichRule(name, rule string) error {
	var i int32
	return s.policy(name).Call(fwService+".AddRichRule", 0, rule, true).Store(&i)
}

func (s *systemConn) PolicyRichRules(name string) ([]string, error) {
	var rules []string
	err := s.policy(name).Call(fwService+".GetRichRules", 0, true).Store(&rules)
	return rules, err
}
