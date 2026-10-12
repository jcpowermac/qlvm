# OVSDB (libovsdb) Pitfalls

Unit fakes and the in-memory ovsdb server cannot catch these; each cost a
live round-trip. **General rule: every D-Bus/OVSDB method call must be
verified against the live service (busctl introspect / a scratch object),
never against memory or older docs. Fake `Conn` interfaces test Manager
logic, not wire shapes.**

- The dom0 ovsdb-servers expose **unix sockets only**:
  `unix:/var/run/ovn/ovnnb_db.sock`, `unix:/var/run/openvswitch/db.sock`
  (no TCP). Endpoints overridable via `QVM_OVN_ENDPOINT`/`QVM_OVS_ENDPOINT`.
- Strict ovsdb-server (OVS 3.6.2) **rejects** the `uuid-name` op member
  ("Type mismatch for member 'uuid-name'"). All ops must go through
  `internal/ovsdbx.Expand`: fill a deterministic UUIDv5
  (`ovsdbx.Named`) into every insert's `op.UUID` first (named models arrive
  with `op.UUID == ""`), then call `ovsdb.ExpandNamedUUIDs(ops, schema)`,
  which resolves all `OvsSet`/`[]string`/`[]UUID` references and clears the
  `uuid-name` members. Schema: parse the embedded
  `internal/{ovn,ovs}/testdata/*.json` schema.
- libovsdb row values for set columns are `OvsSet{GoSet []any}` and map
  columns are `OvsMap{GoMap map[any]any}` — NOT raw `[]any`/`map[string]any`.
  Do not hand-roll a named-UUID walker over row values.
- Never give two rows in one transaction the same deterministic UUID name
  when one is being deleted: the (in-memory) server answers
  "sequence of updates not supported". Use a unique transaction-local name
  (e.g. suffix with a random UUID) for replaced rows.
- `model.Model` is just `any`: `desiredUUID`-style helpers must switch on
  concrete model types and read their `UUID` field (the named UUID used as
  `op.UUIDName`) — never re-derive from `Name` (drifts after renames).
- Mutation values for set columns marshal bare for single values
  (`["uuid",X]`, which RFC 7047 allows as a 1-element set) — live server
  accepted it, but keep set semantics in mind when batching.
