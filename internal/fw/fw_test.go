package fw

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn mimics firewalld 2.4 config-manager state so a second Ensure
// observes what the first call created.
type fakeConn struct {
	calls    []string
	zones    map[string]string // name -> object path
	services map[string]bool   // zonePath+":"+svc
	policies map[string]string // name -> object path
	rules    map[string][]string
}

func newFake() *fakeConn {
	return &fakeConn{
		zones:    map[string]string{},
		services: map[string]bool{},
		policies: map[string]string{},
		rules:    map[string][]string{},
	}
}

func (f *fakeConn) log(name string) { f.calls = append(f.calls, name) }

func (f *fakeConn) ZoneByName(zone string) (string, error) {
	f.log("ZoneByName:" + zone)
	return f.zones[zone], nil
}
func (f *fakeConn) AddZone(zone string) (string, error) {
	f.log("AddZone:" + zone)
	f.zones[zone] = "zone:" + zone
	return f.zones[zone], nil
}
func (f *fakeConn) ZoneQueryService(zonePath, svc string) (bool, error) {
	f.log("ZoneQueryService:" + zonePath + ":" + svc)
	return f.services[zonePath+":"+svc], nil
}
func (f *fakeConn) ZoneAddService(zonePath, svc string) error {
	f.log("ZoneAddService:" + zonePath + ":" + svc)
	f.services[zonePath+":"+svc] = true
	return nil
}
func (f *fakeConn) PolicyByName(name string) (string, error) {
	f.log("PolicyByName:" + name)
	return f.policies[name], nil
}
func (f *fakeConn) AddPolicy(name, target string, priority int32, ingressZones, egressZones []string) (string, error) {
	f.log("AddPolicy:" + name + ":" + target + ":" + strconv.Itoa(int(priority)) + ":" +
		strings.Join(ingressZones, ",") + ":" + strings.Join(egressZones, ","))
	f.policies[name] = "policy:" + name
	return f.policies[name], nil
}
func (f *fakeConn) PolicyRichRules(policyPath string) ([]string, error) {
	f.log("PolicyRichRules:" + policyPath)
	return f.rules[policyPath], nil
}
func (f *fakeConn) PolicySetRichRules(policyPath string, rules []string) error {
	f.log("PolicySetRichRules:" + policyPath + ":" + strings.Join(rules, "|"))
	f.rules[policyPath] = rules
	return nil
}
func (f *fakeConn) Reload() error { f.log("Reload"); return nil }

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
		`rule family="ipv4" destination address="10.100.0.0/16" port port="4711" protocol="tcp" accept`,
		`rule family="ipv4" protocol value="icmp" accept`,
		`rule family="ipv4" port port="8080" protocol="tcp" accept`,
	}
	assert.Equal(t, want, got)

	noDNS := m.EgressRules(config.Egress{AllowHTTPS: true}, "10.100.0.0/16")
	assert.Equal(t, []string{
		`rule family="ipv4" port port="443" protocol="tcp" accept`,
		// the waypipe control rule is unconditional
		`rule family="ipv4" destination address="10.100.0.0/16" port port="4711" protocol="tcp" accept`,
	}, noDNS)
}

func TestEnsureIdempotent(t *testing.T) {
	f := newFake()
	m := New(f)
	require.NoError(t, m.Ensure(context.Background(), testConfig()))
	want := []string{
		"ZoneByName:dom0",
		"AddZone:dom0",
		"ZoneQueryService:zone:dom0:ssh",
		"ZoneAddService:zone:dom0:ssh",
		"PolicyByName:dom0-egress",
		"AddPolicy:dom0-egress:DROP:100:HOST:ANY",
		"PolicyRichRules:policy:dom0-egress",
		`PolicySetRichRules:policy:dom0-egress:rule family="ipv4" port port="53" protocol="udp" accept|rule family="ipv4" port port="53" protocol="tcp" accept|rule family="ipv4" port port="443" protocol="tcp" accept|rule family="ipv4" destination address="10.100.0.0/16" port port="22" protocol="tcp" accept|rule family="ipv4" destination address="10.100.0.0/16" port port="4711" protocol="tcp" accept|rule family="ipv4" protocol value="icmp" accept|rule family="ipv4" port port="8080" protocol="tcp" accept`,
		"Reload",
	}
	assert.Equal(t, want, f.calls)

	f.calls = nil
	require.NoError(t, m.Ensure(context.Background(), testConfig()))
	// Second Ensure: lookups only, nothing created, no reload.
	assert.Equal(t, []string{
		"ZoneByName:dom0",
		"ZoneQueryService:zone:dom0:ssh",
		"PolicyByName:dom0-egress",
		"PolicyRichRules:policy:dom0-egress",
	}, f.calls)
}

func TestNotFoundErr(t *testing.T) {
	e := dbus.Error{Name: "org.fedoraproject.FirewallD1.Exception", Body: []any{"INVALID_ZONE: dom0"}}
	require.NoError(t, notFoundErr(e, "INVALID_ZONE"))
	require.Error(t, notFoundErr(e, "INVALID_POLICY"))
	require.Error(t, notFoundErr(fmt.Errorf("boom"), "INVALID_ZONE"))
	require.NoError(t, notFoundErr(nil, "INVALID_ZONE"))
}
