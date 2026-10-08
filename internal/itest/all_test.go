//go:build integration

// Package itest holds real-plane integration tests. They drive the live
// OVN/OVS/firewalld/NetworkManager planes and Xen on the dom0 they run
// on, so they are excluded from the unit build. Run them deliberately:
//
//	make test-integration
//
// TestCreateStartSSHDelete is additionally guarded: it runs only when
// QVM_ITEST=1 and QVM_ITEST_IMAGE name a bootc image reference.
package itest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"golang.org/x/crypto/ssh"

	"github.com/jcpowermac/qlvm/internal/cli"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/systemd"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// The live OVSDB endpoints on dom0 (same ones internal/ovn and internal/ovs
// dial in their NewLive constructors).
const (
	nbEndpoint  = "unix:/var/run/ovn/ovnnb_db.sock"       // OVN Northbound
	sbEndpoint  = "unix:/var/run/ovn/ovnsb_db.sock"       // OVN Southbound
	ovsEndpoint = "unix:/var/run/openvswitch/db.sock" // Open_vSwitch

	configPath = "/etc/qvm/qlvm.yaml"
	stateRoot  = "/var/lib/qvm"
)

// One minimal row model per table; libovsdb resolves the table from the
// client-model map key, and every model must carry an ovsdb:"_uuid" string.
type (
	nbSwitchRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	nbLSPortRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	nbRouterRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	nbLRPortRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	nbGatewayRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	sbPortBindingRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	ovsBridgeRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	ovsPortRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	ovsInterfaceRow struct {
		UUID string `ovsdb:"_uuid"`
	}
	nbLSPortNamedRow struct {
		UUID string `ovsdb:"_uuid"`
		Name string `ovsdb:"name"`
	}
)

func nbTables() []tableModel {
	return []tableModel{
		{"Logical_Switch", &nbSwitchRow{}},
		{"Logical_Switch_Port", &nbLSPortRow{}},
		{"Logical_Router", &nbRouterRow{}},
		{"Logical_Router_Port", &nbLRPortRow{}},
		{"Gateway", &nbGatewayRow{}},
	}
}

func sbTables() []tableModel {
	return []tableModel{{"Port_Binding", &sbPortBindingRow{}}}
}

func ovsTables() []tableModel {
	return []tableModel{
		{"Bridge", &ovsBridgeRow{}},
		{"Port", &ovsPortRow{}},
		{"Interface", &ovsInterfaceRow{}},
	}
}

type tableModel struct {
	name string
	row  any // *struct with an ovsdb:"_uuid" field
}

// countTables connects to one OVSDB endpoint and returns the row count of
// each named table.
func countTables(ctx context.Context, endpoint, db string, entries ...tableModel) (map[string]int, error) {
	models := make(map[string]model.Model, len(entries))
	for _, e := range entries {
		models[e.name] = e.row
	}
	cm, err := model.NewClientDBModel(db, models)
	if err != nil {
		return nil, err
	}
	c, err := client.NewOVSDBClient(cm, client.WithEndpoint(endpoint))
	if err != nil {
		return nil, err
	}
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return nil, err
	}
	defer c.Close()
	if _, err := c.MonitorAll(ctx); err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		slice := reflect.New(reflect.SliceOf(reflect.TypeOf(e.row).Elem())).Interface()
		if err := c.List(ctx, slice); err != nil {
			return nil, fmt.Errorf("list %s.%s: %w", db, e.name, err)
		}
		counts[e.name] = reflect.ValueOf(slice).Len()
	}
	return counts, nil
}

// planeCounts returns row counts for the tables install reconciles, keyed
// "<database>.<table>".
func planeCounts(ctx context.Context, t *testing.T) map[string]int {
	t.Helper()
	plan := []struct {
		endpoint, db string
		tables       []tableModel
	}{
		{nbEndpoint, "OVN_Northbound", nbTables()},
		{sbEndpoint, "OVN_Southbound", sbTables()},
		{ovsEndpoint, "Open_vSwitch", ovsTables()},
	}
	out := map[string]int{}
	for _, p := range plan {
		counts, err := countTables(ctx, p.endpoint, p.db, p.tables...)
		if err != nil {
			t.Fatalf("count %s: %v", p.db, err)
		}
		for table, n := range counts {
			out[p.db+"."+table] = n
		}
	}
	return out
}

// lspNames returns the names of all logical switch ports.
func lspNames(ctx context.Context, t *testing.T) []string {
	t.Helper()
	cm, err := model.NewClientDBModel("OVN_Northbound", map[string]model.Model{"Logical_Switch_Port": &nbLSPortNamedRow{}})
	if err != nil {
		t.Fatalf("client model: %v", err)
	}
	c, err := client.NewOVSDBClient(cm, client.WithEndpoint(nbEndpoint))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		c.Close()
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	if _, err := c.MonitorAll(ctx); err != nil {
		t.Fatalf("monitor: %v", err)
	}
	var rows []nbLSPortNamedRow
	if err := c.List(ctx, &rows); err != nil {
		t.Fatalf("list ports: %v", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

// TestInstallIdempotent runs the real install against the live planes
// twice and asserts the second pass changes no OVN/OVS row counts.
func TestInstallIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	root := cli.NewRootCmd()
	root.SetArgs([]string{"install"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("first install pass: %v", err)
	}
	before := planeCounts(ctx, t)

	root.SetArgs([]string{"install"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("second install pass: %v", err)
	}
	after := planeCounts(ctx, t)

	for table, b := range before {
		if a := after[table]; a != b {
			t.Errorf("second install pass is not a no-op: %s rows %d -> %d", table, b, a)
		}
	}
}

// TestCreateStartSSHDelete guards a full live lifecycle cycle behind
// QVM_ITEST=1 + QVM_ITEST_IMAGE: bake the template (or reuse the already
// baked dir), create a disposable VM referencing it, start it, wait for
// SSH, delete it, then assert no leftover logical switch port and a gone
// vmDir.
// TestSystemdUnitPropGet is a live regression guard for the D-Bus
// destination bug: Properties.Get on a unit object must target the owning
// service (org.freedesktop.systemd1); targeting the interface name instead
// makes dbus-daemon answer "The name is not activatable". An already
// active+enabled unit exercises the exact probe path with no side effects.
func TestSystemdUnitPropGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	sd, err := systemd.NewSystem()
	if err != nil {
		t.Fatalf("systemd: %v", err)
	}
	if err := sd.EnableStart(ctx, "openvswitch.service"); err != nil {
		t.Fatalf("EnableStart(openvswitch.service): %v", err)
	}
}

func TestCreateStartSSHDelete(t *testing.T) {
	if os.Getenv("QVM_ITEST") != "1" {
		t.Skip("set QVM_ITEST=1 and QVM_ITEST_IMAGE to run the live create/start/ssh/delete cycle")
	}
	image := os.Getenv("QVM_ITEST_IMAGE")
	if image == "" {
		t.Skip("QVM_ITEST_IMAGE is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	domain := cfg.Domains[0].Name

	name := "itest-" + time.Now().Format("20060102150405")
	vmDir := filepath.Join(stateRoot, "vms", name)
	root := cli.NewRootCmd()

	cleanup := func() {
		root.SetArgs([]string{"vm", "kill", name})
		_ = root.Execute() // best effort; a failed create leaves nothing to kill
		if err := os.RemoveAll(vmDir); err != nil {
			t.Logf("cleanup vmDir: %v", err)
		}
	}
	defer func() {
		if t.Failed() {
			cleanup()
		}
	}()

	// Bake first: vm create only references baked templates. On a repeat run
	// the dir already exists and `template create` refuses — fine, the baked
	// dir (discovered by slug prefix) is what vm create resolves.
	slug := template.SlugFromRef(image)
	root.SetArgs([]string{"template", "create", image})
	if err := root.ExecuteContext(ctx); err != nil {
		if _, ok := bakedDir(stateRoot, slug); !ok {
			t.Fatalf("template create: %v (and no baked dir for %s)", err, slug)
		}
	}
	tplDir, ok := bakedDir(stateRoot, slug)
	if !ok {
		t.Fatalf("no baked template dir for %s", slug)
	}
	root.SetArgs([]string{"vm", "create", name, "--domain", domain, "--type", "disposable", "--template", tplDir})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("vm create: %v", err)
	}
	meta, err := vm.LoadMeta(vmDir)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}

	root.SetArgs([]string{"vm", "start", name})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	dial := func() (*ssh.Client, error) {
		return sshx.Connect(ctx, home, meta.IP, vm.SSHUser)
	}
	if err := sshx.WaitForSSH(ctx, dial, 30, 5*time.Second); err != nil {
		t.Fatalf("wait for ssh at %s: %v", meta.IP, err)
	}

	root.SetArgs([]string{"vm", "delete", name})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Errorf("vmDir %s still exists after delete", vmDir)
	}
	for _, n := range lspNames(ctx, t) {
		if n == name {
			t.Errorf("lswitch port %q left behind after delete", n)
		}
	}
}

// bakedDir returns the full baked dir name for slug (vm create resolves a
// full name unambiguously even when several digests of the same ref exist):
// the dir name is <slug>-<digest> and the digest is only known post-pull.
// Prefers a dir holding template.raw.
func bakedDir(root, slug string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(root, "templates"))
	if err != nil {
		return "", false
	}
	prefix := slug + "-"
	var fallback string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "templates", e.Name(), "template.raw")); err == nil {
			return e.Name(), true
		}
		if fallback == "" {
			fallback = e.Name()
		}
	}
	return fallback, fallback != ""
}
