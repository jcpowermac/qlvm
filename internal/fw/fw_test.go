package fw

import (
	"context"
	"strconv"
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn mimics firewalld server state so a second Ensure observes
// what the first call created.
type fakeConn struct {
	calls        []string
	zoneExists   bool
	sshAdded     bool
	policyExists bool
	rules        map[string]bool
}

func (f *fakeConn) log(name string) { f.calls = append(f.calls, name) }

func (f *fakeConn) ZoneExists(zone string) (bool, error) {
	f.log("ZoneExists:" + zone)
	return f.zoneExists, nil
}
func (f *fakeConn) NewZone(zone string) error {
	f.log("NewZone:" + zone)
	f.zoneExists = true
	return nil
}
func (f *fakeConn) ZoneHasService(zone, svc string) (bool, error) {
	f.log("ZoneHasService:" + zone + ":" + svc)
	return f.sshAdded, nil
}
func (f *fakeConn) ZoneAddService(zone, svc string) error {
	f.log("ZoneAddService:" + zone + ":" + svc)
	f.sshAdded = true
	return nil
}
func (f *fakeConn) Reload() error { f.log("Reload"); return nil }
func (f *fakeConn) PolicyExists(name string) (bool, error) {
	f.log("PolicyExists:" + name)
	return f.policyExists, nil
}
func (f *fakeConn) NewPolicy(name string) error {
	f.log("NewPolicy:" + name)
	f.policyExists = true
	return nil
}
func (f *fakeConn) PolicySetTarget(name, target string) error {
	f.log("PolicySetTarget:" + name + ":" + target)
	return nil
}
func (f *fakeConn) PolicySetPriority(name string, priority int32) error {
	f.log("PolicySetPriority:" + name + ":" + strconv.Itoa(int(priority)))
	return nil
}
func (f *fakeConn) PolicyAddIngressZone(name, zone string) error {
	f.log("PolicyAddIngressZone:" + name + ":" + zone)
	return nil
}
func (f *fakeConn) PolicyAddEgressZone(name, zone string) error {
	f.log("PolicyAddEgressZone:" + name + ":" + zone)
	return nil
}
func (f *fakeConn) PolicyAddRichRule(_, rule string) error {
	f.log("PolicyAddRichRule:" + rule)
	if f.rules == nil {
		f.rules = map[string]bool{}
	}
	f.rules[rule] = true
	return nil
}
func (f *fakeConn) PolicyRichRules(name string) ([]string, error) {
	f.log("PolicyRichRules:" + name)
	var out []string
	for r := range f.rules {
		out = append(out, r)
	}
	return out, nil
}

func testConfig() *config.Config {
	return &config.Config{
		Domains: []config.Domain{{Name: "alpha", Subnet: "10.100.1"}},
		Firewall: config.Firewall{Egress: config.Egress{
			AllowDNS:      true,
			AllowHTTPS:    true,
			AllowSSHToVMs: true,
			AllowICMP:     true,
			ExtraRules:    []string{`rule family="ipv4" port port="8080" protocol="tcp" accept`},
		}},
	}
}

func TestEgressRules(t *testing.T) {
	m := New(nil)
	got := m.EgressRules(config.Egress{
		AllowDNS:      true,
		AllowHTTPS:    true,
		AllowSSHToVMs: true,
		AllowICMP:     true,
		ExtraRules:    []string{`rule family="ipv4" port port="8080" protocol="tcp" accept`},
	}, "10.100.0.0/16")
	want := []string{
		`rule family="ipv4" port port="53" protocol="udp" accept`,
		`rule family="ipv4" port port="53" protocol="tcp" accept`,
		`rule family="ipv4" port port="443" protocol="tcp" accept`,
		`rule family="ipv4" destination address="10.100.0.0/16" port port="22" protocol="tcp" accept`,
		`rule family="ipv4" protocol value="icmp" accept`,
		`rule family="ipv4" port port="8080" protocol="tcp" accept`,
	}
	assert.Equal(t, want, got)

	noDNS := m.EgressRules(config.Egress{AllowHTTPS: true}, "10.100.0.0/16")
	assert.Equal(t, []string{
		`rule family="ipv4" port port="443" protocol="tcp" accept`,
	}, noDNS)
}

func TestEnsureIdempotent(t *testing.T) {
	f := &fakeConn{}
	m := New(f)
	require.NoError(t, m.Ensure(context.Background(), testConfig()))
	want := []string{
		"ZoneExists:dom0",
		"NewZone:dom0",
		"ZoneHasService:dom0:ssh",
		"ZoneAddService:dom0:ssh",
		"PolicyExists:dom0-egress",
		"NewPolicy:dom0-egress",
		"PolicySetTarget:dom0-egress:DROP",
		"PolicySetPriority:dom0-egress:100",
		"PolicyAddIngressZone:dom0-egress:host",
		"PolicyAddEgressZone:dom0-egress:any",
		"PolicyRichRules:dom0-egress",
		`PolicyAddRichRule:rule family="ipv4" port port="53" protocol="udp" accept`,
		`PolicyAddRichRule:rule family="ipv4" port port="53" protocol="tcp" accept`,
		`PolicyAddRichRule:rule family="ipv4" port port="443" protocol="tcp" accept`,
		`PolicyAddRichRule:rule family="ipv4" destination address="10.100.0.0/16" port port="22" protocol="tcp" accept`,
		`PolicyAddRichRule:rule family="ipv4" protocol value="icmp" accept`,
		`PolicyAddRichRule:rule family="ipv4" port port="8080" protocol="tcp" accept`,
		"Reload",
	}
	assert.Equal(t, want, f.calls)

	f.calls = nil
	require.NoError(t, m.Ensure(context.Background(), testConfig()))
	// Second Ensure: queries only, nothing created.
	assert.Equal(t, []string{
		"ZoneExists:dom0",
		"ZoneHasService:dom0:ssh",
		"PolicyExists:dom0-egress",
		"PolicyRichRules:dom0-egress",
	}, f.calls)
}
