# qlvm

Go CLI for managing Xen app/disposable VMs from an ostree Fedora dom0.
Usage: `README.md`. Original rewrite brief: `rewrite-golang.md`.

## Hard constraints

- **Xen version skew (container build)**: `make container-build` compiles
  the cgo xenlight binding against the *builder image's* Xen headers
  (`Dockerfile.builder`, currently `quay.io/fedora/fedora:44` = Xen 4.21),
  but the produced binary loads the **dom0's** `libxenlight.so.*` at
  runtime. The builder's headers must stay **at or below** the dom0's Xen
  runtime version. When the dom0 upgrades Xen (or the builder image moves),
  update `Dockerfile.builder` to match and rebuild the image
  (`make container-builder`) before building — otherwise the binary hits
  missing symbols at build time or fails to load at runtime. The `xenlight`
  Go module pin in `go.mod` must match the same libxl API era (a newer
  master snapshot requires symbols absent from 4.21, e.g.
  `libxl_xs_quota_*`; it was pinned to the 4.21-era commit for this reason).
  Check the dom0 runtime with `ldconfig -p | grep libxenlight`.
- **The host is the dom0: ostree/atomic.** No `dnf` installs on the host;
  system packages belong in the ostree/bootc image recipe (see
  `../os/recipes/recipe.yml`), not ad-hoc package adds.
- **Clean repo**: no vendor/product names (CSB), no real IPs, no dropped
  config-management tooling (mgmt/purpleidea, pyinfra) — enforced in git.
- **No shelling out** to management CLIs (xl, ovn-nbctl, ovs-vsctl,
  firewall-cmd, nmcli, podman CLI) — use the native Go control planes
  (xenlight, libovsdb, godbus, podman REST). Exceptions: the `waypipe`
  exec helper and the Makefile's own `podman run` build step.

## Docs

- Design spec: `docs/superpowers/specs/2026-09-26-qlvm-design.md`
  (source of truth for architecture, CLI surface, VM model)
- Implementation plan: `docs/superpowers/plans/2026-09-26-qlvm-implementation.md`
- Dom0 hardening guide: `docs/hardening.md` (rpm-ostree-based)
- Build/usage: `README.md` (Quick start, Development sections)

## Live-smoke pitfalls (learned against the real dom0, 2026-09)

Unit fakes and the in-memory ovsdb server cannot catch these; each cost a
live round-trip. If you touch these control planes, read this section first.
**General rule: every D-Bus/OVSDB method call must be verified against the
live service (busctl introspect / a scratch object), never against memory or
older docs. Fake `Conn` interfaces test Manager logic, not wire shapes.**

### OVSDB (libovsdb)
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

### D-Bus (godbus)
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
    `[firewall.egress] extra_rules` in /etc/qvm/qlvm.toml) needs an allow
    rule or the agent driving this repo loses its model connection mid-run.**
    Verify llama liveness after every firewall change.
  - Permanent state lives in `/etc/firewalld/{zones,policies}/*.xml` and
    survives daemon restarts: a broken permanent policy + `reload` is what
    severs egress. Rollback path: stop firewalld, move the XML aside, start.
- **NetworkManager** (≥1.4x):
  - Connection objects: `/org/freedesktop/NetworkManager/Settings/<n>`
    (NOT `.../Settings/Connection/<n>`).
  - `Settings.Connection.Update` takes **`a{sa{sv}}`** (setting name →
    full property dict), not `a{sv}`; it replaces whole settings and
    re-validates the connection, so partial dicts fail
    ("connection.id/type: property is missing") and type-specific settings
    (e.g. `ovs-interface`) are required for that connection type.
  - `GetSetting`/`UpdateSetting`/`CommitChanges` are **gone** in current NM.
  - Settings round-tripped through `map[string]dbus.Variant` lose structured
    types (`ipv6.addresses` is `a(ayuay)` → rejected as `aav`).
  - **Working path for a single property (zone): edit the keyfile in
    `/etc/NetworkManager/system-connections/*.nmconnection` and call
    `org.freedesktop.NetworkManager.Reload(1)`** (connections-only flag).

### OVN / guest network
- **How it works (live-verified 2026-09-28).** Guest side: no DHCP, no
  NetworkManager — systemd-networkd applies the baked `10-bolt.network`
  (`[Match] MACAddress=` + static address/gateway/DNS) to the netfront
  interface (`enX0`). Dom0 side: each VM gets an OVN LSP named after the VM
  on the configured switch (`work`), plus an OVS port `vif<domid>.0`
  (type=system, `external-ids:iface-id=<vm-name>` — that is how
  ovn-controller ties the physical port to the LSP). Dom0 is NOT on the VM
  subnet: traffic dom0→VM routes over the uplink subnet via the OVN
  gateway router (`10.100.0.0/16 via <gw> dev br-ex`, static route on dom0
  from the legacy setup), so ping replies carry ttl=63. Legacy bash
  reference for the dom0 OVN/OVS/static plumbing: the legacy bash VM
  project (sibling tree, `qvm-install`, `qvm-harden-dom0`,
  `qvm-setup-egress`, `vif-ovn` scripts).
- **Verify in 3 commands**: `ping -c3 <vm-ip>` (ttl=63 = via OVN router);
  `sudo ovn-sbctl find Port_Binding` → the VM's port has `up: true`, a
  `chassis`, and `mac`/`port_security` = the assigned MAC (the OVN "MAC
  binding"); `sudo ovs-vsctl show` → `vif<domid>.0` under br-int.
- The dom0 vif netdev's kernel-default MAC (`fe:ff:ff:ff:ff:ff`) does NOT
  need setting — OVS/ovn match on the guest frame MAC, verified working
  without touching it. No legacy-parity step missing there.
- **In-guest debugging without console input**: console typing is flaky;
  instead loop-attach the VM disk p4 (root) while the VM is STOPPED, drop a
  one-shot `dump.service` into the deployment etc overlay
  (`ostree/deploy/default/deploy/<commit>.0/etc/systemd/system/` + a
  `multi-user.target.wants/` symlink) that writes what you need to `/var`,
  boot, kill, remount, read. The journal is persistent — read
  `/var/log/journal` the same way.
- **A duplicate MAC across LSPs silently breaks routing.** If any stale LSP
  shares a MAC with a live VM's LSP (leftover from a previous test: the
  `bolt-test` LSP with the smoke VM's MAC/IP), ovn-controller's LSP-egress
  flow (`dl_dst=<mac>` → port index in table 40) can resolve to the stale
  port's index while the logical-port-egress flow (table 46) expects the
  live port's — packets hit the `priority=0 drop` in table 46. Diagnosis:
  `ovs-ofctl dump-flows br-int` tables 40 vs 46; fix: delete the stale LSP
  (`ovn-nbctl lsp-del <uuid>`). The whole "guest network OPEN" phase of the
  live smoke was this, not anything in the guest.
- OVN ctl drift (Fedora 44 OVN): `lsp-get`/`lsp-list`-style subcommands and
  the SB `Logical_Switch_Port` table are **gone**. Use
  `--columns=... find <Table> col=val`; the SB port table is `Port_Binding`
  with column `logical_port` (no `name`).
- Guest (bootc) layout for forensics: live `/etc` =
  `ostree/deploy/default/deploy/<commit>.0/etc`; the writable `/var` is
  `ostree/deploy/default/var` (sysroot-level — NOT partition root and NOT
  the deployment's `var/`); journald is persistent there, so stop the VM,
  loop-attach p4 (root), and read `/var/log/journal` + whatever a one-shot
  dump unit wrote. Guest netfront interface is `enX0` (renamed from eth0);
  networkd matches by MAC so the name is irrelevant.
- **Loop hygiene before `qlvm vm create`**: a loop device left attached to a
  *deleted* disk.img keeps stale partition nodes and can make the template
  attach fail ("has no ostree root (and /boot) partition pair ... EINVAL").
  `sudo losetup -a`, detach strays, then create.

### Guest SSH / qlvm vm run
- **`qlvm vm run` is a user-session command** (waypipe needs the desktop's
  WAYLAND_DISPLAY; sudo strips it and the guard says so). Therefore
  `vms/<name>/` is 0755 and `meta.toml` 0644 (LoadMeta as the user);
  `disk.img` stays 0600. Run connects `user@<meta.IP>` directly —
  disposables have no `~/.ssh/config` entry by design — with
  StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null (VM generations
  reuse IPs and ship the image's host keys).
- **SSH identity is baked at template bake**: sshd.service enabled, and
  root's + SUDO_USER's `~/.ssh/id_{ed25519,ecdsa,rsa}.pub` written to
  `var/home/user/.ssh/authorized_keys` in the **osid shared var**
  (`ostree/deploy/<os>/var`, not the deployment's `var/`). Shadow `!` does
  NOT block pubkey auth. No SSH keys found = create hard-fails (a VM
  without SSH is unrunnable).
- **The guest runs SELinux Enforcing and bake-created files carry dom0's
  auto-stamped labels** (e.g. `var_t`), which confined guests reject —
  sshd logged "key is not allowed" with **no AVC line** (kauditd
  coalescing; console shows only "callbacks suppressed"). Diagnose from
  inside: `ls -Z`, `matchpathcon -V <path>` ("verified" = policy agrees).
  `setSELinuxContext` stamps `user_home_dir_t`/`ssh_home_t` on the home
  chain; stamp `security.selinux` on ANY baked path a confined daemon
  reads. dom0 setxattr works and persists.
- **DNS**: resolved+NM are masked and the distro `/etc/resolv.conf` is a
  dangling symlink to the resolved *stub*; this systemd's networkd defers
  resolv.conf writes to `/run/systemd/resolve.hook` (writes nothing, even
  retargeting the symlink to the uplink file — networkctl shows `DNS:`
  applied yet no file appears). `BakeNetworkd` therefore writes a real
  `/etc/resolv.conf` (`nameserver <dns>`); networkd sees "foreign" and
  leaves it alone.
- **A bake costs ~5 minutes** and only `qlvm template create` runs one
  (`vm create` only references): give `template create` a `timeout 600`+
  (killing it early leaves
  the image-builder podman container RUNNING as root — `podman ps -a` —
  and a template dir without `template.raw`). The dir name carries the
  digest from the build that created it — `ls templates/` before
  `rm -rf`/recreate, don't guess it from a manifest digest.
- **Never loop-mount a VM disk before the domain is gone from `xl list`**:
  ACPI `stop` can linger in `---s--` for minutes; a second rw mount of a
  live xfs is a corruption hazard. When the domain won't exit: `xl
  destroy`, verify, then mount.

### Xen / build
- Two binary flavors: repo `bin/qlvm` = portable stub (no `libxl` tag);
  `/usr/local/bin/qlvm` = real cgo build via `sudo make container-build`
  (compiles in `quay.io/fedora/fedora:44`, bundles `libyajl.so.2` with
  `$ORIGIN` rpath, SELinux `:z` on bind mounts, Go cache
  `~/.cache/qlvm-build`).
- Rebuild the container binary after ANY change before re-running live
  commands — the stub in `bin/` cannot talk to Xen.

### In-guest reboot → Xen zombie `---sr-` (reproduced 2026-09-30)
- **In-guest `reboot` is unsupported — use `qlvm vm restart`.** A plain
  in-guest `systemctl reboot` (no upgrade involved) hangs deterministically:
  the guest console ring ends with a 5 s
  `xenbus_frontend_dev_shutdown: device/vif/0 timeout closing device`, then
  `reboot: machine restart`, then NOTHING; the domain sits in `---sr-` in
  `xl list` (vCPU0 `---` halted, CPU time frozen) — unreachable, ACPI
  unresponsive, gone only via `xl destroy`. Reproduced twice back-to-back
  (Xen 4.21, guest kernel 7.2.7-200.fc44, template 72e8ffbb…): reboot fired
  ~20:54:53 → `---sr-` by 20:55:23, and ~21:03:22 → `---sr-` by 21:03:30,
  both zombied 5+ min (22 samples in run 2). This is the same terminal
  state as the "foo" incident; whether the in-guest upgrade changes
  anything on this path is untested.
- **Why no policy can save it: the dom0 has no libxl event handler.**
  qlvm creates domains via libxl cgo in a process that exits after create;
  `ps aux` shows no libxl/xl daemon for the live domain and there is no
  `/etc/libxl.conf` (libxl logging off — `journalctl -t libxl` is empty,
  zero libxl lines around create/stop/reboot/destroy). The guest's ACPI/HVM
  reboot request appears to go unhandled in the hypervisor — no dom0 process
  exists to consume it, and the hypervisor's internal state is not directly
  observable — so even the default `on_reboot=destroy` never runs. Setting
  `on_reboot=` in `internal/xenctl/xenctl_cgo.go` is a NO-OP until a dom0
  process handles libxl events for the domain's lifetime. `xl dmesg`
  (hypervisor ring) was
  empty (0 bytes). Netconsole was judged uninformative: the hang is after
  the guest kernel stops executing, so no in-guest channel reports past
  `machine restart` (the hvc0 ring already captures the kernel's last word).
- Recovery: `sudo xl destroy <name>` + `qlvm vm start <name>` — and
  `qlvm vm restart` automates this (graceful ACPI stop, linger poll ~60 s,
  force-kill on linger, start): live-verified against a real `---sr-`
  zombie on 2026-09-30 (rc=0 in ~61 s; Task 4b smoke). Loop-forensics on the
  zombie's disk are safe once the domain is gone from `xl list`.
- **The image's auto-upgrade timer makes the next reboot unattended.**
  `bootc-fetch-apply-updates.timer` ships ENABLED in the image layer
  (`/usr/lib/systemd/system/default.target.wants/`); ~1–3 h after every boot
  it runs `bootc upgrade --apply --quiet` (stages a deployment; does not
  self-reboot), so any later reboot of a long-lived VM is unattended and
  post-upgrade — and, per the note above, will zombie the domain. Image
  problem for the os-bolt sibling repo; not patchable in qlvm.
- Related state anomalies observed on the same VMs (not chased): domains
  show `-b----` (vCPU `-b-`) for parts of the life while the guest is
  provably live (`qlvm vm list` mislabels such a VM `stopped`); `qlvm vm
  stop` returned rc=0 while the domain lingered `---s--` 6+ min after a
  clean in-guest poweroff; the scratch VM ran 1 vCPU despite meta
  `vcpus = 2` (libxl config `max_vcpus:2, avail_vcpus:[0]`).
- Full evidence (console rings, timestamps, OVN/OVS, journal): the Task 3
  report in `.superpowers/sdd/2026-09-30-foo-reboot-and-uuid-fixes/`
  (gitignored; local only).

### Podman (pkg/bindings REST)
- `podman.socket` must be running: `sudo systemctl enable --now podman.socket`
  (legacy dom0s do not enable it by default; `qlvm template create` needs it).
- **Never build a zero-value `specgen.SpecGenerator` literal**: its
  `HealthLogDestination` stays `""`, and libpod's create path
  (`pkg/specgen/generate/container_create.go`) *unconditionally* runs it
  through `GetValidHealthCheckDestination` → `os.Stat("")` → create fails
  with "HealthCheck Log '' destination error: stat : no such file or
  directory". Use `specgen.NewSpecGenerator(image, false)` and set fields
  on it — the constructor fills the health-log defaults.
- **pkg/bindings `containers.Logs` sends to the caller's channels
  synchronously and never closes them.** Never `for range` the channels —
  it hangs forever once the (fast-exiting) builder is done (this hung
  `create` 23 min after a successful bake). Drain from goroutines and
  `close()` the channels yourself after the `Logs` call returns. A
  non-following fetch after `Wait` reliably returns the full log.
- **An aborted tool call does NOT kill a `sudo qlvm ...` child**: it is
  reparented to init as root and keeps running (kill needs `sudo kill`).
  Check `pgrep -af qlvm` after aborts; clean orphaned containers with
  `podman rm`.
- **image-builder CLI drift**: `ghcr.io/osbuild/image-builder-cli:latest`
  moved the raw build to `build raw <flags>` (top-level `--bootc-*` flags
  are gone) and the entrypoint `chcon`s its cache dir → the container
  must run `--privileged` under SELinux. Raw lands in `$OUTPUT/raw-x86_64.raw`
  (top level; older versions used `image/`), with `--with-buildlog
  --with-manifest` alongside.

### Templates (qlvm template)
- `vm create` only references: `--template` resolves (exact dir name or
  unique prefix, pure filesystem) and the VM's Image+Digest come from the
  template META — it never pulls or bakes. Baking is `template create
  <ref>`, a deliberate user action.
- `template list` / `clean` never touch podman. `create <ref>` pulls
  first, then: dir absent → reap + bake (~5 min); dir exists → refuse
  unless `--force`; `--force` is the re-bake: flat refusal (no override
  flag) if any VM's `vms/<n>/meta.toml` (Image+Digest) resolves to the
  target dir — delete the VM first — then reap, remove, bake.
- `clean` removes complete unreferenced dirs (a `vm create` that wants one
  then fails until `template create <ref>` re-bakes); `--force` adds
  incomplete dirs (no META, e.g. leftovers from a killed bake or by-hand
  dirs like the old `boot`/`cloud-init`).
- `create` reaps a killed bake's residue on both paths, before baking: loop
  devices whose backing file under `/var/lib/qvm` is gone (a force-killed
  VM leaves one attached to `(deleted)` — `losetup -a` shows the suffix,
  parsers must allow it), leftover `/tmp/qlvm-ostree-*` mounts
  (`unix.Unmount`; std has no `os.Unmount`), and exited image-builder
  containers (Podman REST `ancestor` filter + `Exited`).
- Legacy META files can have `image = ""` (pre-`o.Ref` builds); `Ensure`
  backfills it from the ref and re-saves — `template list`'s IMAGE column
  is empty only until the next ensure/create touches the dir.
