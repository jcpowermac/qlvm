package ovsdbx

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

// testSchema loads the real embedded Open_vSwitch schema, doubling as a
// check that the embedded file parses.
func testSchema(t *testing.T) *ovsdb.DatabaseSchema {
	t.Helper()
	b, err := os.ReadFile("../ovs/testdata/openvswitch.ovsschema")
	if err != nil {
		t.Fatalf("read embedded schema: %v", err)
	}
	s := &ovsdb.DatabaseSchema{}
	if err := json.Unmarshal(b, s); err != nil {
		t.Fatalf("parse embedded schema: %v", err)
	}
	return s
}

// TestExpandNamedUUIDs guards the wire format against strict ovsdb-server
// parsers (OVS 3.6+): libovsdb emits the "uuid-name" op member as a bare
// string, which ovsdb-server rejects with "Type mismatch for member
// 'uuid-name'". Expand must replace every named UUID with a deterministic
// real UUID so no named-UUID construct reaches the wire.
func TestExpandNamedUUIDs(t *testing.T) {
	schema := testSchema(t)

	ops := []ovsdb.Operation{
		{
			Op:       "insert",
			Table:    "Interface",
			Row:      ovsdb.Row{"name": "br-ex", "type": "internal"},
			UUIDName: "q-if-br-ex",
		},
		{
			Op:    "insert",
			Table: "Port",
			Row: ovsdb.Row{
				"name":       "br-ex-port",
				"interfaces": ovsdb.OvsSet{GoSet: []any{"q-if-br-ex"}},
			},
			UUIDName: "q-port-br-ex-port",
		},
		{
			Op:    "insert",
			Table: "Bridge",
			Row: ovsdb.Row{
				"name":  "br-ex",
				"ports": ovsdb.OvsSet{GoSet: []any{"q-port-br-ex-port"}},
			},
			UUIDName: "q-br-br-ex",
		},
		{
			Op:    "insert",
			Table: "Open_vSwitch",
			Row: ovsdb.Row{
				"bridges": ovsdb.OvsSet{GoSet: []any{"q-br-br-ex"}},
			},
			UUIDName: "q-ovs",
		},
		{
			Op:    "mutate",
			Table: "Bridge",
			Where: []ovsdb.Condition{
				{Column: "name", Function: ovsdb.ConditionEqual, Value: "br-ex"},
			},
			Mutations: []ovsdb.Mutation{
				{
					Column:  "ports",
					Mutator: ovsdb.MutateOperationInsert,
					Value:   ovsdb.OvsSet{GoSet: []any{"q-port-br-ex-port"}},
				},
			},
		},
		{
			Op:    "insert",
			Table: "Bridge",
			Row:   ovsdb.Row{"name": "br-extra"},
		},
	}

	out, err := Expand(ops, schema)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	// Every insert op has a real UUID; named ones are deterministic.
	if out[0].UUID != Named("q-if-br-ex") || out[0].UUIDName != "" {
		t.Errorf("op 0: uuid=%q name=%q, want %q/\"", out[0].UUID, out[0].UUIDName, Named("q-if-br-ex"))
	}
	if Named("a") == Named("b") {
		t.Error("Named() collides on distinct names")
	}
	// Row references resolve to the deterministic UUIDs.
	ifs, _ := out[1].Row["interfaces"].(ovsdb.OvsSet)
	if len(ifs.GoSet) != 1 || ifs.GoSet[0] != Named("q-if-br-ex") {
		t.Errorf("port interfaces = %v, want [%s]", ifs.GoSet, Named("q-if-br-ex"))
	}
	ports, _ := out[2].Row["ports"].(ovsdb.OvsSet)
	if len(ports.GoSet) != 1 || ports.GoSet[0] != Named("q-port-br-ex-port") {
		t.Errorf("bridge ports = %v, want [%s]", ports.GoSet, Named("q-port-br-ex-port"))
	}
	bridges, _ := out[3].Row["bridges"].(ovsdb.OvsSet)
	if len(bridges.GoSet) != 1 || bridges.GoSet[0] != Named("q-br-br-ex") {
		t.Errorf("openvswitch bridges = %v, want [%s]", bridges.GoSet, Named("q-br-br-ex"))
	}
	// Mutation references resolve too.
	mutSet, _ := out[4].Mutations[0].Value.(ovsdb.OvsSet)
	if len(mutSet.GoSet) != 1 || mutSet.GoSet[0] != Named("q-port-br-ex-port") {
		t.Errorf("mutation value = %v, want [%s]", mutSet.GoSet, Named("q-port-br-ex-port"))
	}
	// Condition values are untouched.
	if out[4].Where[0].Value != "br-ex" {
		t.Errorf("condition value = %v", out[4].Where[0].Value)
	}
	// A non-named insert also gets a valid UUID.
	if err := ovsdb.ValidateUUID(out[5].UUID); err != nil {
		t.Errorf("unnamed insert uuid %q: %v", out[5].UUID, err)
	}

	// The marshalled wire format contains no named-uuid or uuid-name
	// member at all.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, bad := range []string{`"named-uuid"`, `"uuid-name"`, `q-if-`, `q-br-`, `q-port-`, `q-ovs`} {
		if strings.Contains(string(b), bad) {
			t.Errorf("wire format still contains %s: %s", bad, b)
		}
	}
}
