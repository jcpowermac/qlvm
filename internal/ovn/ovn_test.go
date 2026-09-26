package ovn

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

	"github.com/jcpowermac/qlvm/internal/config"
)

// fixtureConfig returns the 2-domain example config used by the tests.
func fixtureConfig() *config.Config {
	return &config.Config{
		Network: config.Network{
			NIC:      "eth0",
			Gateway:  "192.168.1.1",
			RouterIP: "192.168.1.200",
		},
		Domains: []config.Domain{
			{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"},
			{Name: "personal", Subnet: "10.100.2", Gateway: "10.100.2.1"},
		},
	}
}

// newTestEnv stands up an in-process OVN_Northbound server backed by the real
// schema from testdata, plus a connected, monitoring client.
func newTestEnv(t *testing.T) (client.Client, func()) {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", "ovn-nb.ovsschema"))
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

	sock := filepath.Join(t.TempDir(), "ovn-nb.sock")
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

func countBy[T any](t *testing.T, c client.Client) []T {
	t.Helper()
	var out []T
	require.NoError(t, c.List(context.Background(), &out))
	return out
}

func findLSP(t *testing.T, c client.Client, name string) LogicalSwitchPort {
	t.Helper()
	for _, p := range countBy[LogicalSwitchPort](t, c) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("lsp %q not found", name)
	return LogicalSwitchPort{}
}

func TestDesiredObjects(t *testing.T) {
	d := Desired(fixtureConfig())

	var switches, routers, lrps, lss, acls, nats, routes int
	for _, m := range d {
		switch m.(type) {
		case *LogicalSwitch:
			switches++
		case *LogicalRouter:
			routers++
		case *LogicalRouterPort:
			lrps++
		case *LogicalSwitchPort:
			lss++
		case *ACL:
			acls++
		case *LogicalRouterNAT:
			nats++
		case *LogicalRouterStaticRoute:
			routes++
		default:
			t.Fatalf("unexpected object type %T", m)
		}
	}
	// 2 domains: lswitch work, personal, external; lrp gw-work, gw-personal,
	// gw-external; lsp work-to-gw, personal-to-gw, ext-localnet, ext-to-gw;
	// 1 NAT, 1 static route, ACLs = 2 domains x (1 allow + 1 deny-other).
	require.Equal(t, 3, switches)
	require.Equal(t, 1, routers)
	require.Equal(t, 3, lrps)
	require.Equal(t, 4, lss)
	require.Equal(t, 1, nats)
	require.Equal(t, 1, routes)
	require.Equal(t, 4, acls)

	// spot-checks
	var gwWork *LogicalRouterPort
	var workDeny, workAllow *ACL
	var nat *LogicalRouterNAT
	var route *LogicalRouterStaticRoute
	for _, m := range d {
		switch v := m.(type) {
		case *LogicalRouterPort:
			if v.Name == "gw-work" {
				gwWork = v
			}
		case *ACL:
			if v.Name != nil && *v.Name == "acl-work-deny-personal" {
				workDeny = v
			}
			if v.Name != nil && *v.Name == "acl-work-allow" {
				workAllow = v
			}
		case *LogicalRouterNAT:
			nat = v
		case *LogicalRouterStaticRoute:
			route = v
		}
	}
	require.NotNil(t, gwWork)
	require.Equal(t, "02:00:00:ff:01:01", gwWork.MAC)
	require.Equal(t, []string{"10.100.1.1/24"}, gwWork.Networks)

	require.NotNil(t, workDeny)
	require.Equal(t, 900, workDeny.Priority)
	require.Equal(t, "from-lport", workDeny.Direction)
	require.Equal(t, "ip4.dst==10.100.2.0/24", workDeny.MatchExpression)
	require.Equal(t, "drop", workDeny.Action)
	require.NotNil(t, workAllow)
	require.Equal(t, 800, workAllow.Priority)
	require.Equal(t, "ip4", workAllow.MatchExpression)
	require.Equal(t, "allow", workAllow.Action)

	require.NotNil(t, nat)
	require.Equal(t, "192.168.1.200", nat.ExternalIP)
	require.Equal(t, "10.100.0.0/16", nat.LogicalIP)
	require.Equal(t, "snat", nat.Type)

	require.NotNil(t, route)
	require.Equal(t, "0.0.0.0/0", route.IPPrefix)
	require.Equal(t, "192.168.1.1", route.Nexthop)
}

func TestApplyIdempotent(t *testing.T) {
	c, closeFn := newTestEnv(t)
	defer closeFn()

	cfg := fixtureConfig()
	r := New(c)
	require.NoError(t, r.Apply(context.Background(), cfg))

	require.Equal(t, 3, c.Cache().Table("Logical_Switch").Len())
	require.Equal(t, 1, c.Cache().Table("Logical_Router").Len())
	require.Equal(t, 3, c.Cache().Table("Logical_Router_Port").Len())
	require.Equal(t, 4, c.Cache().Table("Logical_Switch_Port").Len())
	require.Equal(t, 4, c.Cache().Table("ACL").Len())
	require.Equal(t, 1, c.Cache().Table("NAT").Len())
	require.Equal(t, 1, c.Cache().Table("Logical_Router_Static_Route").Len())

	// second apply must be a no-op
	require.NoError(t, r.Apply(context.Background(), cfg))
	require.Equal(t, 3, c.Cache().Table("Logical_Switch").Len())
	require.Equal(t, 3, c.Cache().Table("Logical_Router_Port").Len())
	require.Equal(t, 4, c.Cache().Table("Logical_Switch_Port").Len())
	require.Equal(t, 4, c.Cache().Table("ACL").Len())
	require.Equal(t, 1, c.Cache().Table("NAT").Len())
}

func TestAddDelLSPort(t *testing.T) {
	c, closeFn := newTestEnv(t)
	defer closeFn()

	cfg := fixtureConfig()
	r := New(c)
	require.NoError(t, r.Apply(context.Background(), cfg))

	require.NoError(t, r.AddLSPort(context.Background(), "work", "vm1", "aa:bb:cc:dd:ee:01", "10.100.1.11"))
	vm1 := findLSP(t, c, "vm1")
	require.Equal(t, []string{"aa:bb:cc:dd:ee:01 10.100.1.11"}, vm1.Addresses)
	require.Equal(t, []string{"aa:bb:cc:dd:ee:01 10.100.1.11"}, vm1.PortSecurity)

	require.NoError(t, r.AddLSPort(context.Background(), "work", "vm2", "aa:bb:cc:dd:ee:02", "10.100.1.12"))

	// vm1 must be attached to the work switch
	var workSwitch LogicalSwitch
	for _, s := range countBy[LogicalSwitch](t, c) {
		if s.Name == "work" {
			workSwitch = s
		}
	}
	require.Contains(t, workSwitch.Ports, vm1.UUID)

	require.NoError(t, r.DelLSPort(context.Background(), "vm1"))
	var gone bool
	for _, p := range countBy[LogicalSwitchPort](t, c) {
		if p.Name == "vm1" {
			gone = true
		}
	}
	require.False(t, gone, "vm1 should be deleted")
}

func TestCountLSPorts(t *testing.T) {
	c, closeFn := newTestEnv(t)
	defer closeFn()

	cfg := fixtureConfig()
	r := New(c)
	require.NoError(t, r.Apply(context.Background(), cfg))

	// Apply creates work-to-gw on the work switch, which must be excluded.
	n, err := r.CountLSPorts(context.Background(), "work")
	require.NoError(t, err)
	require.Equal(t, 0, n)

	for i, ip := range []string{"10.100.1.11", "10.100.1.12", "10.100.1.13"} {
		require.NoError(t, r.AddLSPort(context.Background(), "work", "vm"+string(rune('1'+i)), "aa:bb:cc:dd:ee:0"+string(rune('0'+i)), ip))
	}
	n, err = r.CountLSPorts(context.Background(), "work")
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestApplyRepairsMissingLRP(t *testing.T) {
	c, closeFn := newTestEnv(t)
	defer closeFn()

	// Pre-insert only the personal lswitch, as if a previous run died halfway.
	ops, err := c.Create(&LogicalSwitch{Name: "personal"})
	require.NoError(t, err)
	reply, err := c.Transact(context.Background(), ops...)
	require.NoError(t, err)
	_, err = ovsdb.CheckOperationResults(reply, ops)
	require.NoError(t, err)

	require.NoError(t, New(c).Apply(context.Background(), fixtureConfig()))
	uuid := findLRPUUID(t, c, "gw-personal")
	require.NotNil(t, c.Cache().Table("Logical_Router_Port").Row(uuid))
	require.Equal(t, 3, c.Cache().Table("Logical_Router_Port").Len())
}

func findLRPUUID(t *testing.T, c client.Client, name string) string {
	t.Helper()
	for _, p := range countBy[LogicalRouterPort](t, c) {
		if p.Name == name {
			return p.UUID
		}
	}
	require.FailNowf(t, "lrp not found", "lrp %q not found", name)
	return ""
}

func TestSetGatewayChassis(t *testing.T) {
	c, closeFn := newTestEnv(t)
	defer closeFn()

	cfg := fixtureConfig()
	r := New(c)
	require.NoError(t, r.Apply(context.Background(), cfg))

	require.NoError(t, r.SetGatewayChassis(context.Background(), "gw-work", "chassis-abc"))

	chassis := countBy[HAChassis](t, c)
	require.Len(t, chassis, 1)
	require.Equal(t, "chassis-abc", chassis[0].ChassisName)

	groups := countBy[HAChassisGroup](t, c)
	require.Len(t, groups, 1)
	require.Contains(t, groups[0].HAChassis, chassis[0].UUID)

	lrp := findLogicalRouterPort(t, c, "gw-work")
	require.NotNil(t, lrp.HaChassisGroup)
	require.Equal(t, groups[0].UUID, *lrp.HaChassisGroup)
}

func findLogicalRouterPort(t *testing.T, c client.Client, name string) LogicalRouterPort {
	t.Helper()
	for _, p := range countBy[LogicalRouterPort](t, c) {
		if p.Name == name {
			return p
		}
	}
	require.FailNowf(t, "lrp not found", "lrp %q not found", name)
	return LogicalRouterPort{}
}
