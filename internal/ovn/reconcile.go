package ovn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	"github.com/jcpowermac/qlvm/internal/config"
)

// External switch and port names.
const (
	ExternalSwitch    = "external"
	ExternalLocalnet  = "ext-localnet"
	ExternalToGWPport = "ext-to-gw"
	ExternalGWPport   = "gw-external"
	RouterName        = "gateway"
	// ProviderNet is the localnet network_name bridging dom0 to the LAN.
	ProviderNet = "provider"

	gwExternalMAC = "02:00:00:ff:00:01"
)

// Reconciler converges the OVN_Northbound database onto the qlvm desired
// state. The client must be connected and monitoring (MonitorAll) so the
// cache-backed lookups stay fresh.
type Reconciler struct {
	client client.Client
}

// New returns a Reconciler over the given OVN_Northbound client.
func New(nbClient client.Client) *Reconciler {
	return &Reconciler{client: nbClient}
}

// desiredUUIDs returns the deterministic named UUID of a desired object.
// Named UUIDs let a single transaction cross-reference rows that are
// inserted in the same transaction (see ovsdb.ExpandNamedUUIDs).
func desiredUUID(m model.Model) string {
	switch v := m.(type) {
	case *LogicalSwitch:
		return "q-lsw-" + v.Name
	case *LogicalRouter:
		return "q-lr-" + v.Name
	case *LogicalRouterPort:
		return "q-lrp-" + v.Name
	case *LogicalSwitchPort:
		return "q-lsp-" + v.Name
	case *ACL:
		return "q-acl-" + *v.Name
	case *LogicalRouterNAT:
		return "q-nat-" + v.ExternalIP + "-" + v.LogicalIP
	case *LogicalRouterStaticRoute:
		return "q-route-" + v.IPPrefix
	}
	panic(fmt.Sprintf("unknown desired model type %T", m))
}

// identity identifies an existing or desired row for the name-based diff.
func identity(m model.Model) string {
	switch v := m.(type) {
	case *LogicalSwitch:
		return "lsw:" + v.Name
	case *LogicalRouter:
		return "lr:" + v.Name
	case *LogicalRouterPort:
		return "lrp:" + v.Name
	case *LogicalSwitchPort:
		return "lsp:" + v.Name
	case *ACL:
		return "acl:" + *v.Name
	case *LogicalRouterNAT:
		return "nat:" + v.ExternalIP + "/" + v.LogicalIP
	case *LogicalRouterStaticRoute:
		return "route:" + v.IPPrefix + "=>" + v.Nexthop
	}
	panic(fmt.Sprintf("unknown desired model type %T", m))
}

// Desired builds the exact OVN object set for cfg (spec §5.5): one router,
// one switch + router port + link port + ACLs per domain, the external
// localnet switch, SNAT and the default static route.
func Desired(cfg *config.Config) []model.Model {
	var out []model.Model
	var routerPorts []string

	for i, d := range cfg.Domains {
		idx := i + 1
		gwName := "gw-" + d.Name
		lsw := d.Name
		toGw := lsw + "-to-gw"

		acls := make([]string, 0, len(cfg.Domains))
		allow := &ACL{
			UUID:            "q-acl-acl-" + lsw + "-allow",
			Name:            strPtr("acl-" + lsw + "-allow"),
			Priority:        800,
			Direction:       "from-lport",
			MatchExpression: "ip4",
			Action:          "allow",
		}
		out = append(out, allow)
		acls = append(acls, desiredUUID(allow))

		for _, other := range cfg.Domains {
			if other.Name == d.Name {
				continue
			}
			deny := &ACL{
				UUID:            "q-acl-acl-" + lsw + "-deny-" + other.Name,
				Name:            strPtr("acl-" + lsw + "-deny-" + other.Name),
				Priority:        900,
				Direction:       "from-lport",
				MatchExpression: "ip4.dst==" + other.Subnet + ".0/24",
				Action:          "drop",
			}
			out = append(out, deny)
			acls = append(acls, desiredUUID(deny))
		}

		out = append(out,
			&LogicalSwitch{
				UUID:  "q-lsw-" + lsw,
				Name:  lsw,
				Ports: []string{"q-lsp-" + toGw},
				Acls:  acls,
			},
			&LogicalRouterPort{
				UUID:     "q-lrp-" + gwName,
				Name:     gwName,
				MAC:      fmt.Sprintf("02:00:00:ff:%02x:01", idx),
				Networks: []string{d.Gateway + "/24"},
			},
			&LogicalSwitchPort{
				UUID:      "q-lsp-" + toGw,
				Name:      toGw,
				Type:      "router",
				Options:   map[string]string{"router-port": gwName},
				Addresses: []string{"router"},
			},
		)
		routerPorts = append(routerPorts, "q-lrp-"+gwName)
	}

	out = append(out,
		&LogicalSwitch{
			UUID:  "q-lsw-" + ExternalSwitch,
			Name:  ExternalSwitch,
			Ports: []string{"q-lsp-" + ExternalLocalnet, "q-lsp-" + ExternalToGWPport},
		},
		&LogicalSwitchPort{
			UUID:      "q-lsp-" + ExternalLocalnet,
			Name:      ExternalLocalnet,
			Type:      "localnet",
			Options:   map[string]string{"network_name": ProviderNet},
			Addresses: []string{"unknown"},
		},
		&LogicalRouterPort{
			UUID:     "q-lrp-" + ExternalGWPport,
			Name:     ExternalGWPport,
			MAC:      gwExternalMAC,
			Networks: []string{cfg.Network.RouterIP + "/24"},
		},
		&LogicalSwitchPort{
			UUID:      "q-lsp-" + ExternalToGWPport,
			Name:      ExternalToGWPport,
			Type:      "router",
			Options:   map[string]string{"router-port": ExternalGWPport},
			Addresses: []string{"router"},
		},
		&LogicalRouter{
			UUID:         "q-lr-" + RouterName,
			Name:         RouterName,
			Ports:        append(routerPorts, "q-lrp-"+ExternalGWPport),
			StaticRoutes: []string{"q-route-0.0.0.0/0"},
			NAT:          []string{"q-nat-" + cfg.Network.RouterIP + "-" + cfg.VMSupernet()},
		},
		&LogicalRouterNAT{
			UUID:        "q-nat-" + cfg.Network.RouterIP + "-" + cfg.VMSupernet(),
			ExternalIP:  cfg.Network.RouterIP,
			ExternalMAC: strPtr(gwExternalMAC),
			LogicalIP:   cfg.VMSupernet(),
			Type:        "snat",
			Priority:    100,
		},
		&LogicalRouterStaticRoute{
			UUID:       "q-route-0.0.0.0/0",
			IPPrefix:   "0.0.0.0/0",
			Nexthop:    cfg.Network.Gateway,
			OutputPort: strPtr(ExternalGWPport),
		},
	)
	return out
}

func strPtr(s string) *string { return &s }

// Apply converges the OVN database onto Desired(cfg). It diffs by name,
// inserts the missing rows, and attaches references to them on rows that
// already exist (required: rows inserted without a same-transaction
// reference are dropped by the database). Everything goes out in a single
// transaction ordered so that referenced rows (ports, ACLs, NAT, routes)
// exist before their referrers (switches, router). It also repairs the spec
// §5.5 partial state: a domain switch that exists without its router port.
func (r *Reconciler) Apply(ctx context.Context, cfg *config.Config) error {
	desired := Desired(cfg)

	have, err := r.indexExisting(ctx)
	if err != nil {
		return err
	}

	// resolve maps a desired named UUID to the real UUID of the existing row,
	// or to itself when the row is created in this transaction.
	resolve := make(map[string]string, len(desired))
	var missing []model.Model
	for _, m := range desired {
		uuid := desiredUUID(m)
		if u, ok := have[identity(m)]; ok {
			resolve[uuid] = u
		} else {
			resolve[uuid] = uuid
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// Rewrite references in missing switches/routers: references to rows that
	// already exist must use the real UUID; references to rows created in
	// this transaction stay named and are expanded by the server.
	rewire(missing, resolve)

	// Existing switches/routers must be updated to reference the rows created
	// in this transaction, otherwise those rows stay unreferenced and the
	// database silently drops them (referential integrity).
	attachOps, err := r.attachExistingRefs(desired, have, resolve)
	if err != nil {
		return err
	}

	var ops []ovsdb.Operation
	for _, table := range []string{
		"ACL", "NAT", "Logical_Router_Static_Route",
		"Logical_Switch_Port", "Logical_Router_Port",
		"Logical_Switch", "Logical_Router",
	} {
		var rows []model.Model
		for _, m := range missing {
			if tableOf(m) == table {
				rows = append(rows, m)
			}
		}
		if len(rows) == 0 {
			continue
		}
		tableOps, err := r.client.Create(rows...)
		if err != nil {
			return err
		}
		ops = append(ops, tableOps...)
	}
	ops = append(ops, attachOps...)

	reply, err := r.client.Transact(ctx, ops...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}

	// Wait for the monitor to deliver the inserts so callers can build on
	// a fresh cache.
	return r.waitForCache(ctx, func() bool {
		have, err := r.indexExisting(ctx)
		if err != nil {
			return false
		}
		for _, m := range desired {
			if _, ok := have[identity(m)]; !ok {
				return false
			}
		}
		return true
	})
}

// rewire rewrites cross-references in missing switch/router rows.
func rewire(missing []model.Model, resolve map[string]string) {
	for _, m := range missing {
		switch v := m.(type) {
		case *LogicalSwitch:
			rewireUUIDs(&v.Ports, resolve)
			rewireUUIDs(&v.Acls, resolve)
		case *LogicalRouter:
			rewireUUIDs(&v.Ports, resolve)
			rewireUUIDs(&v.StaticRoutes, resolve)
			rewireUUIDs(&v.NAT, resolve)
		}
	}
}

func rewireUUIDs(refs *[]string, resolve map[string]string) {
	for i := range *refs {
		if u, ok := resolve[(*refs)[i]]; ok {
			(*refs)[i] = u
		}
	}
}

// attachExistingRefs builds mutate-insert ops so that switches and routers
// that already exist gain the desired references to rows created in the same
// transaction. Without those ops the new rows are unreferenced and the
// database drops them (referential integrity).
func (r *Reconciler) attachExistingRefs(desired []model.Model, have map[string]string, resolve map[string]string) ([]ovsdb.Operation, error) {
	var ops []ovsdb.Operation
	for _, m := range desired {
		uuid, ok := have[identity(m)]
		if !ok {
			continue
		}
		switch v := m.(type) {
		case *LogicalSwitch:
			cur, err := cacheModel[*LogicalSwitch](r.client, "Logical_Switch", uuid)
			if err != nil {
				return nil, err
			}
			swModel := &LogicalSwitch{UUID: uuid}
			ops, err = r.appendRefs(ops, swModel, &swModel.Ports, v.Ports, cur.Ports, resolve)
			if err != nil {
				return nil, err
			}
			ops, err = r.appendRefs(ops, swModel, &swModel.Acls, v.Acls, cur.Acls, resolve)
			if err != nil {
				return nil, err
			}
		case *LogicalRouter:
			cur, err := cacheModel[*LogicalRouter](r.client, "Logical_Router", uuid)
			if err != nil {
				return nil, err
			}
			rModel := &LogicalRouter{UUID: uuid}
			ops, err = r.appendRefs(ops, rModel, &rModel.Ports, v.Ports, cur.Ports, resolve)
			if err != nil {
				return nil, err
			}
			ops, err = r.appendRefs(ops, rModel, &rModel.StaticRoutes, v.StaticRoutes, cur.StaticRoutes, resolve)
			if err != nil {
				return nil, err
			}
			ops, err = r.appendRefs(ops, rModel, &rModel.NAT, v.NAT, cur.NAT, resolve)
			if err != nil {
				return nil, err
			}
		}
	}
	return ops, nil
}

// appendRefs appends a mutate-insert op adding the refs (resolved through
// resolve) that are not already present in cur.
func (r *Reconciler) appendRefs(ops []ovsdb.Operation, m model.Model, field *[]string, want, cur []string, resolve map[string]string) ([]ovsdb.Operation, error) {
	var missing []string
	for _, w := range want {
		resolved := resolve[w]
		present := false
		for _, c := range cur {
			if c == resolved {
				present = true
				break
			}
		}
		if !present {
			missing = append(missing, resolved)
		}
	}
	if len(missing) == 0 {
		return ops, nil
	}
	mutate, err := r.client.Where(m).Mutate(m, model.Mutation{
		Field:   field,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   missing,
	})
	if err != nil {
		return nil, err
	}
	return append(ops, mutate...), nil
}

// cacheModel fetches a row from the client cache by UUID.
func cacheModel[T any](c client.Client, table, uuid string) (T, error) {
	var zero T
	row := c.Cache().Table(table).Row(uuid)
	if row == nil {
		return zero, fmt.Errorf("row %s not found in table %s", uuid, table)
	}
	m, ok := row.(T)
	if !ok {
		return zero, fmt.Errorf("row %s in table %s is %T, not %T", uuid, table, row, zero)
	}
	return m, nil
}

func tableOf(m model.Model) string {
	switch m.(type) {
	case *LogicalSwitch:
		return "Logical_Switch"
	case *LogicalRouter:
		return "Logical_Router"
	case *LogicalRouterPort:
		return "Logical_Router_Port"
	case *LogicalSwitchPort:
		return "Logical_Switch_Port"
	case *ACL:
		return "ACL"
	case *LogicalRouterNAT:
		return "NAT"
	case *LogicalRouterStaticRoute:
		return "Logical_Router_Static_Route"
	case *HAChassisGroup:
		return "HA_Chassis_Group"
	case *HAChassis:
		return "HA_Chassis"
	}
	panic(fmt.Sprintf("unknown model type %T", m))
}

// indexExisting maps identity -> real UUID for every qlvm row in the cache.
func (r *Reconciler) indexExisting(ctx context.Context) (map[string]string, error) {
	have := make(map[string]string)
	var switches []LogicalSwitch
	if err := r.client.List(ctx, &switches); err != nil {
		return nil, err
	}
	for _, v := range switches {
		have[identity(&v)] = v.UUID
	}
	var routers []LogicalRouter
	if err := r.client.List(ctx, &routers); err != nil {
		return nil, err
	}
	for _, v := range routers {
		have[identity(&v)] = v.UUID
	}
	var lrps []LogicalRouterPort
	if err := r.client.List(ctx, &lrps); err != nil {
		return nil, err
	}
	for _, v := range lrps {
		have[identity(&v)] = v.UUID
	}
	var lss []LogicalSwitchPort
	if err := r.client.List(ctx, &lss); err != nil {
		return nil, err
	}
	for _, v := range lss {
		have[identity(&v)] = v.UUID
	}
	var acls []ACL
	if err := r.client.List(ctx, &acls); err != nil {
		return nil, err
	}
	for _, v := range acls {
		have[identity(&v)] = v.UUID
	}
	var nats []LogicalRouterNAT
	if err := r.client.List(ctx, &nats); err != nil {
		return nil, err
	}
	for _, v := range nats {
		have[identity(&v)] = v.UUID
	}
	var routes []LogicalRouterStaticRoute
	if err := r.client.List(ctx, &routes); err != nil {
		return nil, err
	}
	for _, v := range routes {
		have[identity(&v)] = v.UUID
	}
	return have, nil
}

// AddLSPort adds a VM switch port to sw with addresses and port_security
// both set to "<mac> <ip>".
func (r *Reconciler) AddLSPort(ctx context.Context, sw, name, mac, ip string) error {
	swUUID, err := r.switchUUIDByName(ctx, sw)
	if err != nil {
		return err
	}
	if _, ok := r.lspByName(ctx, name); ok {
		return fmt.Errorf("switch port %q already exists", name)
	}

	addr := mac + " " + ip
	named := "q-lsp-" + name
	port := &LogicalSwitchPort{
		UUID:         named,
		Name:         name,
		Addresses:    []string{addr},
		PortSecurity: []string{addr},
	}
	createOps, err := r.client.Create(port)
	if err != nil {
		return err
	}
	swModel := &LogicalSwitch{UUID: swUUID}
	mutateOps, err := r.client.Where(swModel).Mutate(swModel, model.Mutation{
		Field:   &swModel.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{named},
	})
	if err != nil {
		return err
	}
	reply, err := r.client.Transact(ctx, append(createOps, mutateOps...)...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, append(createOps, mutateOps...)); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		_, ok := r.lspByName(ctx, name)
		return ok
	})
}

// DelLSPort removes the switch port named name and detaches it from its
// switch.
func (r *Reconciler) DelLSPort(ctx context.Context, name string) error {
	lsp, ok := r.lspByName(ctx, name)
	if !ok {
		return fmt.Errorf("switch port %q not found", name)
	}
	var ops []ovsdb.Operation
	delOps, err := r.client.Where(&LogicalSwitchPort{UUID: lsp.UUID}).Delete()
	if err != nil {
		return err
	}
	ops = append(ops, delOps...)

	// Detach from the owning switch if it still references the port.
	var switches []LogicalSwitch
	if err := r.client.List(ctx, &switches); err != nil {
		return err
	}
	for i := range switches {
		attached := false
		for _, p := range switches[i].Ports {
			if p == lsp.UUID {
				attached = true
				break
			}
		}
		if !attached {
			continue
		}
		swModel := &LogicalSwitch{UUID: switches[i].UUID}
		mutateOps, err := r.client.Where(swModel).Mutate(swModel, model.Mutation{
			Field:   &swModel.Ports,
			Mutator: ovsdb.MutateOperationDelete,
			Value:   []string{lsp.UUID},
		})
		if err != nil {
			return err
		}
		ops = append(ops, mutateOps...)
	}

	reply, err := r.client.Transact(ctx, ops...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		_, ok := r.lspByName(ctx, name)
		return !ok
	})
}

// CountLSPorts counts VM ports attached to switch sw, excluding the
// management ports (names ending in "-to-gw").
func (r *Reconciler) CountLSPorts(ctx context.Context, sw string) (int, error) {
	swUUID, err := r.switchUUIDByName(ctx, sw)
	if err != nil {
		return 0, err
	}
	var switches []LogicalSwitch
	if err := r.client.List(ctx, &switches); err != nil {
		return 0, err
	}
	var ports []string
	for _, s := range switches {
		if s.UUID == swUUID {
			ports = s.Ports
			break
		}
	}
	count := 0
	for _, p := range ports {
		row := r.client.Cache().Table("Logical_Switch_Port").Row(p)
		if row == nil {
			continue
		}
		lsp, ok := row.(*LogicalSwitchPort)
		if !ok {
			continue
		}
		if strings.HasSuffix(lsp.Name, "-to-gw") {
			continue
		}
		count++
	}
	return count, nil
}

// SetGatewayChassis pins lrp to chassisID via an NB HA chassis group:
// one HA_Chassis row per chassis, one HA_Chassis_Group per router port,
// with the router port's ha_chassis_group pointing at the group.
func (r *Reconciler) SetGatewayChassis(ctx context.Context, lrpName, chassisID string) error {
	lrpUUID, ok := r.lrpUUIDByName(ctx, lrpName)
	if !ok {
		return fmt.Errorf("router port %q not found", lrpName)
	}
	groupName := "ha-" + lrpName

	chassisUUID, chassisExists := "", false
	var chassisList []HAChassis
	if err := r.client.List(ctx, &chassisList); err != nil {
		return err
	}
	for _, c := range chassisList {
		if c.ChassisName == chassisID {
			chassisUUID, chassisExists = c.UUID, true
			break
		}
	}
	if !chassisExists {
		chassisUUID = "q-hac-" + chassisID
	}

	groupUUID, groupExists, groupHasChassis := "", false, false
	var groups []HAChassisGroup
	if err := r.client.List(ctx, &groups); err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name != groupName {
			continue
		}
		groupUUID, groupExists = g.UUID, true
		for _, c := range g.HAChassis {
			if c == chassisUUID {
				groupHasChassis = true
			}
		}
	}
	if !groupExists {
		groupUUID = "q-hag-" + lrpName
	}

	// Everything goes out in one transaction: a row inserted without a
	// reference in the same transaction is silently dropped by the database,
	// so the chassis, the group and the router-port update must close the
	// reference loop together.
	var ops []ovsdb.Operation
	if !chassisExists {
		create, err := r.client.Create(&HAChassis{UUID: chassisUUID, ChassisName: chassisID, Priority: 1})
		if err != nil {
			return err
		}
		ops = append(ops, create...)
	}
	if !groupExists {
		create, err := r.client.Create(&HAChassisGroup{UUID: groupUUID, Name: groupName, HAChassis: []string{chassisUUID}})
		if err != nil {
			return err
		}
		ops = append(ops, create...)
	} else if !groupHasChassis {
		groupModel := &HAChassisGroup{UUID: groupUUID}
		mutate, err := r.client.Where(groupModel).Mutate(groupModel, model.Mutation{
			Field:   &groupModel.HAChassis,
			Mutator: ovsdb.MutateOperationInsert,
			Value:   []string{chassisUUID},
		})
		if err != nil {
			return err
		}
		ops = append(ops, mutate...)
	}

	lrpModel := &LogicalRouterPort{UUID: lrpUUID}
	groupRef := groupUUID
	update, err := r.client.Where(lrpModel).Update(&LogicalRouterPort{UUID: lrpUUID, HaChassisGroup: &groupRef})
	if err != nil {
		return err
	}
	ops = append(ops, update...)

	reply, err := r.client.Transact(ctx, ops...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		lrp, ok := r.lrpByName(ctx, lrpName)
		if !ok || lrp.HaChassisGroup == nil {
			return false
		}
		g, err := cacheModel[*HAChassisGroup](r.client, "HA_Chassis_Group", *lrp.HaChassisGroup)
		if err != nil {
			return false
		}
		return g.Name == groupName
	})
}

// waitForCache polls until cond holds or ctx is done.
func (r *Reconciler) waitForCache(ctx context.Context, cond func() bool) error {
	if cond() {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if cond() {
				return nil
			}
		}
	}
}

func (r *Reconciler) switchUUIDByName(ctx context.Context, name string) (string, error) {
	var switches []LogicalSwitch
	if err := r.client.List(ctx, &switches); err != nil {
		return "", err
	}
	for _, s := range switches {
		if s.Name == name {
			return s.UUID, nil
		}
	}
	return "", fmt.Errorf("logical switch %q not found", name)
}

func (r *Reconciler) lspByName(ctx context.Context, name string) (LogicalSwitchPort, bool) {
	var lss []LogicalSwitchPort
	if err := r.client.List(ctx, &lss); err != nil {
		return LogicalSwitchPort{}, false
	}
	for _, p := range lss {
		if p.Name == name {
			return p, true
		}
	}
	return LogicalSwitchPort{}, false
}

func (r *Reconciler) lrpByName(ctx context.Context, name string) (LogicalRouterPort, bool) {
	var lrps []LogicalRouterPort
	if err := r.client.List(ctx, &lrps); err != nil {
		return LogicalRouterPort{}, false
	}
	for _, p := range lrps {
		if p.Name == name {
			return p, true
		}
	}
	return LogicalRouterPort{}, false
}

func (r *Reconciler) lrpUUIDByName(ctx context.Context, name string) (string, bool) {
	lrp, ok := r.lrpByName(ctx, name)
	return lrp.UUID, ok
}
