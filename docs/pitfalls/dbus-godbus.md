# D-Bus (godbus) Pitfalls

- **godbus v5 silently drops responses that contain pointer-to-basic-type
  fields** (`*bool`, `*int`, `*string`, etc.) in exported method return
  values. The method IS called (logs appear), but the response is never
  sent — the client hangs until timeout with no error. Use non-pointer
  basic types in all D-Bus return structs; add a separate `XSet bool`
  field if the value is optional. Found live 2026-10-16: `Dom0ConfigInfo`
  had `Pin *bool` which made `GetConfig` hang forever.
- **`conn.Export()` only exports methods whose last return value is
  `*dbus.Error`.** Methods returning plain `error` are silently skipped
  and return "Unknown method" on the bus. Use `conn.ExportAll()` instead
  (which wraps plain `error` returns automatically). Found live 2026-10-16.
- **godbus does not provide introspection data.** `busctl introspect` and
  `busctl tree` return "Access denied" even as root unless you separately
  export an introspectable object. You cannot verify method signatures via
  introspection — you must test with `busctl call` and read the type codes
  in the response.
- **D-Bus policy must allow `org.freedesktop.DBus.Peer.Ping`** on the
  destination service if the client uses it for liveness checks. The
  qlvm CLI calls `Peer.Ping` before every operation; without it in the
  policy, all CLI commands fail with a connection error even though other
  methods work via `busctl`.
- `bus.Object(dest, path)`: the first arg is the **destination service
  name** (e.g. `org.freedesktop.NetworkManager`, the systemd service), not
  the interface. An interface name as destination makes dbus-daemon try to
  activate it → "The name is not activatable".
- **firewalld 2.x config-manager** (`org.fedoraproject.FirewallD1.config`,
  lowercase interface):
  - `getZoneByName`/`getPolicyByName` for a missing object raise
    `org.fedoraproject.FirewallD1.Exception` with message
    `INVALID_ZONE: <n>` / `INVALID_POLICY: <n>` (NOT an empty path). Map to
    `("", nil)` — see `fw.notFoundErr`.
  - Policy zone references must be the **uppercase** literals `HOST`/`ANY`;
    lowercase is validated as a real zone and rejected.
  - `addPolicy`'s settings dict must NOT contain a `name` key (INVALID_OPTION).
  - Zone target: send a real target (`ACCEPT`); the old code shipped an
    unsubstituted `{chain}_{zone}` template.
  - **The dom0-egress policy is a strict DROP allowlist. Any service the
    dom0 itself depends on (e.g. the llama.cpp inference endpoint — see
    `firewall.egress.extra_rules` in /etc/qlvm/qlvm.yaml) needs an allow
    rule or the agent driving this repo loses its model connection mid-run.**
    Verify llama liveness after every firewall change.
  - Permanent state lives in `/etc/firewalld/{zones,policies}/*.xml` and
    survives daemon restarts: a broken permanent policy + `reload` is what
    severs egress. Rollback path: stop firewalld, move the XML aside, start.
- **dom0 networking: networkd, not NetworkManager (2026-10-05).** The
  dom0 IP config is owned by systemd-networkd: install writes
  `/etc/systemd/network/90-qlvm-{nic,br-ex}.network` (plain `DHCP=ipv4`
  for the bare NIC and for br-ex), masks NetworkManager, and OVS
  (ovsdb-native) enslaves the NIC into br-ex; whichever device holds the
  uplink carries the dom0's single LAN address. Failback is
  `ovs.Reconciler.DropEx` (remove the OVS topology; the NIC's own lease
  resumes). NM was removed because its OVS plugin fights ovs-vswitchd:
  it blocks system ports NM did not create, marks externally created
  bridges unmanaged, and its connection validation is a black box (the
  ethernet D-Bus type string is `802-3-ethernet`, not `ethernet`).
  Old note (2026-10-05): the enslaved NIC was observed holding its own
  DHCP lease in parallel. That lease is NOT guaranteed to persist (it had
  gone by 2026-10-08), which broke `install` re-runs: the ovs step waited
  for a bare-NIC lease that never came. The step now skips the NIC-lease
  wait when the NIC already has a kernel master (`netd.Enslaved`, sysfs
  `master` = `ovs-system` for OVS system ports) and relies on the
  bridge-lease check + failback instead.
  - **`systemd-networkd.service` is DISABLED by the distro preset** (`preset: disabled`), and the old install only ever *started* it via the drop-in reload — so the first reboot after install booted with no uplink until someone ran `systemctl restart systemd-networkd` (hit live 2026-10-06). The install now `EnableStart`s the unit at the top of the ovs step; if you change dom0 networking, check `systemctl is-enabled systemd-networkd` on the dom0.
  - NM D-Bus history (why the old path died): `Settings.Connection.Update`
    takes `a{sa{sv}}` and re-validates the whole connection; structured
    types round-trip through `map[string]dbus.Variant` lose their
    signatures; `GetSetting`/`UpdateSetting`/`CommitChanges` are gone in
    current NM. Each of these cost a failed migration — see git history.
