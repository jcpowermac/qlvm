package setup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/fw"
	"github.com/jcpowermac/qlvm/internal/netd"
	"github.com/jcpowermac/qlvm/internal/systemd"
)

// fixture is the 1-domain example config the install tests run against
// (example IP space only, per repo convention).
func fixture() *config.Config {
	return &config.Config{
		Network: config.Network{
			NIC:     "enp3s0",
			Gateway: "192.168.1.1",
			RouterIP: "192.168.1.200",
		},
		Domains: []config.Domain{
			{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"},
		},
		Firewall: config.Firewall{
			Egress: config.Egress{AllowDNS: true},
		},
	}
}

// rec records the sequence of plane calls so tests can pin step ordering.
type rec struct {
	seq []string
}

func (r *rec) add(s string) { r.seq = append(r.seq, s) }

type fakeOVN struct {
	r *rec
	n int
}

func (f *fakeOVN) Apply(_ context.Context, _ *config.Config) error {
	f.r.add("ovn:apply")
	f.n++
	return nil
}

type fakeOVS struct {
	r        *rec
	n        int
	dropN    int
	bridgeUp *bool
	// noLease keeps the bridge IP-less so the migration's failback path
	// is exercised.
	noLease bool
}

func (f *fakeOVS) Apply(_ context.Context, _ string) error {
	f.r.add("ovs:apply")
	f.n++
	if !f.noLease {
		*f.bridgeUp = true // ovs-vswitchd enslaved the NIC; the bridge leases
	}
	return nil
}

func (f *fakeOVS) DropEx(_ context.Context, _ string) error {
	f.r.add("ovs:dropex")
	f.dropN++
	*f.bridgeUp = false
	return nil
}

// fakeFW behaves like a converged firewalld: zone/policy exist, ssh
// service present; rich rules and zone interfaces start empty so run 1
// sets them.
type fakeFW struct {
	r       *rec
	mut     int
	rules   []string
	ifaces  map[string]bool
}

func (f *fakeFW) ZoneByName(string) (string, error) { return "/org/zone/dom0", nil }
func (f *fakeFW) AddZone(string) (string, error) {
	f.r.add("fw:AddZone")
	f.mut++
	return "/org/zone/dom0", nil
}
func (f *fakeFW) ZoneQueryService(string, string) (bool, error) { return true, nil }
func (f *fakeFW) ZoneAddService(string, string) error {
	f.r.add("fw:ZoneAddService")
	f.mut++
	return nil
}
func (f *fakeFW) ZoneQueryInterface(_ string, dev string) (bool, error) {
	f.r.add("fw:ZoneQueryInterface:" + dev)
	return f.ifaces[dev], nil
}
func (f *fakeFW) ZoneAddInterface(_ string, dev string) error {
	f.r.add("fw:ZoneAddInterface:" + dev)
	f.mut++
	if f.ifaces == nil {
		f.ifaces = map[string]bool{}
	}
	f.ifaces[dev] = true
	return nil
}
func (f *fakeFW) PolicyByName(string) (string, error) { return "/org/policy/dom0-egress", nil }
func (f *fakeFW) AddPolicy(string, string, int32, []string, []string) (string, error) {
	f.r.add("fw:AddPolicy")
	f.mut++
	return "/org/policy/dom0-egress", nil
}
func (f *fakeFW) PolicyRichRules(string) ([]string, error) {
	f.r.add("fw:PolicyRichRules:read")
	return f.rules, nil
}
func (f *fakeFW) PolicySetRichRules(_ string, rules []string) error {
	f.r.add("fw:PolicySetRichRules")
	f.mut++
	f.rules = rules
	return nil
}
func (f *fakeFW) Reload() error {
	f.r.add("fw:Reload")
	f.mut++
	return nil
}

type fakeSD struct {
	r   *rec
	mut int
	// masked tracks UnitFileState for units masked during the run.
	masked map[string]bool
	// disabled reports UnitFileState "disabled" for listed units (the
	// distro preset state of systemd-networkd).
	disabled map[string]bool
}

func (f *fakeSD) UnitActive(unit string) (string, error) {
	f.r.add("sd:" + unit)
	return "active", nil
}
func (f *fakeSD) UnitFileState(unit string) (string, error) {
	if f.masked[unit] {
		return "masked", nil
	}
	if f.disabled[unit] {
		return "disabled", nil
	}
	return "enabled", nil
}
func (f *fakeSD) StartUnit(string) error {
	f.r.add("sd:StartUnit")
	f.mut++
	return nil
}
func (f *fakeSD) StopUnit(unit string) error {
	f.r.add("sd:stop:" + unit)
	f.mut++
	return nil
}
func (f *fakeSD) EnableUnit(string) error {
	f.r.add("sd:EnableUnit")
	f.mut++
	return nil
}
func (f *fakeSD) MaskUnit(unit string) error {
	f.r.add("sd:mask:" + unit)
	f.mut++
	if f.masked == nil {
		f.masked = map[string]bool{}
	}
	f.masked[unit] = true
	return nil
}

// wiring bundles the recording fakes and the counters assertions read.
type wiring struct {
	p    *Plan
	ovs  *fakeOVS
	fw   *fakeFW
	sd   *fakeSD
	dropEx int
}

// testPlan wires every plane to a recording fake. The NIC starts holding
// its lease (networkd converged); the bridge gains its lease when OVS
// Apply runs, unless the OVS fake is told the bridge never leases.
func testPlan(t *testing.T, r *rec, noLease bool) *wiring {
	var bridgeUp bool
	nicUp := true
	ovsFake := &fakeOVS{r: r, bridgeUp: &bridgeUp, noLease: noLease}
	ovnFake := &fakeOVN{r: r}
	fwFake := &fakeFW{r: r}
	sdFake := &fakeSD{r: r}
	netdMgr := &netd.Manager{
		Dir: t.TempDir(),
		Reload: func() error {
			r.add("netd:reload")
			return nil
		},
		UplinkOK: func(dev string) bool {
			if dev == "br-ex" {
				return bridgeUp
			}
			return nicUp
		},
	}
	p := &Plan{
		OVN:       ovnFake,
		OVS:       ovsFake,
		FW:        fw.New(fwFake),
		Netd:      netdMgr,
		SD:        systemd.New(sdFake),
		Storage:   func() error { r.add("storage"); return nil },
		VifScript: func() error { r.add("vif"); return nil },
		SaveCfg:   func() error { r.add("save"); return nil },
		Dom0Check: func() (bool, error) { return true, nil },
		Out:       io.Discard,
	}
	return &wiring{p: p, ovs: ovsFake, fw: fwFake, sd: sdFake}
}

// TestInstallTwiceIsNoOp is the idempotency pin: a second Run makes no
// mutation calls on any plane.
func TestInstallTwiceIsNoOp(t *testing.T) {
	r := &rec{}
	w := testPlan(t, r, false)
	cfg := fixture()

	require.NoError(t, Run(context.Background(), w.p, cfg, Options{}))
	// sanity: run 1 actually did work
	assert.Equal(t, 1, w.ovs.n)
	assert.Greater(t, w.fw.mut, 0)

	ovsRun1, fwRun1, sdRun1 := w.ovs.n, w.fw.mut, w.sd.mut
	r.seq = nil
	require.NoError(t, Run(context.Background(), w.p, cfg, Options{}))

	// Ruling R5 (ratified 2026-09-26): run 2 must make zero MUTATION calls;
	// one Apply call per run is expected because Run is stateless and Apply
	// is the diff mechanism (zero mutations when converged is the
	// reconcilers' job).
	assert.Equal(t, 1, w.ovs.n-ovsRun1, "run 2 made OVS Apply calls: %v", r.seq)
	assert.Zero(t, w.dropEx, "run 2 dropped the OVS topology: %v", r.seq)
	assert.Equal(t, 0, w.fw.mut-fwRun1, "run 2 made firewalld mutation calls: %v", r.seq)
	assert.Equal(t, 0, w.sd.mut-sdRun1, "run 2 made systemd mutation calls: %v", r.seq)
	assert.Zero(t, count(r, "netd:reload"), "run 2 reloaded networkd: %v", r.seq)
}

// TestOVSStepFailsBackWhenBridgeHasNoIP: the bridge never leases, so the
// migration must drop the OVS topology (NIC back on its own lease) and
// report the failure.
func TestOVSStepFailsBackWhenBridgeHasNoIP(t *testing.T) {
	old := netd.UplinkTimeout
	netd.UplinkTimeout = 300 * time.Millisecond
	t.Cleanup(func() { netd.UplinkTimeout = old })

	r := &rec{}
	w := testPlan(t, r, true)
	err := Run(context.Background(), w.p, fixture(), Options{SkipNICMigration: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failback")
	assert.Equal(t, 1, w.ovs.dropN)
}

func count(r *rec, s string) int {
	n := 0
	for _, e := range r.seq {
		if e == s {
			n++
		}
	}
	return n
}

func TestInstallRefusesWithoutDom0(t *testing.T) {
	r := &rec{}
	w := testPlan(t, r, false)
	w.p.Dom0Check = func() (bool, error) { return false, nil }

	err := Run(context.Background(), w.p, fixture(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dom0")
	assert.Empty(t, r.seq)
}

// TestNICMigrationSSHGuard (Review Focus 4): inside an SSH session the
// install refuses before touching any plane unless --skip-nic-migration
// is set.
func TestNICMigrationSSHGuard(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.168.1.5 51000 10.100.1.10 22")

	r := &rec{}
	w := testPlan(t, r, false)
	err := Run(context.Background(), w.p, fixture(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-nic-migration")
	assert.Equal(t, 0, w.ovs.n)
	// The refusal happens at the start of the ovs step: no networkd
	// enable/start, no drop-in reload, no NM mask, no OVS mutation.
	assert.Zero(t, count(r, "sd:systemd-networkd.service"))
	assert.Zero(t, count(r, "netd:reload"))
	assert.Zero(t, count(r, "sd:mask:NetworkManager.service"))
	assert.Zero(t, count(r, "ovs:apply"))

	r2 := &rec{}
	w2 := testPlan(t, r2, false)
	require.NoError(t, Run(context.Background(), w2.p, fixture(), Options{SkipNICMigration: true}))
	assert.Equal(t, 1, w2.ovs.n)
}

// TestInstallStepOrder pins the spec §5 order: dom0 → storage → services →
// OVS → OVN → firewall → vif → config save (vif before config save).
func TestInstallStepOrder(t *testing.T) {
	r := &rec{}
	w := testPlan(t, r, false)
	require.NoError(t, Run(context.Background(), w.p, fixture(), Options{}))
	assert.Equal(t, []string{
		"storage",
		"sd:openvswitch.service",
		"sd:ovn-northd.service",
		"sd:ovn-controller.service",
		"sd:systemd-networkd.service",
		"netd:reload",
		"sd:stop:NetworkManager.service",
		"sd:mask:NetworkManager.service",
		"ovs:apply",
		"ovn:apply",
		"fw:PolicyRichRules:read",
		"fw:PolicySetRichRules",
		"fw:PolicyRichRules:read",
		"fw:PolicySetRichRules",
		"fw:Reload",
		"fw:ZoneQueryInterface:br-ex",
		"fw:ZoneAddInterface:br-ex",
		"fw:ZoneQueryInterface:enp3s0",
		"fw:ZoneAddInterface:enp3s0",
		"fw:Reload",
		"vif",
		"save",
	}, r.seq)
}

// TestInstallEnablesNetworkd (regression): the distro preset leaves
// systemd-networkd disabled and the old install only ever started it via
// the drop-in reload, so the first reboot after install left the dom0
// without an uplink. Install must enable the unit when it finds it
// disabled.
func TestInstallEnablesNetworkd(t *testing.T) {
	r := &rec{}
	w := testPlan(t, r, false)
	w.sd.disabled = map[string]bool{"systemd-networkd.service": true}
	require.NoError(t, Run(context.Background(), w.p, fixture(), Options{}))
	assert.Equal(t, 1, count(r, "sd:EnableUnit"), "install did not enable systemd-networkd: %v", r.seq)
}

func TestIsDom0(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capabilities")
	old := dom0CapPath
	dom0CapPath = path
	t.Cleanup(func() { dom0CapPath = old })

	ok, err := IsDom0()
	require.Error(t, err)
	assert.False(t, ok)

	require.NoError(t, os.WriteFile(path, []byte("x86_64 control_d\n"), 0o600))
	ok, err = IsDom0()
	require.NoError(t, err)
	assert.True(t, ok)
}
