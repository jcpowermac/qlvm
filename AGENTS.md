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

### Waypipe control channel (`vm run --connect ssh|tcp|vsock`)
- **`--connect` picks the channel** (default `ssh`): ssh = `waypipe ssh`
  over the VM's sshd (works on every template, no relay needed); tcp =
  token control frame to the guest's qvm-ctl relay + ephemeral TCP data
  port; vsock = waypipe `--vsock` (guest relay dials the dom0's vsock
  port). **vsock kernel-module path is dead; Xen Argo is the real
  alternative (investigated 2026-10-03).** There is no
  `CONFIG_XEN_VSOCKETS` in any F44 kernel: no XEN_VSOCKETS Kconfig symbol
  or driver exists anywhere in the 7.2.8 srpm tree or mainline v7.2, and
  F44 ships no 6.x kernels (oldest: 7.1.10) — the appended config line is
  silently dropped by olddefconfig. The only existing vsock-over-Xen
  kernel stacks are `xen-troops/meta-xt-vhost` (needs qemudm/QEMU) and
  **Xen Argo** (v4v lineage: Citrix 2010 → XenServer/OpenXT 2019 →
  upstreamed to Xen 2025/26, **Status: Tech Preview**, first in the
  4.22-dev tree). Argo needs NO QEMU and NO virtio: 4 hypercalls
  (register_ring/unregister_ring/sendv/notify), hypervisor-copied 16 MB
  rings; guest+dom0 run the OpenXT `linux-xen-argo` modules
  (`argo-linux` driver + `vsock-argo` AF_VSOCK transport, WIP/experimental,
  CID ≈ domid, last touched 2025-04), and waypipe --vsock works
  unmodified. BLOCKER: the dom0's Xen 4.21.2 has no Argo (no ARGO line in
  /boot/xen-4.21.2.config; git check of stable-4.21/master) — using Argo
  means building Xen master (CONFIG_ARGO=y) as a custom dom0 hypervisor
  (reboot, all VMs down, Tech Preview) + porting both OpenXT modules to
  the 7.2 kernels (guest .ko baked into templates). Until that lands, use
  the ssh (default) or tcp channels.
- **The relay is an ncat listener, not systemd socket activation.** A
  socket-activated service receives the connection on **fd 3** (via
  LISTEN_FDS), NOT stdin — the relay's `read -r tok` from stdin got
  /dev/null EOF and the frame was never consumed (CLOSE-WAIT pileup on
  4711, dom0 hung forever). `ncat -lk 4711 --sh-exec /etc/qvm/qvm-ctl`
  hands each connection to the script as fd 0. ncat `--sh-exec` takes
  exactly ONE argument, so it points at the executable script path, not a
  split command line. A failed relay now kills only its child; no
  start-limit wedge class exists.
- **Control frame is 5 lines**: `<token>\n<mode>\n<port>\n<dom0-ip>\n<exec>\n`
  (mode tcp|vsock). Dom0 and relay must match — a 4-line relay reads the
  mode line as the port and mis-runs; re-bake or loop-patch after changes.
- **waypipe CLI**: global options go BEFORE the mode word (`waypipe
  --xwls ssh ...`); `waypipe ssh --xwls` fails (the parser treats the
  first bare word after `ssh` as the destination).
- **tcp data channel gotchas (live-verified 2026-10-03, cost the whole
  session; the guest's waypipe server dials the bridge, which dials the
  dom0's data port — every link in that chain has a failure mode):**
  - **The bridge dialer's ncat timeout needs an explicit `s` unit.**
    `ncat -w 120 host port` *exits immediately* (rc=2) printing
    "the default unit for -w is seconds … QUITTING." — ncat treats a bare
    1–5000 value as ambiguous ms-vs-s and refuses to run. The relay's
    `ncat -lkU <sock> --sh-exec "ncat -w 120 $dom0 $port"` therefore
    never dialed, the unix pipe got EPIPE, and the guest waypipe server
    panicked (`server/mod.rs` Backend BrokenPipe) and tore down the app.
    Use `-w 120s`. Symptom of the bare form: the guest waypipe process
    exists for a moment then vanishes, no TCP ever reaches the dom0 data
    port, `ss` on the dom0 shows only the relay's 4711 listener.
  - **The frame's dom0-ip is the dom0's uplink IP, not the OVN router.**
    The bridge dials `<dom0-ip>:<port>`; the dom0 data listener is bound
    on the dom0's br-ex address (e.g. 172.31.12.111), NOT
    `router_ip` (172.31.12.200, the OVN L3 gateway, which does not
    forward to dom0 host ports). `--connect tcp` therefore requires the
    new `network.dom0_ip` config field (the old code passed
    `router_ip` and hung). Set it to the same IP `ip -4 addr show br-ex`
    shows.
  - **The dom0 firewall must admit guest→dom0 data-port dials, and the
    rich rule matches the router IP, not the VM subnet.** The uplink
    `public` zone on br-ex rejects everything except ssh/mdns/dhcpv6, so
    the guest's dial of the dom0's ephemeral data port (32768–60999) was
    EHOSTUNREACH. Worse, the OVN `gateway` router SNATs ALL VM traffic
    to `router_ip` (172.31.12.200), so a rule scoping
    `source address="10.100.0.0/16"` never matched — the dom0 saw
    source 172.31.12.200. `fw.Ensure` now also manages a
    `dom0-data-in` policy (target CONTINUE, ingress ANY, egress HOST) with
    rich rules for BOTH the supernet and router_ip; port range is the
    default `ip_local_port_range`. Symptom if missing: `ping` to the
    dom0 from the guest works, ssh works, but `ncat <dom0> <highport>`
    returns "No route to host" — while the dom0 itself can connect to
    its own IP:port (that path skips the OVN SNAT + zone).
  - **`bootc-fetch-apply-updates.timer` is masked at bake.** The
    fedora-bootc base image ships it enabled; ~1–3 h after boot it stages
    an in-guest upgrade and emits a "restart required" that is a trap for
    qlvm VMs (the boot path is qlvm-owned, so the upgrade can't take
    effect, and the suggested in-guest reboot deterministically zombies
    the domain in `---sr-`). `bakeHeadlessUnits` masks it; kernel
    refresh belongs to `qlvm vm sync-kernel`. A pre-existing VM keeps
    the timer unless its disk is loop-patched (mask symlink in the
    deployment's `/etc/systemd/system/`).

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
  Known wart: the enslaved NIC still holds its own DHCP lease (networkd
  has no match condition on kernel master), so the LAN sees a double
  lease — harmless, and it makes failback instant.
  - NM D-Bus history (why the old path died): `Settings.Connection.Update`
    takes `a{sa{sv}}` and re-validates the whole connection; structured
    types round-trip through `map[string]dbus.Variant` lose their
    signatures; `GetSetting`/`UpdateSetting`/`CommitChanges` are gone in
    current NM. Each of these cost a failed migration — see git history.

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
- **Guest xfs log vs dom0 xfs (kernel skew, cost a VM 2026-10-02):** the
  guest kernel (7.2.x) writes an xfs log the older dom0 xfs driver cannot
  replay — after an unclean guest stop, any dom0 mount of the root
  partition fails with `log mount/recovery failed: error -22` (both ro and
  rw), and the *guest itself* then boots into emergency mode (its own log
  is mid-replay). `xfs_repair` without flags refuses ("valuable metadata
  changes in a log"). Fix: domain gone from `xl list`, then
  `sudo xfs_repair -L /dev/loopXp4` (resets the log), then mount rw and
  re-bake anything the lost log tail held (drop-ins, tokens). A clean
  ACPI stop (`qlvm vm stop`) replays fine — only unclean stops hit this.
  Loop-mount forensics reads are otherwise reliable; "empty reads" from a
  stale mount path were a separate (lost mount) issue, not xfs.

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

### CI (GitHub Actions)
- **CI = ubuntu-latest: non-root, no SELinux, no libxl** (stub path; the
  Makefile's exclude tags already handle the podman cgo deps). Unit tests
  must pass there, not just on this SELinux-enforcing dom0.
- **Fake-FS contract**: a fake's mount target is a REAL temp dir, so any
  raw `os.*` call on a target path materializes for real and leaks into
  raw-OS side effects (chown/setxattr ran on CI and broke the bake tests
  2026-09-29). Route tree operations through the `ostree.FS` interface and
  keep raw side effects Lstat-guarded (surgery.go `chownForUser` /
  `setSELinuxContext`). Repro the CI failure locally: `sudo go test
  ./internal/ostree/`, or non-root via `podman run --rm -v $PWD:/src:z -w
  /src localhost/qlvm-builder:local sh -c 'go test -tags
  "exclude_graphdriver_btrfs exclude_graphdriver_zfs
  containers_image_openpgp" ./...'`.

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
- `template list` / `delete` never touch podman. `create <ref>` pulls
  first, then: dir absent → reap + bake (~5 min); dir exists → refuse
  unless `--force`; `--force` is the re-bake: flat refusal (no override
  flag) if any VM's `vms/<n>/meta.toml` (Image+Digest) resolves to the
  target dir — delete the VM first — then reap, remove, bake.
- `delete [dir...]` removes the named dirs (exact name or unique prefix;
  flat refusal while a VM references one). With no args it removes complete
  unreferenced dirs (a `vm create` that wants one then fails until
  `template create <ref>` re-bakes); `--force` adds incomplete dirs (no
  META, e.g. leftovers from a killed bake or by-hand dirs like the old
  `boot`/`cloud-init`).
- **The boot kernel is per-VM state** (`vms/<name>/{vmlinuz,initramfs}`),
  seeded from the template dir at `vm start` (`vm.EnsureKernel`; existing
  files are never replaced) and refreshed in place by `vm sync-kernel`. The
  template dir is immutable after the bake — one VM's in-guest upgrade never
  re-points a sibling's boot.
- `create` reaps a killed bake's residue on both paths, before baking: loop
  devices whose backing file under `/var/lib/qvm` is gone (a force-killed
  VM leaves one attached to `(deleted)` — `losetup -a` shows the suffix,
  parsers must allow it), leftover `/tmp/qlvm-ostree-*` mounts
  (`unix.Unmount`; std has no `os.Unmount`), and exited image-builder
  containers (Podman REST `ancestor` filter + `Exited`).
- Legacy META files can have `image = ""` (pre-`o.Ref` builds); `Ensure`
  backfills it from the ref and re-saves — `template list`'s IMAGE column
  is empty only until the next ensure/create touches the dir.
