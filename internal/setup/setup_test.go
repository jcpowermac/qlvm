package setup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/fw"
	"github.com/jcpowermac/qlvm/internal/nm"
	"github.com/jcpowermac/qlvm/internal/systemd"
)

// fixture is the 1-domain example config the install tests run against
// (example IP space only, per repo convention).
func fixture() *config.Config {
	return &config.Config{
		Network: config.Network{
			NIC:           "enp3s0",
			NICConnection: "Wired connection 1",
			Gateway:       "192.168.1.1",
			RouterIP:      "192.168.1.200",
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
	r *rec
	n int
}

func (f *fakeOVS) Apply(_ context.Context, _ string) error {
	f.r.add("ovs:apply")
	f.n++
	return nil
}

// fakeFW behaves like a converged firewalld: zone/policy exist, ssh
// service present; rich rules start empty so run 1 sets them.
type fakeFW struct {
	r     *rec
	mut   int
	rules []string
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

// fakeNM behaves like the live dom0: all five OVS con-names already
// exist; the NIC enslavement state flips after the migrate deactivation.
type fakeNM struct {
	r        *rec
	mut      int
	enslaved bool
	existing []string
	zones    map[string]string
}

func (f *fakeNM) ConNames() ([]string, error) {
	f.r.add("nm:ConNames")
	return f.existing, nil
}
func (f *fakeNM) AddConnection(spec map[string]map[string]any) error {
	id, _ := spec["connection"]["id"].(string)
	f.r.add("nm:AddConnection:" + id)
	f.mut++
	f.existing = append(f.existing, id)
	return nil
}
func (f *fakeNM) SetConnectionValue(conName, key, value string) error {
	f.r.add("nm:zone:" + conName)
	f.mut++
	if key == "zone" {
		if f.zones == nil {
			f.zones = map[string]string{}
		}
		f.zones[conName] = value
	}
	return nil
}
func (f *fakeNM) ConnZone(conName string) (string, error) {
	f.r.add("nm:ConnZone:" + conName)
	return f.zones[conName], nil
}
func (f *fakeNM) Activate(_, _ string) error {
	f.r.add("nm:Activate")
	f.mut++
	return nil
}
func (f *fakeNM) Deactivate(string) error {
	f.r.add("nm:Deactivate")
	f.mut++
	f.enslaved = true
	return nil
}

type fakeSD struct {
	r   *rec
	mut int
}

func (f *fakeSD) UnitActive(unit string) (string, error) {
	f.r.add("sd:" + unit)
	return "active", nil
}
func (f *fakeSD) UnitFileState(string) (string, error) { return "enabled", nil }
func (f *fakeSD) StartUnit(string) error {
	f.r.add("sd:StartUnit")
	f.mut++
	return nil
}
func (f *fakeSD) EnableUnit(string) error {
	f.r.add("sd:EnableUnit")
	f.mut++
	return nil
}

// testPlan wires every plane to a recording fake and returns the
// mutation counters the assertions read.
func testPlan(r *rec) (p *Plan, ovsApply, ovnApply, fwMut, nmMut, sdMut *int) {
	ovsFake := &fakeOVS{r: r}
	ovnFake := &fakeOVN{r: r}
	fwFake := &fakeFW{r: r}
	nmFake := &fakeNM{r: r, existing: []string{"br-ex", "br-ex-port", "br-ex-iface", "enp3s0-port", "enp3s0-ovs"}}
	sdFake := &fakeSD{r: r}
	p = &Plan{
		OVN:       ovnFake,
		OVS:       ovsFake,
		FW:        fw.New(fwFake),
		NM:        nm.New(nmFake),
		SD:        systemd.New(sdFake),
		Storage:   func() error { r.add("storage"); return nil },
		VifScript: func() error { r.add("vif"); return nil },
		SaveCfg:   func() error { r.add("save"); return nil },
		Dom0Check: func() (bool, error) { return true, nil },
		Out:       io.Discard,
	}
	p.NM.NICEnslaved = func(string) bool { return nmFake.enslaved }
	return p, &ovsFake.n, &ovnFake.n, &fwFake.mut, &nmFake.mut, &sdFake.mut
}

// TestInstallTwiceIsNoOp is the idempotency pin: a second Run makes no
// mutation calls on any plane.
func TestInstallTwiceIsNoOp(t *testing.T) {
	r := &rec{}
	p, ovsApply, ovnApply, fwMut, nmMut, sdMut := testPlan(r)
	cfg := fixture()

	require.NoError(t, Run(context.Background(), p, cfg, Options{}))
	// sanity: run 1 actually did work
	assert.Equal(t, 1, *ovsApply)
	assert.Equal(t, 1, *ovnApply)
	assert.Greater(t, *fwMut, 0)
	assert.Greater(t, *nmMut, 0)
	storageRun1, vifRun1, saveRun1 := count(r, "storage"), count(r, "vif"), count(r, "save")
	require.Equal(t, 1, storageRun1)

	ovsRun1, ovnRun1, fwRun1, nmRun1, sdRun1 := *ovsApply, *ovnApply, *fwMut, *nmMut, *sdMut
	r.seq = nil
	require.NoError(t, Run(context.Background(), p, cfg, Options{}))

	// Ruling R5 (ratified 2026-09-26): run 2 must make zero MUTATION calls;
	// one Apply call per run is expected because Run is stateless and Apply
	// is the diff mechanism (zero mutations when converged is the
	// reconcilers' job).
	assert.Equal(t, 1, *ovsApply-ovsRun1, "run 2 made OVS Apply calls: %v", r.seq)
	assert.Equal(t, 1, *ovnApply-ovnRun1, "run 2 made OVN Apply calls: %v", r.seq)
	assert.Equal(t, 0, *fwMut-fwRun1, "run 2 made firewalld mutation calls: %v", r.seq)
	assert.Equal(t, 0, *nmMut-nmRun1, "run 2 made NM mutation calls: %v", r.seq)
	assert.Equal(t, 0, *sdMut-sdRun1, "run 2 made systemd mutation calls: %v", r.seq)
	// func planes are self-idempotent: no more calls in run 2 than run 1.
	assert.LessOrEqual(t, count(r, "storage"), storageRun1)
	assert.LessOrEqual(t, count(r, "vif"), vifRun1)
	assert.LessOrEqual(t, count(r, "save"), saveRun1)
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
	p, _, _, _, _, _ := testPlan(r)
	p.Dom0Check = func() (bool, error) { return false, nil }

	err := Run(context.Background(), p, fixture(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dom0")
	assert.Empty(t, r.seq)
}

// TestNICMigrationSSHGuard (Review Focus 4): inside an SSH session the
// install refuses before touching OVS/NM unless --skip-nic-migration is set.
func TestNICMigrationSSHGuard(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.168.1.5 51000 10.100.1.10 22")

	r := &rec{}
	p, ovsApply, _, _, nmMut, _ := testPlan(r)
	err := Run(context.Background(), p, fixture(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-nic-migration")
	assert.Equal(t, 0, *ovsApply)
	assert.Equal(t, 0, *nmMut)

	r2 := &rec{}
	p2, ovsApply2, _, _, nmMut2, _ := testPlan(r2)
	require.NoError(t, Run(context.Background(), p2, fixture(), Options{SkipNICMigration: true}))
	assert.Equal(t, 1, *ovsApply2)
	assert.Greater(t, *nmMut2, 0)
}

// TestInstallStepOrder pins the spec §5 order: dom0 → storage → services →
// OVS → OVN → firewall → vif → config save (vif before config save).
func TestInstallStepOrder(t *testing.T) {
	r := &rec{}
	p, _, _, _, _, _ := testPlan(r)
	require.NoError(t, Run(context.Background(), p, fixture(), Options{}))
	assert.Equal(t, []string{
		"storage",
		"sd:openvswitch.service",
		"sd:ovn-northd.service",
		"sd:ovn-controller.service",
		"ovs:apply",
		"nm:ConNames",
		"nm:Activate",
		"nm:Deactivate",
		"ovn:apply",
		"fw:PolicyRichRules:read",
		"fw:PolicySetRichRules",
		"fw:Reload",
		"nm:ConnZone:br-ex-iface",
		"nm:zone:br-ex-iface",
		"nm:ConnZone:enp3s0-ovs",
		"nm:zone:enp3s0-ovs",
		"vif",
		"save",
	}, r.seq)
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
