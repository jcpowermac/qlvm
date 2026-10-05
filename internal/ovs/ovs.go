package ovs

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	"github.com/jcpowermac/qlvm/internal/ovsdbx"
)

// expandOps prepares ops for the live ovsdb-server: deterministic real
// UUIDs for every named UUID, no uuid-name members on the wire.
func expandOps(ops []ovsdb.Operation) ([]ovsdb.Operation, error) {
	schema, err := parseOVSSchema()
	if err != nil {
		return nil, err
	}
	return ovsdbx.Expand(ops, schema)
}

// OVS topology qlvm reconciles on the dom0: the OVN-facing Open_vSwitch
// external-ids, the br-int/br-ex bridge pair, the internal br-ex interface
// (dom0 side of br-ex, DHCP later via NM), and the physical NIC member.
//
// Naming follows the OVN external-bridge convention used by the running
// dom0: ports are named "<x>-port" and keep the bare interface names —
// the internal br-ex interface keeps the bridge name and the system
// interface keeps the NIC name.
const (
	BrInt = "br-int"
	BrEx  = "br-ex"
)

// ovnExternalIDs are the Open_vSwitch external-ids OVN needs to wire
// br-int/br-ex into the underlay.
var ovnExternalIDs = map[string]string{
	"ovn-bridge":          BrInt,
	"ovn-remote":          "unix:/run/ovn/ovnsb_db.sock",
	"ovn-encap-type":      "geneve",
	"ovn-encap-ip":        "127.0.0.1",
	"ovn-bridge-mappings": "provider:" + BrEx,
}

// OpenVSwitch is a row in the Open_vSwitch table.
type OpenVSwitch struct {
	UUID        string            `ovsdb:"_uuid"`
	Bridges     []string          `ovsdb:"bridges"`
	OtherConfig map[string]string `ovsdb:"other_config"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// Bridge is a row in the Bridge table.
type Bridge struct {
	UUID  string   `ovsdb:"_uuid"`
	Name  string   `ovsdb:"name"`
	Ports []string `ovsdb:"ports"`
}

// Port is a row in the Port table.
type Port struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Interfaces  []string          `ovsdb:"interfaces"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// Interface is a row in the Interface table.
type Interface struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Type        string            `ovsdb:"type"`
	MacInUse    *string           `ovsdb:"mac_in_use"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// Tables maps the Open_vSwitch table names qlvm manages to their models.
// It is the table set used to build the libovsdb client DB model.
func Tables() map[string]model.Model {
	return map[string]model.Model{
		"Open_vSwitch": &OpenVSwitch{},
		"Bridge":       &Bridge{},
		"Port":         &Port{},
		"Interface":    &Interface{},
	}
}

// Reconciler converges the Open_vSwitch database onto the qlvm desired
// state and manages the per-VM vif ports on br-int. The client must be
// connected and monitoring (MonitorAll) so the cache-backed lookups stay
// fresh.
type Reconciler struct {
	client client.Client
	// NetdevExists reports whether a dom0 network device exists; injected
	// for tests, defaulting to /sys/class/net lookup.
	NetdevExists func(dev string) bool
}

// New returns a Reconciler over the given Open_vSwitch client.
func New(c client.Client) *Reconciler {
	return &Reconciler{client: c, NetdevExists: sysNetdevExists}
}

// sysNetdevExists reports whether dev exists under /sys/class/net.
func sysNetdevExists(dev string) bool {
	_, err := os.Stat("/sys/class/net/" + dev)
	return err == nil
}

// desiredUUID returns the deterministic named UUID of a desired object.
// Named UUIDs let a single transaction cross-reference rows that are
// inserted in the same transaction (see ovsdb.ExpandNamedUUIDs).
// desiredUUID returns the named UUID a desired model is inserted under —
// its own UUID field, so references (written as "q-..." strings) and the
// resolve map always agree.
func desiredUUID(m model.Model) string {
	switch v := m.(type) {
	case *OpenVSwitch:
		return v.UUID
	case *Bridge:
		return v.UUID
	case *Port:
		return v.UUID
	case *Interface:
		return v.UUID
	}
	panic(fmt.Sprintf("unknown desired model type %T", m))
}

// identity identifies an existing or desired row for the name-based diff.
func identity(m model.Model) string {
	switch v := m.(type) {
	case *OpenVSwitch:
		return "ovs:"
	case *Bridge:
		return "br:" + v.Name
	case *Port:
		return "port:" + v.Name
	case *Interface:
		return "if:" + v.Name
	}
	panic(fmt.Sprintf("unknown desired model type %T", m))
}

// Desired builds the exact OVS object set for the given dom0 NIC: the
// Open_vSwitch row with the OVN external-ids, br-int, and br-ex with its
// internal br-ex port and the NIC member port.
func Desired(nic string) []model.Model {
	return []model.Model{
		&Interface{UUID: "q-if-" + BrEx, Name: BrEx, Type: "internal"},
		&Interface{UUID: "q-if-" + nic, Name: nic, Type: "system"},
		&Port{UUID: "q-port-" + BrEx, Name: BrEx + "-port", Interfaces: []string{"q-if-" + BrEx}},
		&Port{UUID: "q-port-" + nic, Name: nic + "-port", Interfaces: []string{"q-if-" + nic}},
		&Bridge{UUID: "q-br-" + BrInt, Name: BrInt},
		&Bridge{
			UUID:  "q-br-" + BrEx,
			Name:  BrEx,
			Ports: []string{"q-port-" + BrEx, "q-port-" + nic},
		},
		&OpenVSwitch{
			UUID:        "q-ovs",
			Bridges:     []string{"q-br-" + BrInt, "q-br-" + BrEx},
			ExternalIDs: ovnExternalIDs,
		},
	}
}

// Apply converges the Open_vSwitch database onto Desired(nic). It is
// idempotent: rows are diffed by name, missing rows are inserted in one
// transaction ordered so referenced rows exist before their referrers, and
// existing referrers gain references to rows created in that same
// transaction (rows inserted without a same-transaction reference are
// dropped by the database). The Open_vSwitch external-ids are merged, so
// keys set by other tools survive.
func (r *Reconciler) Apply(ctx context.Context, nic string) error {
	desired := Desired(nic)

	have, err := r.indexExisting(ctx)
	if err != nil {
		return err
	}

	// resolve maps a desired named UUID to the real UUID of the existing row,
	// or to itself when the row is created in this transaction.
	resolve := make(map[string]string, len(desired))
	var missing []model.Model
	var ops []ovsdb.Operation
	for _, m := range desired {
		uuid := desiredUUID(m)
		u, ok := have[identity(m)]
		if ok {
			resolve[uuid] = u
			if v, isOVS := m.(*OpenVSwitch); isOVS {
				updateOps, err := r.externalIDOps(u, v.ExternalIDs)
				if err != nil {
					return err
				}
				ops = append(ops, updateOps...)
			}
		} else {
			resolve[uuid] = uuid
			missing = append(missing, m)
		}
	}

	// Rewrite references in missing rows: references to rows that already
	// exist must use the real UUID; references to rows created in this
	// transaction stay named and are expanded by the server.
	for _, m := range missing {
		switch v := m.(type) {
		case *Bridge:
			rewireUUIDs(&v.Ports, resolve)
		case *Port:
			rewireUUIDs(&v.Interfaces, resolve)
		case *OpenVSwitch:
			rewireUUIDs(&v.Bridges, resolve)
		}
	}

	// Existing referrers must be updated to reference the rows created in
	// this transaction, otherwise those rows stay unreferenced and the
	// database silently drops them (referential integrity).
	attachOps, err := r.attachExistingRefs(desired, have, resolve)
	if err != nil {
		return err
	}
	ops = append(ops, attachOps...)

	for _, m := range missing {
		tableOps, err := r.client.Create(m)
		if err != nil {
			return err
		}
		ops = append(ops, tableOps...)
	}
	if len(ops) == 0 {
		return nil
	}

	send, err := expandOps(ops)
	if err != nil {
		return err
	}
	reply, err := r.client.Transact(ctx, send...)
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

// externalIDOps builds an op merging want into the current external-ids of
// the Open_vSwitch row, or none when they already match.
func (r *Reconciler) externalIDOps(uuid string, want map[string]string) ([]ovsdb.Operation, error) {
	cur, err := cacheModel[*OpenVSwitch](r.client, "Open_vSwitch", uuid)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]string, len(cur.ExternalIDs)+len(want))
	for k, v := range cur.ExternalIDs {
		merged[k] = v
	}
	changed := false
	for k, v := range want {
		if merged[k] != v {
			merged[k] = v
			changed = true
		}
	}
	if !changed {
		return nil, nil
	}
	// Update with no field args updates every non-default field of the
	// model; the merged map must live on the model (Update's field args
	// are pointers into it, resolved via ColumnByPtr).
	ovsModel := &OpenVSwitch{UUID: uuid, ExternalIDs: merged}
	return r.client.Where(ovsModel).Update(ovsModel)
}

// attachExistingRefs builds mutate-insert ops so existing bridges, ports
// and the Open_vSwitch row gain the desired references to rows created in
// the same transaction.
func (r *Reconciler) attachExistingRefs(desired []model.Model, have map[string]string, resolve map[string]string) ([]ovsdb.Operation, error) {
	var ops []ovsdb.Operation
	for _, m := range desired {
		uuid, ok := have[identity(m)]
		if !ok {
			continue
		}
		switch v := m.(type) {
		case *Bridge:
			cur, err := cacheModel[*Bridge](r.client, "Bridge", uuid)
			if err != nil {
				return nil, err
			}
			// The mutation field pointer must point into the model being
			// mutated: libovsdb resolves the column as the offset from that
			// model's base (ColumnByPtr), so a pointer into the cache copy
			// errors.
			target := &Bridge{UUID: uuid}
			ops, err = appendRefs(ops, r.client, target, &target.Ports, v.Ports, cur.Ports, resolve)
			if err != nil {
				return nil, err
			}
		case *Port:
			cur, err := cacheModel[*Port](r.client, "Port", uuid)
			if err != nil {
				return nil, err
			}
			target := &Port{UUID: uuid}
			ops, err = appendRefs(ops, r.client, target, &target.Interfaces, v.Interfaces, cur.Interfaces, resolve)
			if err != nil {
				return nil, err
			}
		case *OpenVSwitch:
			cur, err := cacheModel[*OpenVSwitch](r.client, "Open_vSwitch", uuid)
			if err != nil {
				return nil, err
			}
			target := &OpenVSwitch{UUID: uuid}
			ops, err = appendRefs(ops, r.client, target, &target.Bridges, v.Bridges, cur.Bridges, resolve)
			if err != nil {
				return nil, err
			}
		}
	}
	return ops, nil
}

// appendRefs appends a mutate-insert op adding the refs (resolved through
// resolve) that are not already present in cur.
func appendRefs(ops []ovsdb.Operation, c client.Client, m model.Model, field *[]string, want, cur []string, resolve map[string]string) ([]ovsdb.Operation, error) {
	var missing []string
	for _, w := range want {
		resolved := resolve[w]
		present := false
		for _, cu := range cur {
			if cu == resolved {
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
	mutate, err := c.Where(m).Mutate(m, model.Mutation{
		Field:   field,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   missing,
	})
	if err != nil {
		return nil, err
	}
	return append(ops, mutate...), nil
}

func rewireUUIDs(refs *[]string, resolve map[string]string) {
	for i := range *refs {
		if u, ok := resolve[(*refs)[i]]; ok {
			(*refs)[i] = u
		}
	}
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

// indexExisting maps identity -> real UUID for every row in the cache.
func (r *Reconciler) indexExisting(ctx context.Context) (map[string]string, error) {
	have := make(map[string]string)
	var ovsRows []OpenVSwitch
	if err := r.client.List(ctx, &ovsRows); err != nil {
		return nil, err
	}
	for _, v := range ovsRows {
		have[identity(&v)] = v.UUID
	}
	var bridges []Bridge
	if err := r.client.List(ctx, &bridges); err != nil {
		return nil, err
	}
	for _, v := range bridges {
		have[identity(&v)] = v.UUID
	}
	var ports []Port
	if err := r.client.List(ctx, &ports); err != nil {
		return nil, err
	}
	for _, v := range ports {
		have[identity(&v)] = v.UUID
	}
	var ifaces []Interface
	if err := r.client.List(ctx, &ifaces); err != nil {
		return nil, err
	}
	for _, v := range ifaces {
		have[identity(&v)] = v.UUID
	}
	return have, nil
}

// VifPorter is the vif-port surface the VM lifecycle needs (stale-port
// cleanup in start/delete); *Reconciler satisfies it. Task 14's vif
// hotplug consumes it too.
type VifPorter interface {
	AddVifPort(ctx context.Context, dev, ifaceID, vmUUID, mac string) error
	DelVifPort(ctx context.Context, dev string) error
	StaleVifPorts(ctx context.Context, ifaceID string) ([]string, error)
}

var _ VifPorter = (*Reconciler)(nil)

// AddVifPort adds a vif port named dev to br-int: the port carries an
// internal-type interface whose external-ids carry the OVN iface-id, the
// Xen VM UUID and the attached MAC. When a port named dev already exists
// its Interface external-ids are updated in place (delete+recreate races
// with ovs-vswitchd's still-held netdev: "could not add network device
// ... (File exists)"); a missing port is created and added to br-int.
func (r *Reconciler) AddVifPort(ctx context.Context, dev, ifaceID, vmUUID, mac string) error {
	brInt, ok := r.bridgeByName(ctx, BrInt)
	if !ok {
		return fmt.Errorf("bridge %q not found", BrInt)
	}

	extIDs := map[string]string{
		"iface-id":     ifaceID,
		"xen-vm-uuid":  vmUUID,
		"attached-mac": mac,
	}

	var ops []ovsdb.Operation
	if old, ok := r.portByName(ctx, dev); ok {
		// Re-run with the same identity is a no-op (script retries must
		// not churn the port); a changed identity mutates the Interface
		// row in place so ovs-vswitchd keeps its netdev.
		if len(old.Interfaces) != 1 {
			return fmt.Errorf("port %s has %d interfaces, want 1", dev, len(old.Interfaces))
		}
		var ifs []Interface
		if err := r.client.List(ctx, &ifs); err != nil {
			return err
		}
		for _, i := range ifs {
			if i.UUID == old.Interfaces[0] {
				if mapsEqual(i.ExternalIDs, extIDs) {
					return nil
				}
				ifaceModel := &Interface{UUID: i.UUID, ExternalIDs: extIDs}
				mutateOps, err := r.client.Where(ifaceModel).Mutate(ifaceModel, model.Mutation{
					Field:   &ifaceModel.ExternalIDs,
					Mutator: ovsdb.MutateOperationInsert,
					Value:   extIDs,
				})
				if err != nil {
					return err
				}
				ops = append(ops, mutateOps...)
			}
		}
	} else {
		// One Create per table: a single Create call targets one table, but
		// all rows go out in the same transaction so the cross-reference
		// survives (referential integrity).
		// type=system: the vif netdev already exists (libxl created it).
		// type=internal makes ovs-vswitchd try to create it and fail with
		// "could not add network device ... (File exists)".
		createOps, err := r.client.Create(&Interface{UUID: "q-if-" + dev, Name: dev, Type: "system", ExternalIDs: extIDs})
		if err != nil {
			return err
		}
		ops = append(ops, createOps...)
		portOps, err := r.client.Create(&Port{UUID: "q-port-" + dev, Name: dev, Interfaces: []string{"q-if-" + dev}})
		if err != nil {
			return err
		}
		ops = append(ops, portOps...)

		brModel := &Bridge{UUID: brInt.UUID}
		mutateOps, err := r.client.Where(brModel).Mutate(brModel, model.Mutation{
			Field:   &brModel.Ports,
			Mutator: ovsdb.MutateOperationInsert,
			Value:   []string{"q-port-" + dev},
		})
		if err != nil {
			return err
		}
		ops = append(ops, mutateOps...)
	}
	send, err := expandOps(ops)
	if err != nil {
		return err
	}
	reply, err := r.client.Transact(ctx, send...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		p, ok := r.portByName(ctx, dev)
		if !ok {
			return false
		}
		iface, err := r.interfaceOf(p)
		if err != nil {
			return false
		}
		return iface.ExternalIDs["iface-id"] == ifaceID &&
			iface.ExternalIDs["attached-mac"] == mac
	})
}

// DelVifPort removes the vif port named dev from br-int.
func (r *Reconciler) DelVifPort(ctx context.Context, dev string) error {
	port, ok := r.portByName(ctx, dev)
	if !ok {
		return fmt.Errorf("port %q not found", dev)
	}
	var ops []ovsdb.Operation
	delOps, err := r.client.Where(&Port{UUID: port.UUID}).Delete()
	if err != nil {
		return err
	}
	ops = append(ops, delOps...)

	// Detach from br-int if it still references the port.
	if brInt, ok := r.bridgeByName(ctx, BrInt); ok && slicesContain(brInt.Ports, port.UUID) {
		brModel := &Bridge{UUID: brInt.UUID}
		mutateOps, err := r.client.Where(brModel).Mutate(brModel, model.Mutation{
			Field:   &brModel.Ports,
			Mutator: ovsdb.MutateOperationDelete,
			Value:   []string{port.UUID},
		})
		if err != nil {
			return err
		}
		ops = append(ops, mutateOps...)
	}

	send, err := expandOps(ops)
	if err != nil {
		return err
	}
	reply, err := r.client.Transact(ctx, send...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		_, ok := r.portByName(ctx, dev)
		return !ok
	})
}

// DropEx tears down the br-ex underlay topology (bridge, its ports and
// interfaces, and the NIC's system port). Deleting the NIC's port makes
// ovs-vswitchd release the NIC, returning it to plain netdev status so
// networkd can DHCP it again — the migration failback path.
func (r *Reconciler) DropEx(ctx context.Context, nic string) error {
	var ops []ovsdb.Operation
	del := func(m model.Model) error {
		d, err := r.client.Where(m).Delete()
		if err != nil {
			return err
		}
		ops = append(ops, d...)
		return nil
	}
	if p, ok := r.portByName(ctx, BrEx+"-port"); ok {
		if err := del(&Port{UUID: p.UUID}); err != nil {
			return fmt.Errorf("delete port %s: %w", BrEx+"-port", err)
		}
	}
	if p, ok := r.portByName(ctx, nic+"-port"); ok {
		if err := del(&Port{UUID: p.UUID}); err != nil {
			return fmt.Errorf("delete port %s: %w", nic+"-port", err)
		}
	}
	if b, ok := r.bridgeByName(ctx, BrEx); ok {
		if err := del(&Bridge{UUID: b.UUID}); err != nil {
			return fmt.Errorf("delete bridge %s: %w", BrEx, err)
		}
	}
	var ifRows []Interface
	if err := r.client.List(ctx, &ifRows); err != nil {
		return err
	}
	for _, name := range []string{BrEx, nic} {
		for _, i := range ifRows {
			if i.Name == name {
				if err := del(&Interface{UUID: i.UUID}); err != nil {
					return fmt.Errorf("delete interface %s: %w", name, err)
				}
			}
		}
	}
	send, err := expandOps(ops)
	if err != nil {
		return err
	}
	reply, err := r.client.Transact(ctx, send...)
	if err != nil {
		return err
	}
	if _, err := ovsdb.CheckOperationResults(reply, ops); err != nil {
		return err
	}
	return r.waitForCache(ctx, func() bool {
		_, ok := r.bridgeByName(ctx, BrEx)
		return !ok
	})
}

// StaleVifPorts returns the vif* ports attached to br-int whose interface
// external-ids iface-id equals ifaceID but whose netdev no longer exists
// on the dom0. Non-vif ports are ignored.
func (r *Reconciler) StaleVifPorts(ctx context.Context, ifaceID string) ([]string, error) {
	brInt, ok := r.bridgeByName(ctx, BrInt)
	if !ok {
		return nil, fmt.Errorf("bridge %q not found", BrInt)
	}
	var stale []string
	var ports []Port
	if err := r.client.List(ctx, &ports); err != nil {
		return nil, err
	}
	byUUID := make(map[string]Port, len(ports))
	for _, p := range ports {
		byUUID[p.UUID] = p
	}
	for _, u := range brInt.Ports {
		port, ok := byUUID[u]
		if !ok || !strings.HasPrefix(port.Name, "vif") {
			continue
		}
		iface, err := r.interfaceOf(port)
		if err != nil {
			return nil, err
		}
		if iface.ExternalIDs["iface-id"] != ifaceID {
			continue
		}
		if !r.NetdevExists(port.Name) {
			stale = append(stale, port.Name)
		}
	}
	sort.Strings(stale)
	return stale, nil
}

// interfaceOf returns the first interface of port.
func (r *Reconciler) interfaceOf(port Port) (Interface, error) {
	if len(port.Interfaces) == 0 {
		return Interface{}, fmt.Errorf("port %q has no interfaces", port.Name)
	}
	iface, err := cacheModel[*Interface](r.client, "Interface", port.Interfaces[0])
	if err != nil {
		return Interface{}, err
	}
	return *iface, nil
}

// bridgeByName looks up a bridge in the cache by name.
func (r *Reconciler) bridgeByName(ctx context.Context, name string) (Bridge, bool) {
	var bridges []Bridge
	if err := r.client.List(ctx, &bridges); err != nil {
		return Bridge{}, false
	}
	for _, b := range bridges {
		if b.Name == name {
			return b, true
		}
	}
	return Bridge{}, false
}

// portByName looks up a port in the cache by name.
func (r *Reconciler) portByName(ctx context.Context, name string) (Port, bool) {
	var ports []Port
	if err := r.client.List(ctx, &ports); err != nil {
		return Port{}, false
	}
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

func slicesContain[T comparable](s []T, v T) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
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

// mapsEqual reports whether two external-ids maps carry the same
// key/value pairs.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
