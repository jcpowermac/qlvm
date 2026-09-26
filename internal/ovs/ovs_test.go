// Package ovs models the Open_vSwitch tables qlvm manages and reconciles
// them plus the per-VM vif ports on br-int.
package ovs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/database/inmemory"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"github.com/ovn-kubernetes/libovsdb/server"
	"github.com/stretchr/testify/require"
)

// newTestEnv stands up an in-process Open_vSwitch server backed by the real
// schema from testdata, plus a connected, monitoring client.
func newTestEnv(t *testing.T) (client.Client, func()) {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", "openvswitch.ovsschema"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	schema, err := ovsdb.SchemaFromFile(f)
	require.NoError(t, err)

	clientModel, err := model.NewClientDBModel(schema.Name, Tables())
	require.NoError(t, err)
	dbModel, errs := model.NewDatabaseModel(schema, clientModel)
	require.Empty(t, errs)

	logger := logr.Discard()
	ovsDB := inmemory.NewDatabase(map[string]model.ClientDBModel{schema.Name: clientModel}, &logger)
	srv, err := server.NewOvsdbServer(ovsDB, &logger, dbModel)
	require.NoError(t, err)

	sock := filepath.Join(t.TempDir(), "ovsdb.sock")
	go func() { _ = srv.Serve("unix", sock) }()
	require.Eventually(t, srv.Ready, 2*time.Second, 10*time.Millisecond)

	c, err := client.NewOVSDBClient(clientModel, client.WithEndpoint("unix:"+sock))
	require.NoError(t, err)
	require.NoError(t, c.Connect(context.Background()))
	_, err = c.MonitorAll(context.Background())
	require.NoError(t, err)

	return c, func() {
		c.Close()
		srv.Close()
	}
}

// list returns all rows of model type T from the client cache.
func list[T any](t *testing.T, c client.Client) []T {
	t.Helper()
	var out []T
	require.NoError(t, c.List(context.Background(), &out))
	return out
}

func findBridge(t *testing.T, c client.Client, name string) Bridge {
	t.Helper()
	for _, b := range list[Bridge](t, c) {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("bridge %q not found", name)
	return Bridge{}
}

func findPort(t *testing.T, c client.Client, name string) Port {
	t.Helper()
	for _, p := range list[Port](t, c) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("port %q not found", name)
	return Port{}
}

// ifacesOf returns the interfaces of port, in order.
func ifacesOf(t *testing.T, c client.Client, port Port) []Interface {
	t.Helper()
	var out []Interface
	for _, u := range port.Interfaces {
		row := c.Cache().Table("Interface").Row(u)
		require.NotNil(t, row, "interface %s of port %q not found", u, port.Name)
		ptr, ok := row.(*Interface)
		require.True(t, ok, "row is %T, want *Interface", row)
		out = append(out, *ptr)
	}
	return out
}

func TestApplyIdempotent(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	r := New(c)

	ctx := context.Background()
	require.NoError(t, r.Apply(ctx, "eth0"))
	require.NoError(t, r.Apply(ctx, "eth0"))

	brs := list[Bridge](t, c)
	require.Len(t, brs, 2)
	byName := map[string]Bridge{}
	for _, b := range brs {
		byName[b.Name] = b
	}
	require.Len(t, byName, 2)
	require.Empty(t, byName["br-int"].Ports)

	ports := list[Port](t, c)
	require.Len(t, ports, 2)
	portNames := []string{ports[0].Name, ports[1].Name}
	require.ElementsMatch(t, []string{"br-ex-iface", "eth0"}, portNames)
	portUUIDs := []string{}
	for _, p := range ports {
		portUUIDs = append(portUUIDs, p.UUID)
	}
	require.ElementsMatch(t, portUUIDs, byName["br-ex"].Ports)

	iface := ifacesOf(t, c, findPort(t, c, "br-ex-iface"))[0]
	require.Equal(t, "internal", iface.Type)
	require.Equal(t, "br-ex-iface", iface.Name)
	require.Empty(t, ifacesOf(t, c, findPort(t, c, "eth0"))[0].Type)

	ovss := list[OpenVSwitch](t, c)
	require.Len(t, ovss, 1)
	require.Equal(t, map[string]string{
		"ovn-bridge":          "br-int",
		"ovn-remote":          "unix:/run/ovn/ovnsb_db.sock",
		"ovn-encap-type":      "geneve",
		"ovn-encap-ip":        "127.0.0.1",
		"ovn-bridge-mappings": "provider:br-ex",
	}, ovss[0].ExternalIDs)
	require.Len(t, ovss[0].Bridges, 2)
}

func TestAddVifPortReplaces(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	r := New(c)

	ctx := context.Background()
	require.NoError(t, r.Apply(ctx, "eth0"))
	require.NoError(t, r.AddVifPort(ctx, "vif1.0", "if-a", "vm-1", "aa:bb:cc:dd:ee:01"))
	require.NoError(t, r.AddVifPort(ctx, "vif1.0", "if-b", "vm-1", "aa:bb:cc:dd:ee:02"))

	vifs := []Port{}
	for _, p := range list[Port](t, c) {
		if p.Name == "vif1.0" {
			vifs = append(vifs, p)
		}
	}
	require.Len(t, vifs, 1, "same dev added twice must yield one port")

	brInt := findBridge(t, c, "br-int")
	require.ElementsMatch(t, []string{vifs[0].UUID}, brInt.Ports)

	extIDs := ifacesOf(t, c, vifs[0])[0].ExternalIDs
	require.Equal(t, map[string]string{
		"iface-id":     "if-b",
		"xen-vm-uuid":  "vm-1",
		"attached-mac": "aa:bb:cc:dd:ee:02",
	}, extIDs)
}

func TestDelVifPort(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	r := New(c)

	ctx := context.Background()
	require.NoError(t, r.Apply(ctx, "eth0"))
	require.NoError(t, r.AddVifPort(ctx, "vif1.0", "if-a", "vm-1", "aa:bb:cc:dd:ee:01"))
	require.NoError(t, r.DelVifPort(ctx, "vif1.0"))

	found := false
	for _, p := range list[Port](t, c) {
		if p.Name == "vif1.0" {
			found = true
		}
	}
	require.False(t, found, "port vif1.0 must be gone")
	require.Empty(t, findBridge(t, c, "br-int").Ports)
}

func TestStaleVifPorts(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	exists := map[string]bool{"vif1.0": true} // vif3.0 and other0 are gone
	r := New(c)
	r.NetdevExists = func(dev string) bool { return exists[dev] }

	ctx := context.Background()
	require.NoError(t, r.Apply(ctx, "eth0"))
	require.NoError(t, r.AddVifPort(ctx, "vif1.0", "if-a", "vm-1", "aa:bb:cc:dd:ee:01"))
	require.NoError(t, r.AddVifPort(ctx, "vif3.0", "if-a", "vm-3", "aa:bb:cc:dd:ee:03"))
	require.NoError(t, r.AddVifPort(ctx, "other0", "if-a", "vm-9", "aa:bb:cc:dd:ee:09"))

	stale, err := r.StaleVifPorts(ctx, "if-a")
	require.NoError(t, err)
	require.Equal(t, []string{"vif3.0"}, stale, "only missing vif* ports with the iface-id are stale")

	// A different iface-id matches no port.
	stale, err = r.StaleVifPorts(ctx, "if-none")
	require.NoError(t, err)
	require.Empty(t, stale)
}

func TestStaleVifPortsDefaultNetdevExists(t *testing.T) {
	r := New(nil)
	require.False(t, r.NetdevExists("definitely-not-a-device-0.0"))
	require.True(t, r.NetdevExists("lo"))
}

// seedOVSRow inserts an Open_vSwitch row directly, simulating the row a
// real dom0 already has (created by ovs-vsctl / other tooling) before
// qlvm's first Apply.
func seedOVSRow(t *testing.T, c client.Client, extIDs map[string]string) {
	t.Helper()
	ops, err := c.Create(&OpenVSwitch{ExternalIDs: extIDs})
	require.NoError(t, err)
	reply, err := c.Transact(context.Background(), ops...)
	require.NoError(t, err)
	_, err = ovsdb.CheckOperationResults(reply, ops)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return len(list[OpenVSwitch](t, c)) == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestApplyConvergesPreexistingOVS covers the first Apply on a real dom0:
// the Open_vSwitch row already exists with foreign external-ids keys and
// no bridges. Apply must merge the OVN external-ids (CRITICAL 2: the
// update op) and attach the new bridges to the existing row (CRITICAL 1:
// the mutate field must resolve against the mutated model).
func TestApplyConvergesPreexistingOVS(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	r := New(c)

	seedOVSRow(t, c, map[string]string{"hostname": "dom0"})
	require.NoError(t, r.Apply(context.Background(), "eth0"))

	ovss := list[OpenVSwitch](t, c)
	require.Len(t, ovss, 1)
	require.Equal(t, map[string]string{
		"hostname":            "dom0",
		"ovn-bridge":          "br-int",
		"ovn-remote":          "unix:/run/ovn/ovnsb_db.sock",
		"ovn-encap-type":      "geneve",
		"ovn-encap-ip":        "127.0.0.1",
		"ovn-bridge-mappings": "provider:br-ex",
	}, ovss[0].ExternalIDs)

	want := []string{findBridge(t, c, "br-int").UUID, findBridge(t, c, "br-ex").UUID}
	require.ElementsMatch(t, want, ovss[0].Bridges)
}

// TestApplyChangesNIC covers a NIC change after Apply: the new port is
// created and must be attached to the existing br-ex (CRITICAL 1 mutate
// path). Apply is additive by design; migrating away from the old NIC port
// is the install CLI's job, not the reconciler's.
func TestApplyChangesNIC(t *testing.T) {
	c, teardown := newTestEnv(t)
	defer teardown()
	r := New(c)

	ctx := context.Background()
	require.NoError(t, r.Apply(ctx, "eth0"))
	require.NoError(t, r.Apply(ctx, "eth1"))

	brEx := findBridge(t, c, "br-ex")
	var ports []Port
	require.NoError(t, c.List(ctx, &ports))
	byUUID := map[string]Port{}
	for _, p := range ports {
		byUUID[p.UUID] = p
	}
	portNames := []string{}
	for _, u := range brEx.Ports {
		portNames = append(portNames, byUUID[u].Name)
	}
	require.Subset(t, portNames, []string{"br-ex-iface", "eth1"})

	require.Equal(t, "eth1", ifacesOf(t, c, findPort(t, c, "eth1"))[0].Name)
}
