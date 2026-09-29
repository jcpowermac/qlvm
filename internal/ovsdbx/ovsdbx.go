// Package ovsdbx prepares libovsdb operations for strict ovsdb-server
// parsers (OVS 3.6+) before a transaction is sent.
//
// libovsdb emits OVSDB named UUIDs in a form ovsdb-server rejects: the
// insert op's "uuid-name" member as a bare string
// ("Type mismatch for member 'uuid-name'"). Expand assigns every insert
// op a real UUID — named ones mapped deterministically to a UUIDv5, so
// repeated runs converge on the same rows — and then lets libovsdb's
// ExpandNamedUUIDs resolve every named-UUID reference in rows, mutations
// and conditions to those real UUIDs and clear the uuid-name members.
package ovsdbx

import (
	"github.com/google/uuid"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

// qlvmNamespace is the fixed namespace for mapping qlvm named UUIDs to
// deterministic UUIDv5 values.
var qlvmNamespace = uuid.MustParse("f0e1d2c3-b4a5-4968-9708-1a2b3c4d5e6f")

// Named returns the deterministic UUID assigned to a named UUID.
func Named(name string) string {
	return uuid.NewSHA1(qlvmNamespace, []byte(name)).String()
}

// Expand returns ops with all named-UUID usage replaced by real UUID
// values. schema is the database schema of the target (used by
// ExpandNamedUUIDs to type the reference columns). Ops are rewritten in
// place and returned.
func Expand(ops []ovsdb.Operation, schema *ovsdb.DatabaseSchema) ([]ovsdb.Operation, error) {
	for i := range ops {
		op := &ops[i]
		if op.Op != ovsdb.OperationInsert || op.UUID != "" {
			continue
		}
		if op.UUIDName != "" {
			op.UUID = Named(op.UUIDName)
		} else {
			op.UUID = uuid.New().String()
		}
	}
	return ovsdb.ExpandNamedUUIDs(ops, schema)
}
