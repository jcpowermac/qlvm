# qlvm Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `qlvm`, a Go rewrite of the bash qvm toolset: unified container-based VM creation (app/disposable) on Xen PVH with OVN network isolation, 9-subcommand CLI + vif hotplug binary, native control planes only.

**Architecture:** One cobra binary (`cmd/qlvm`) + one tiny hotplug binary (`cmd/qlvm-vif`). All system state flows from `/etc/qvm/qlvm.toml`; `install` reconciles OVS/OVN/firewalld/systemd/NM declaratively; VMs are libxl domain configs built at runtime from per-VM `meta.toml` + template dirs (no .xl files). Control planes: xenlight (cgo), libovsdb, D-Bus (godbus), podman Go client, mgmt, x/crypto/ssh.

**Tech Stack:** Go (latest stable), cobra, ovn-kubernetes/libovsdb, xen-project xenlight (cgo/libxl), godbus, containers/podman client API, purpleidea/mgmt, golang.org/x/crypto/ssh, golang.org/x/sys/unix, vishvananda/netlink, BurntSushi/toml, golangci-lint.

**Spec:** `docs/superpowers/specs/2026-09-26-qlvm-design.md`

## Global Constraints

- Module path: `github.com/jcpowermac/qlvm`.
- **TDD:** every task starts with a failing test; no implementation code before a test exists for it.
- `golangci-lint run` must pass at every commit (config in Task 1).
- **Clean repo:** no references to the source environment's config-management tooling, vendor names, or real internal IPs anywhere in code, docs, test fixtures, or commit messages. Example network space is `10.100.x` / `192.168.1.x` only.
- **No `os/exec`** for any management-plane operation. The only permitted local exec is the `waypipe` binary (`internal/cli/run.go`). Enforce in review; lint cannot catch it.
- cgo is allowed (xenlight requires libxl headers: `libxl-devel` on dom0).
- All privileged-plane code (OVS/OVN/fw/NM/xen/ssh/podman) goes behind an interface in its package so unit tests run fakes; real implementations live in the same package.
- Integration tests use `//go:build integration` and never run in default `go test ./...`.

## Review Focus

1. **Stale OVN/OVS port with duplicate `iface-id`** (VM started after a `kill` without cleanup): `start` must delete the stale port before boot so the VM comes up networked, not "Multiple bindings". → test in Task 10 (start).
2. **Template and VM dir on different filesystems** (FICLONE unsupported): `create` must fail with a clear "same filesystem" error and leave **no orphan OVN port** behind. → test in Task 9 (create, ordering + error path).
3. **Dirty xfs log after `kill`** (ostree root won't mount): bake must fail with an error whose message contains the exact repair command `xfs_repair -L <dev>`. → test in Task 8 (mount failure path).
4. **NIC migration from an SSH session**: guard must detect `SSH_CONNECTION` and refuse without `--skip-nic-migration`, printing that re-running from a console is required. → test in Task 6 (install).
5. **OVS failure during vif hotplug**: `qlvm-vif` must exit non-zero so libxl aborts VM startup — never exit 0 with the port missing (silent networkless VM). → test in Task 14 (vif).

---

### Task 1: Module scaffold + `internal/config`

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `README.md` (stub), `cmd/qlvm/main.go`
- Create: `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/fixture.toml` (testdata)

**Interfaces:**
- Produces:
  - `type Config struct { Network Network; Domains []Domain; VM VMDefaults; Disposable VMDefaults; Firewall Firewall }`
  - `type Network struct { NIC, NICConnection, Gateway, RouterIP string; DNS []string }`
  - `type Domain struct { Name, Subnet, Gateway string }` // Subnet = first three octets, e.g. "10.100.1"
  - `type VMDefaults struct { User string; MemoryMB int; VCPUs int }`
  - `type Firewall struct { Egress Egress }`
  - `type Egress struct { AllowDNS, AllowHTTPS, AllowSSHToVMs, AllowICMP bool; ExtraRules []string }`
  - `func Load(path string) (*Config, error)`
  - `func (c *Config) Save(path string) error`
  - `func (c *Config) Validate() error` // non-empty NIC/Gateway/RouterIP/DNS, ≥1 domain, unique domain names, Subnet matches `^\d+\.\d+\.\d+$`, valid gateway IPs, MemoryMB>0, VCPUs>0
  - `func (c *Config) Domain(name string) (Domain, bool)`
  - `func (c *Config) VMSupernet() string` // first domain's Subnet → "10.100.0.0/16"

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`: `TestLoadFixture` (parse `testdata/fixture.toml` — the exact `[network]`/`[[domain]]`/`[vm]`/`[disposable]`/`[firewall.egress]` block from spec §4 with work=10.100.1/personal=10.100.2 — assert every field incl. `Egress.AllowICMP` and `ExtraRules`); `TestLoadValidateErrors` (subcases: empty NIC; duplicate domain name; subnet with 4 octets; MemoryMB=0); `TestSaveLoadRoundTrip` (Save to t.TempDir, Load, `reflect.DeepEqual`); `TestDomainLookup` (known + unknown name); `TestVMSupernet` (`10.100.1` → `10.100.0.0/16`).

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/config/...` — FAIL: package/identifier undefined (after `go mod init github.com/jcpowermac/qlvm` + `go get github.com/BurntSushi/toml`).

- [ ] **Step 3: Implement**

`internal/config/config.go` (structs + Load/Save via BurntSushi/toml + Validate + lookups); `testdata/fixture.toml`; `cmd/qlvm/main.go` with a bare cobra root returning "no subcommand yet"; `Makefile` targets `build` (both binaries), `test`, `lint`; `.golangci.yml` (default linters + `gosec`, `errcheck`, `revive`; skip `internal/xenctl` cgo file from body checks if needed); README stub with project name + one-paragraph description (clean-repo constraint applies).

- [ ] **Step 4: Verify pass**

Run: `go test ./... && make lint` — PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: module scaffold + config package"
```

---

### Task 2: Host allocation (IP/MAC) + netlogic

**Files:**
- Create: `internal/vm/hostalloc.go`, `internal/vm/hostalloc_test.go`

**Interfaces:**
- Produces:
  - `func HostIPMAC(d config.Domain, existing int) (ip string, mac string)` // hostnum = existing+10; ip = `<d.Subnet>.<hostnum>`; mac = `02:00:00:00:<hostnum/256 %256>:<hostnum%256>` (both two-digit hex)

- [ ] **Step 1: Write the failing test**

`TestHostIPMAC`: work domain (`10.100.1`), existing=0 → `10.100.1.10`, `02:00:00:00:00:0a`; existing=9 → `.19`/`...:00:13`; existing=205 → hostnum 215 → `10.100.1.215`, `02:00:00:00:01:07`.

- [ ] **Step 2: Verify failure** — `go test ./internal/vm/ -run TestHostIPMAC` — FAIL undefined.

- [ ] **Step 3: Implement** `HostIPMAC` in `internal/vm/hostalloc.go` (pure function; `fmt.Sprintf` for MAC).

- [ ] **Step 4: Verify pass** — `go test ./internal/vm/ -run TestHostIPMAC` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: IP/MAC host allocation"`

---

### Task 3: OVN models + reconciler

**Files:**
- Create: `internal/ovn/models.go`, `internal/ovn/reconcile.go`, `internal/ovn/ovn_test.go`

**Interfaces:**
- Consumes: Task 1 `config.Config`.
- Produces:
  - libovsdb model structs for OVN_Northbound tables: `LogicalSwitch`, `LogicalRouter`, `LogicalRouterPort`, `LogicalSwitchPort`, `ACL`, `LogicalRouterNAT`, `LogicalRouterStaticRoute` (each with `UUID ovsdb.UUID`, `Name`, `ExternalMAC`, `Networks`, `Ports`, `Type`, `Addresses`, `Options map[string]string`, `Direction`, `Priority int`, `MatchExpression string`, `Action string`, `ExternalIP`, `TypeNAT`… — field tags per libovsdb `mapper` conventions).
  - `func New(nbClient *client.OvsDbClient) *Reconciler`
  - `func (r *Reconciler) Apply(ctx context.Context, cfg *config.Config) error` // idempotent: build desired (below), insert only missing by name
  - `func Desired(cfg *config.Config) []model.Model` // pure: the exact object set from spec §5.5 — router `gateway`; per domain idx (1-based): lswitch `<name>`, lrp `gw-<name>` MAC `02:00:00:ff:<idx>:01` networks `<gw>/24`, lswitch port `<name>-to-gw` (type router, options router-port=gw-<name>); lswitch `external` + lswitch port `ext-localnet` (type localnet, options network_name=provider); lrp `gw-external` MAC `02:00:00:ff:00:01` networks `<router_ip>/24`; lswitch port `ext-to-gw`; NAT snat `<router_ip>`/`<VMSupernet>`; static route `0.0.0.0/0` → external gw; ACLs per domain: from-lport 900 drop `ip4.dst==<other subnet>.0/24` for each other domain, from-lport 800 allow `ip4`
  - `func (r *Reconciler) AddLSPort(ctx context.Context, sw, name, mac, ip string) error` // addresses + port_security both `"<mac> <ip>"`
  - `func (r *Reconciler) DelLSPort(ctx context.Context, name string) error`
  - `func (r *Reconciler) CountLSPorts(ctx context.Context, sw string) (int, error)` // excludes ports whose name ends `-to-gw` or `ext-to-gw`
  - `func (r *Reconciler) SetGatewayChassis(ctx context.Context, lrpName string, chassisID string) error` // via Logical_Router_Port ha-chassis group + Chassis model (ovn-sb or nb HA group — follow libovsdb OVN model; keep behind Reconciler)

- [ ] **Step 1: Write the failing tests**

Use libovsdb's in-process test `server` package (see libovsdb README "server" row) — stand up an in-memory OVN_Northbound db, run `Apply`, assert via `Cache.GetByCol`. Tests:
- `TestDesiredObjects`: from a 2-domain config fixture → assert the exact object list: 4 lswitches, 5 lrps, 6 lsp's, 1 NAT, 1 static route, ACL count = 2 domains × (1 + 1 other) = 4, and spot-check exact values: `gw-work` MAC `02:00:00:ff:01:01`, work ACL `ip4.dst==10.100.2.0/24` priority 900 action drop, SNAT external-ip `192.168.1.200` external-prefix `10.100.0.0/16`.
- `TestApplyIdempotent`: Apply twice against the in-process db → second apply inserts zero rows (count unchanged), no error.
- `TestAddDelLSPort` + `TestCountLSPorts` (add 3 ports + 1 `work-to-gw` → count 3).

- [ ] **Step 2: Verify failure** — `go test ./internal/ovn/...` — FAIL undefined (after `go get github.com/ovn-kubernetes/libovsdb`).

- [ ] **Step 3: Implement** `models.go` (structs with mapper tags) and `reconcile.go` (`Apply` = diff desired against cache by Name and batch-insert missing; repair path: if a domain lswitch exists but its lrp is missing, insert the lrp — this is the spec §5.5 repair case, covered by `TestApplyRepairsMissingLRP`: pre-insert only `personal` lswitch, Apply, assert `gw-personal` appears).

- [ ] **Step 4: Verify pass** — `go test ./internal/ovn/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: OVN models + declarative reconciler"`

---

### Task 4: OVS reconciler + port management

**Files:**
- Create: `internal/ovs/ovs.go`, `internal/ovs/ovs_test.go`

**Interfaces:**
- Produces:
  - OVSDB models: `OpenVSwitch` (external-ids), `Bridge`, `Port`, `Interface`
  - `func New(db *client.OvsDbClient) *Reconciler`
  - `func (r *Reconciler) Apply(ctx context.Context, nic string) error` // idempotent: openvswitch external-ids `ovn-bridge=br-int`, `ovn-remote=unix:/run/ovn/ovnsb_db.sock`, `ovn-encap-type=geneve`, `ovn-encap-ip=127.0.0.1`, `ovn-bridge-mappings=provider:br-ex`; bridges `br-int`, `br-ex` (internal) with port `br-ex-iface` (internal interface, DHCP later via NM) and member port for `<nic>`
  - `func (r *Reconciler) AddVifPort(ctx context.Context, dev, ifaceID, vmUUID, mac string) error` // delete-if-exists then add port on br-int, interface external_ids `iface-id`, `xen-vm-uuid`, `attached-mac`
  - `func (r *Reconciler) DelVifPort(ctx context.Context, dev string) error`
  - `func (r *Reconciler) StaleVifPorts(ctx context.Context, ifaceID string) ([]string, error)` // ports on br-int with external_ids iface-id == ifaceID whose interface is `vif*` but the netdev no longer exists (netdev check via injected `func(dev string) bool` field, default: `/sys/class/net/<dev>` existence)

- [ ] **Step 1: Write the failing tests**

In-process OVSDB server as in Task 3. `TestApplyIdempotent` (twice → no dup bridges/ports; assert exact external-ids map); `TestAddVifPortReplaces` (add same dev twice → one port, updated external_ids); `TestStaleVifPorts` (injected netdev-exists func returning false for `vif3.0` → listed; true → not listed; non-vif port ignored).

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Verify pass** — `go test ./internal/ovs/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: OVS reconciler + vif port management"`

---

### Task 5: D-Bus planes — firewalld, NetworkManager, systemd

**Files:**
- Create: `internal/fw/fw.go`, `internal/fw/fw_test.go`, `internal/nm/nm.go`, `internal/nm/nm_test.go`, `internal/systemd/systemd.go`, `internal/systemd/systemd_test.go`

**Interfaces:**
- Produces:
  - `internal/fw`: `type Conn interface { Call(method string, args ...any) (dbus.BusObject, error); ... }` — define the narrowest godbus wrapper interface you use (one method per firewalld call you need, named for the D-Bus method: `NewZone(zone)`, `ZoneAddService(zone, svc)`, `Reload()`, `PolicyExists(name) (bool)`, `NewPolicy(name)`, `PolicySetTarget/PolicySetPriority/PolicyAddIngressZone/PolicyAddEgressZone/PolicyAddRichRule(name, rule)`, `BindConnection…` (via NM, see below)).
  - `func (m *Manager) EgressRules(e config.Egress, supernet string) []string` // **pure** — exact rich-rule strings: dns → `rule family="ipv4" port port="53" protocol="udp" accept` + tcp twin; https → 443 tcp; ssh → `rule family="ipv4" destination address="<supernet>" port port="22" protocol="tcp" accept`; icmp → `rule family="ipv4" protocol value="icmp" accept`; then `ExtraRules` appended in order
  - `func (m *Manager) Ensure(ctx, cfg *config.Config) error` // zone `dom0` + ssh service; policy `dom0-egress` (target DROP, priority 100, ingress HOST, egress ANY, rich rules); idempotent (skip existing)
  - `internal/nm`: `func (m *Manager) EnsureOVSConnections(ctx, cfg *config.Config) error` // NM D-Bus: connections `br-ex` (ovs-bridge), `br-ex-port`, `br-ex-iface` (ovs-interface, ipv4 auto), `<nic>-port`, `<nic>-ovs` (ethernet slave) — skip existing by con-name; `func (m *Manager) SetZone(ctx, conName, zone string) error` // `connection.zone=dom0` on `br-ex-iface` + `<nic>-ovs`; `func (m *Manager) MigrateNIC(ctx, cfg, allowSSH bool) error` // refuses if `os.Getenv("SSH_CONNECTION") != "" && !allowSSH` with message naming `--skip-nic-migration`; else activate br-ex set + deactivate the NIC's own connection
  - `internal/systemd`: `func (m *Manager) EnableStart(ctx, unit string) error` // `org.freedesktop.systemd1` `StartUnit` + `EnableUnit`, idempotent (skip if active/enabled)

- [ ] **Step 1: Write the failing tests**

- `fw`: `TestEgressRules` (golden: exact strings + ordering for a full Egress with one ExtraRule; DNS disabled → no 53 rules); `TestEnsureIdempotent` (fake Conn recording calls: first Ensure creates zone+policy+rules, second Ensure creates nothing).
- `nm`: `TestMigrateNICRefusesSSH` (t.Setenv SSH_CONNECTION → error containing `--skip-nic-migration`; with allowSSH=true → calls recorded); `TestEnsureOVSConnectionsSkipsExisting` (fake reports existing con-names → not re-added).
- `systemd`: `TestEnableStartSkipsActive` (fake active=true → no StartUnit call).

- [ ] **Step 2: Verify failure** — FAIL undefined (after `go get github.com/godbus/dbus/v5`).

- [ ] **Step 3: Implement** each behind its small interface; real impl uses godbus session bus (systemd) / system bus (firewalld, NM).

- [ ] **Step 4: Verify pass** — `go test ./internal/fw/... ./internal/nm/... ./internal/systemd/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: firewalld + NM + systemd D-Bus planes"`

---

### Task 6: `install` command

**Files:**
- Create: `internal/setup/setup.go`, `internal/setup/setup_test.go`, `internal/cli/install.go`

**Interfaces:**
- Consumes: Tasks 1, 3, 4, 5.
- Produces:
  - `type Plan struct { OVN *ovn.Reconciler; OVS *ovs.Reconciler; FW *fw.Manager; NM *nm.Manager; SD *systemd.Manager; Storage func() error; VifScript func() error; SaveCfg func() error }`
  - `func Run(ctx context.Context, p *Plan, cfg *config.Config, opts Options) error`
  - `type Options struct { SkipNICMigration bool }`
  - dom0 check: `func IsDom0() (bool, error)` // reads `/proc/xen/capabilities` for `control_d` (path overridable via `Plan.Dom0Check func() (bool, error)`)
  - `internal/cli`: cobra `install` cmd: flags `--config` (default `/etc/qvm/qlvm.toml`), `--skip-nic-migration`; writes config via `cfg.Save` as final step; ordering per spec §5 (dom0 → storage → services → OVS → OVN → firewall → vif → config save).

- [ ] **Step 1: Write the failing tests**

- `TestInstallTwiceIsNoOp`: all planes fakes recording calls; run `Run` twice with the same fixture config → second run: zero mutation calls on every plane (storage, systemd, OVS, OVN, FW, NM) — the idempotency pin.
- `TestInstallRefusesWithoutDom0`: Dom0Check false → error before any plane touched.
- Review Focus 4: `TestNICMigrationSSHGuard` — SSH_CONNECTION set, no SkipNICMigration → error containing `--skip-nic-migration`, OVS/NM not mutated; with flag → proceeds.
- `TestInstallStepOrder`: fake recording sequence → assert vif-script install happens before config save.

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement** `setup.Run` (sequential steps, each printing a `=== step ===` line) + `cli/install.go` wiring real planes.

- [ ] **Step 4: Verify pass** — `go test ./internal/setup/... ./internal/cli/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: install command (idempotent orchestration)"`

---

### Task 7: Template engine — podman + image-builder + META

**Files:**
- Create: `internal/template/template.go`, `internal/template/template_test.go`

**Interfaces:**
- Produces:
  - `type Podman interface { Pull(ctx, ref string) (digest string, err error); InspectDigest(ctx, ref string) (string, error); RunImageBuilder(ctx, workdir, ref string, errStream io.Writer) error }`
  - `type Template struct { Dir, Image, Digest, KernelVer, RootDev, RootFlags, OstreePath string }` // RootDev e.g. `UUID=<uuid>`, RootFlags `subvol=root` for btrfs else "", OstreePath e.g. `/ostree/boot.1/<osid>/<commit>/0`
  - `func DirFor(root, slug, digest string) string` // `<root>/templates/<slug>-<digest>` (slug = image ref lowercased, `/`→`-`, registry host dropped)
  - `func Ensure(ctx context.Context, p Podman, tplRoot, ref string, log io.Writer) (*Template, error)` // pull → digest → if `DirFor` exists with readable META → return; else run image-builder in `DirFor`, adopt `<dir>/*.raw` as `template.raw`; boot-asset extraction + baking is Task 8's `Bake` (Ensure calls `Bake` — inject via `EnsureOpts{ Bake func(dir string) error }` so this task's tests fake it)
  - `func LoadMeta(dir string) (*Template, error)` / `func (t *Template) SaveMeta(dir string) error` // `META` file, TOML

- [ ] **Step 1: Write the failing tests**

- `TestDirFor` (ref `registry.example.com/ns/os-bolt:latest` → slug `ns-os-bolt`, dir path exact).
- `TestEnsureSkipsWhenMetaMatchesDigest`: fake Podman; pre-create dir + META with matching digest → Pull called, RunImageBuilder **not** called.
- `TestEnsureRunsBuilderWhenMissing`: no dir → RunImageBuilder called with exact args incl. `--bootc-ref <ref> --bootc-pull-container --bootc-default-fs xfs --output-dir <dir>`; fake `Bake` recorded; adopted raw renamed to `template.raw`; `LoadMeta` returns saved values.
- `TestEnsureAdoptsUnprocessedTemplate`: raw present but META digest mismatch → builder runs and raw is replaced.
- `TestMetaRoundTrip`.

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement.** Real `Podman` impl: podman Go client over `unix:///run/podman/podman.sock` (`go get github.com/containers/podman/v5/pkg/client`); `RunImageBuilder` = container run of `ghcr.io/osbuild/image-builder-cli:latest` (pull it first via the client) with `-v <dir>:/output` and the args above.

- [ ] **Step 4: Verify pass** — `go test ./internal/template/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: template engine (podman + image-builder + META)"`

---

### Task 8: Ostree surgery

**Files:**
- Create: `internal/ostree/surgery.go`, `internal/ostree/surgery_test.go`, `internal/ostree/loop.go`

**Interfaces:**
- Consumes: Task 7 `template.Template`.
- Produces:
  - `type FS interface { LoopAttach(path string) (loop string, err error); LoopDetach(dev string) error; Mount(dev, target string, ro bool) error; Umount(target string) error; Partitions(loop string) (names []string); FileExists(p string) bool; ReadDir(p string) ([]string, error); StatSize... }` — the narrow file/loop/mount surface used by surgery, default impl via `x/sys/unix` (loop ioctls `LOOP_CTL_GET_FREE`/`LOOP_SET_STATUS`, `unix.Mount/Unmount`, sysfs for partition UUID/type).
  - `type Mounter func(dev, target string, ro bool) error` — injected so the dirty-log path is unit-testable.
  - `func BakeTemplate(ctx, fs FS, dir string) error` // ensureTemplate post-build: find ostree root part (dir containing `/ostree/repo`), find /boot part (contains `ostree/*/vmlinuz-*`), copy vmlinuz+initramfs to `dir/`, compute Template fields (KernelVer, RootDev from sysfs uuid, RootFlags, OstreePath = first `ostree/boot.*` sorted -V + osid + commit + `/0`), write identity+units into deployment tree (`<root>/ostree/deploy/<osid>/deploy/<commit>.0/etc/…`), SaveMeta.
  - `func BakeNetworkd(ctx, fs FS, diskPath, ip, gw, mac, dns string) error` // per-VM: loop-attach, mount root rw (via Mounter), write `<depTree>/etc/systemd/network/10-bolt.network` (golden content: `[Match] MACAddress`, `[Network] Address ip/24 Gateway DNS Domains=~.`), umount, detach.
  - `func IdentityLines(user string, uid int) (passwd, group, shadow string)` // pure
  - `func NetworkdFile(ip, gw, mac, dns string) string` // pure

- [ ] **Step 1: Write the failing tests**

- `TestNetworkdFile` (golden, exact bytes).
- `TestIdentityLines` (`user:x:1000:1000::/home/user:/bin/bash` etc.).
- `TestOstreePathComputation`: fake FS with `ostree/boot.1`+`boot.2`, osid/commit dirs → picks `boot.1`, exact path `/ostree/boot.1/os1/abc123/0`.
- `TestBakeTemplateFindsParts`: fake FS (root part has `ostree/repo`, boot part has `ostree/os1/vmlinuz-6.1.0`) → Template fields set correctly; vmlinuz copied (fake records copy).
- Review Focus 3: `TestBakeNetworkdMountFailure`: Mounter returns `&unix.SyscallError{Err: unix.EBUSY}` (bad-superblock class) → error message contains `xfs_repair -L` **and** the device name.
- `TestBakeTemplateCleanupOnFailure`: fake fails mid-bake → loop detached + umounted (fake records).

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement.** Loop attach: `unix.IoctlRet(int(fd), unix.LOOP_CTL_GET_FREE, ...)` on `/dev/loop-control`, then `LOOP_SET_STATUS` (lo_info{filename, offset, sizelimit 0, autoclear 1, read_only 0}); partition nodes = `<loop>p<N>` from `Partitions` (sysfs `/sys/class/block/<loop>/`); udev settle equivalent: poll partition node existence (≤5s).

- [ ] **Step 4: Verify pass** — `go test ./internal/ostree/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: ostree surgery (loop/mount/bakes)"`

---

### Task 9: VM create + meta + DomainConfig

**Files:**
- Create: `internal/vm/meta.go`, `internal/vm/create.go`, `internal/vm/domcfg.go`, `internal/vm/create_test.go`, `internal/vm/sshconfig.go`, `internal/vm/sshconfig_test.go`, `internal/mounts/reflink.go`, `internal/mounts/reflink_test.go`, `internal/cli/create.go`

**Interfaces:**
- Consumes: Tasks 1, 2, 3, 7, 8.
- Produces:
  - `type Meta struct { Name, Type, Image, Digest, Domain, IP, MAC string; MemoryMB, VCPUs int; Mounts []Mount; Uuid string; Created time.Time }`
  - `type Mount struct { Host, Guest string }` // p9: Host path on dom0, Guest tag
  - `func LoadMeta(vmDir string) (*Meta, error)` / `func (m *Meta) Save(vmDir string) error` // `meta.toml`
  - `type CreateDeps struct { OVN *ovn.Reconciler; Tpl *template.Template; FS ostree.FS; Mounter ostree.Mounter; Reflink func(dst, src string) error }`
  - `func Create(ctx context.Context, d CreateDeps, cfg *config.Config, spec Spec) (*Meta, error)`
  - `type Spec struct { Name, Domain, Type, Image string; MemoryMB, VCPUs int; Mounts []Mount }` // zero MemoryMB/VCPUs → defaults from cfg per Type
  - `func DomainConfig(m *Meta, tpl *template.Template) *xenlight.DomainConfig` // **pure, golden-tested** (xenlight = `xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight`, package `xenlight`): Type PVH, Name, Uuid (parsed from m), Kernel `tpl.Dir/vmlinuz`, Ramdisk `tpl.Dir/initramfs`, Extra `[root=<RootDev> <RootFlags> ostree=<OstreePath> systemd.default-target=multi-user.target console=hvc0]`, MaxVcpus, TargetMemkb, Disks [{PdevPath `<vmDir>/disk.img`, Vdev `xvda`, Format Raw, Readwrite 1}], Nics [{Mac, Script `vif-ovn`, Nictype Vif}], P9S [{Tag m, Guest…, Path mount.Host, SecurityModel `none`, Type Xen9Pfsd}]
  - `func AddSSHConfig(home, m *Meta) error` / `func RemoveSSHConfig(home, name string) error` // idempotent Host block, exact format from spec §6.5
  - `internal/mounts`: `func Reflink(dst, src string) error` // `unix.CloneFileRange` fallback; on `EOPNOTSUPP/EXDEV` → error containing "same filesystem"
  - `internal/cli`: cobra `create` cmd (flags `--domain`, `--type` (default `app`), `--image` required, `--memory`, `--vcpus`, `--mount` repeatable `host:guest`).

- [ ] **Step 1: Write the failing tests**

- `TestDomainConfigGolden`: full Meta (2 mounts, disposable) + Template → assert every libxl field (extra string byte-exact, p9 entries, vif script).
- `TestCreateHappyPath`: fakes — assert **order**: OVN port added → reflink → networkd bake → meta save → ssh config (app only); assert `Spec` defaulting (disposable gets `cfg.Disposable` values).
- Review Focus 2: `TestCreateReflinkFailureLeavesNoOrphanPort`: Reflink returns EXDEV error → error returned contains "same filesystem" **and** fake OVN records a `DelLSPort` for the VM (cleanup).
- `TestAddSSHConfigIdempotent` + `TestRemoveSSHConfig` (golden ~/.ssh/config before/after; existing other Host blocks untouched).
- `TestReflinkError`: inject clonefn returning `unix.EXDEV` → message contains "same filesystem".

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement** (create ordering per spec §6; cleanup on failure = delete OVN port; `Uuid` = random, persisted in Meta).

- [ ] **Step 4: Verify pass** — `go test ./internal/vm/... ./internal/mounts/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: vm create (meta, reflink, domain config, ssh config)"`

---

### Task 10: Lifecycle — xenctl + start/stop/kill/delete/list

**Files:**
- Create: `internal/xenctl/xenctl.go`, `internal/xenctl/xenctl_test.go`, `internal/cli/lifecycle.go`

**Interfaces:**
- Consumes: Tasks 4, 9.
- Produces:
  - `type Xen interface { CreateDomain(*xenlight.DomainConfig) error; Destroy(name string) error; Shutdown(name string) error; List() ([]DomainInfo, error); Running(name string) (bool, error) }`
  - `type DomainInfo struct { Name string; ID uint32; MemMB uint64; VCPUs uint8; State string }`
  - Real impl wraps `xenlight.NewContext()` (one Context per process; `defer Close`).
  - `func Start(ctx, x Xen, ovs *ovs.Reconciler, m *Meta, tpl *template.Template) error` // error if Running; `StaleVifPorts` → `DelVifPort` each; then CreateDomain(DomainConfig(m, tpl))
  - `func Delete(ctx, x Xen, ovs *ovn.Reconciler, ovsOvs *ovs.Reconciler, home string, vmDir, name string) error` // Destroy-if-running → DelLSPort → stale port cleanup → rm vmDir → RemoveSSHConfig (app only, read meta first)
  - `internal/cli`: `start`, `stop`, `kill`, `delete`, `list` cmds. `list` output columns: `NAME  TYPE  STATE  MEM  VCPUS` — running from `x.List()`, stopped from `meta.toml` scan under an "available" section.

- [ ] **Step 1: Write the failing tests**

- Fake Xen + fake OVS. `TestStartRefusesRunning`; Review Focus 1: `TestStartCleansStalePort` (StaleVifPorts returns `["vif3.0"]` → DelVifPort called with it before CreateDomain; then create called); `TestDeleteOrdering` (records: Destroy, DelLSPort, DirRemoved, SSHRemoved only when type=app); `TestListRows` (2 running + 1 stopped meta → exact output lines incl. `disposable` type column).

- [ ] **Step 2: Verify failure** — FAIL undefined. Add the dep: `go get xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight@<latest xen tag sha>` — pin a release tag, never floating `master`; record the pinned ref in the commit message. cgo requires libxl headers (`libxl-devel`) — `make build` surfaces a clear error if absent.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Verify pass** — `go test ./internal/xenctl/... ./internal/cli/...` — PASS (cgo build of xenctl only; ensure `CGO_ENABLED=1 go build ./...` works — add libxl check to Makefile `build` with helpful error).

- [ ] **Step 5: Commit** — `git commit -am "feat: lifecycle commands (start/stop/kill/delete/list)"`

---

### Task 11: sshx + `run` + `sync-kernel`

**Files:**
- Create: `internal/sshx/sshx.go`, `internal/sshx/sshx_test.go`, `internal/cli/run.go`, `internal/cli/synckernel.go`

**Interfaces:**
- Produces:
  - `func Resolve(host string, home string) (hostname string, user string, err error)` // reads `~/.ssh/config` Host block (HostName/User), fallback user = `user`
  - `func Connect(ctx, home, hostname, user string) (*ssh.Client, error)` // agent/keys: try ssh-agent then `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`; `StrictHostKeyChecking no` equivalent (InsecureClientHostKeyCallback — VMs are auto-provisioned, matching the bash behavior)
  - `func WaitForSSH(ctx, dial func() (*ssh.Client, error), maxTries int, interval time.Duration) error`
  - `func FetchFile(ctx, c *ssh.Client, path string) (io.ReadCloser, error)`
  - `func Run(ctx, c *ssh.Client, cmd string) (string, error)`
  - `func KernelVersion(output string) (string, error)` // parses `rpm -q kernel-core --last | head -1`
  - `internal/cli`: `run` cmd — resolves VM (meta) then **execs `waypipe ssh <vm> <app>…`** (only permitted local exec; error if `WAYLAND_DISPLAY` empty: "run from the dom0 Wayland session"); `sync-kernel` cmd — Run `rpm -q kernel-core --last | head -1`, KernelVersion, FetchFile `/boot/vmlinuz-<ver>` + `/boot/initramfs-<ver>.img` into the VM's template dir (from Meta → `template.DirFor`), instruct restart.

- [ ] **Step 1: Write the failing tests**

- `TestResolve` (ssh_config fixture with two Host blocks; exact hostname/user; missing block → error); `TestKernelVersion` (`kernel-core-6.11.9-300.fc44.x86_64` → `6.11.9-300.fc44`); `TestWaitForSSH` (fake dial: fails twice then ok, interval 1ms → nil; 3 tries → error); `TestRunWaypipeGuard` (WAYLAND_DISPLAY empty → error string contains `WAYLAND_DISPLAY`); `TestSyncKernelFlow` (fake ssh: Run returns version line, FetchFile returns bodies → files written to template dir with exact names, bodies intact).

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Verify pass** — `go test ./internal/sshx/... ./internal/cli/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: sshx + run + sync-kernel"`

---

### Task 12: `provision` (mgmt)

**Files:**
- Create: `internal/provisioner/provisioner.go`, `internal/provisioner/provisioner_test.go`, `internal/cli/provision.go`, `provision/base/packages.txt`, `provision/base/dotfiles/.gitkeep`

**Interfaces:**
- Consumes: Task 11 `sshx.Resolve`.
- Produces:
  - `func ParsePackages(dirs ...string) []string` // union, file order preserved, `#` comments + blank lines dropped, de-duplicated
  - `func DotfileList(dir string) ([]string, error)` // files under dir, relative paths, `.gitkeep` excluded
  - `type Runner interface { Run(ctx, host string, ops []Op) error }` // wraps mgmt (real: mgmt batch with SSH transport, host from sshx.Resolve; `_sudo` for package install)
  - `func Provision(ctx, r Runner, home, vm, provisionDir string, mode Mode) error` // `Mode` ∈ all/packages/dotfiles; layers `base` then `<vm>` (missing dir = skip)
  - `internal/cli`: `provision` cmd with flags `--packages-only`, `--dotfiles-only` (default: both). The bash `setup.sh`/`--profile` concepts are gone — custom commands run via `qlvm run`.
  - Repo scaffold: `provision/base/packages.txt` (comment-only), `provision/base/dotfiles/.gitkeep`.

- [ ] **Step 1: Write the failing tests**

- `TestParsePackages` (two dirs, overlap, comments, blanks → exact slice); `TestDotfileList` (nested `.config/` file included, `.gitkeep` excluded, relative paths); `TestProvisionModes` (fake Runner: packages mode → only pkg op with union list; dotfiles → file ops with exact paths; all → both; missing per-vm dir → base only, no error).

- [ ] **Step 2: Verify failure** — FAIL undefined (after `go get github.com/purpleidea/mgmt`).

- [ ] **Step 3: Implement** (mgmt real Runner: `mgmt.New` with ssh transport + `mgmt.Batch` of `mgmt.NewOperation`… follow mgmt's API for `pkg`/`file` ops; keep the translation inside `Runner`).

- [ ] **Step 4: Verify pass** — `go test ./internal/provisioner/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: provision via mgmt"`

---

### Task 13: `apps` — desktop cache + rofi

**Files:**
- Create: `internal/apps/apps.go`, `internal/apps/apps_test.go`, `internal/cli/apps.go`

**Interfaces:**
- Consumes: Task 11 `sshx`.
- Produces:
  - `func SplitDesktops(data []byte) (map[string]string, error)` // concatenated `/usr/share/applications/*.desktop` stream → per-entry name→content (split on `[Desktop Entry]`; entry key = its `Name=` value, fallback `entry-<n>`)
  - `func WriteCache(cacheDir, vm string, entries map[string]string) (int, error)` // replaces `<cacheDir>/<vm>/*.desktop`, returns count
  - `func EmitRofi(cacheDir string) (string, error)` // rofi mode protocol: per entry with `Type=Application` and `NoDisplay != true` and non-empty `Name=`+`Exec=` → line `[<vm>] <name>\0icon\x1f<icon|application-x-executable>\x1finfo\x1f<vm>|<exec>\n` with exec field-codes (`%[fFuUdDnNickvm]`) stripped; sorted
  - `func Launch(rofiInfo, cacheDir string, running func(vm string) bool, start func(vm string) error, waitSSH func(vm string) error, runApp func(vm, exec string) error) error` // `vm|exec` split; start+wait only when `running(vm)` is false
  - `internal/cli`: `apps` cmd — `apps sync [vm]` (default: all running VMs per injected running-list; fetch over sshx.FetchFile); bare `apps` reads `ROFI_RETV`/`ROFI_INFO` env like the bash original.

- [ ] **Step 1: Write the failing tests**

- `TestSplitDesktops` (3-entry stream incl. one with no Name → fallback key; exact contents); `TestEmitRofiGolden` (fake cache dir: app entry, NoDisplay entry, non-Application entry, entry missing Exec → exactly one rofi line, byte-exact incl. `\x1f` and default icon); `TestLaunchStartsStoppedVM` (running=false → start+waitSSH called then runApp; running=true → neither).

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Verify pass** — `go test ./internal/apps/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: apps (desktop cache + rofi mode)"`

---

### Task 14: `qlvm-vif` hotplug binary + xenstore

**Files:**
- Create: `internal/xenstore/xenstore.go`, `internal/xenstore/xenstore_test.go`, `cmd/qlvm-vif/main.go`, `cmd/qlvm-vif/main_test.go`, `internal/cli/vifinstall.go` (used by Task 6's `Plan.VifScript`)

**Interfaces:**
- Produces:
  - `type Client struct{ ... }` // dials `/var/run/xenstore/socket` (path injectable); `func (c *Client) Read(path string) (string, error)` // protocol: `R <path>\n` → `r <value>\n` / `e <errcode>\n`
  - `func VifHandle(command, dev string, xs *xenstore.Client, ovs *ovs.Reconciler, netlinkUp func(dev string) error) error` // `-emu` → nil (no-op); `add|online`: frontend-id from `/local/domain/0/device/vif-<dev>/frontend-id`-equivalent (read `$XENBUS_PATH/frontend-id` env like the bash: `XENBUS_PATH` + `XENBUS_DEV` env vars) → vm name/uuid → mac → `ovs.AddVifPort` → netlinkUp; any failure → error (main exits 1); `remove|offline`: `ovs.DelVifPort` + link down, errors swallowed (bash: do_without_error)
  - `func InstallVifScript(dst string, src string) error` // copy binary, chmod 0755 (src injectable; default = os.Executable() of qlvm-vif located via env `QVM_VIF_BIN` set by install, fallback `qlvm-vif` in PATH)
  - `cmd/qlvm-vif/main.go`: argv `<command> <dev> <domid> <mac> <port>`, wires real planes, exit 0/1 per vif-script convention.

- [ ] **Step 1: Write the failing tests**

- `TestXenstoreRead` (in-memory server goroutine on a temp socket: `R /local/domain/1/vm/name` → `r myvm`; error path `e 2` → error containing `enoent`).
- `TestVifSkipsEmu` (`vif0.0-emu` → no ovs/netlink calls, nil).
- `TestVifAddHappyPath` (fake xs returns name/uuid/mac; fake ovs+netlink record calls with exact values).
- Review Focus 5: `TestVifAddFailurePropagates` (ovs fails → error returned; `main` maps error → exit 1 — test via `VifHandle` return + a small `run(args...) int` function in main_test).
- `TestInstallVifScript` (temp src/dst: copied, mode 0755).

- [ ] **Step 2: Verify failure** — FAIL undefined.

- [ ] **Step 3: Implement**; wire `Plan.VifScript` in `internal/setup` to `InstallVifScript("/etc/xen/scripts/vif-ovn", …)` (path overridable in Plan for tests — Task 6's fakes already cover the call).

- [ ] **Step 4: Verify pass** — `go test ./internal/xenstore/... ./cmd/qlvm-vif/...` — PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat: qlvm-vif hotplug binary + xenstore client"`

---

### Task 15: CI, docs, README, integration scaffold

**Files:**
- Create: `.github/workflows/ci.yml`, `README.md` (full), `docs/hardening.md`, `docs/ovn-gateway-runbook.md` (sanitized: all real IPs → `10.100.x`/`192.168.1.x` placeholders, no environment-specific tooling), `internal/itest/all_test.go` (`//go:build integration`)
- Modify: `Makefile` (add `test-integration`)

**Interfaces:**
- Consumes: everything.
- Produces: working repo a newcomer can install and use.

- [ ] **Step 1: CI workflow** — `golangci-lint run` (container with libxl-devel for cgo build), `go test ./...`, `make build`. Run: `act` or push to verify; at minimum `golangci-lint run` locally must pass.

- [ ] **Step 2: Integration scaffold** — `internal/itest/all_test.go`: `TestInstallIdempotent` (real planes, run `setup.Run` twice, assert second pass no-ops by OVN/OVS row counts before/after) + `TestCreateStartSSHDelete` (guarded: skip unless `QVM_ITEST=1`; create disposable from env `QVM_ITEST_IMAGE`, start, WaitForSSH, delete; assert no leftover lswitch port + vmDir gone).

- [ ] **Step 3: README** — quick start (install → create → start → run → apps), subcommand table, config reference (the spec §4 TOML verbatim), architecture diagram (dom0/br-int/br-ex/waypipe), layout, dev (make targets, TDD, golangci-lint). Clean-repo constraint applies.

- [ ] **Step 4: Sanitized docs** — `hardening.md` (package-removal + greetd guidance from the old harden script, generic wording); `ovn-gateway-runbook.md` (chassis-claim diagnosis/fix, real IPs and hostnames replaced with `10.100.x`/`<dom0>`).

- [ ] **Step 5: Final gate + commit**

Run: `make build && make lint && go test ./...` — all PASS.

```bash
git add -A && git commit -m "chore: CI, README, sanitized docs, integration scaffold"
```

---

## Execution notes

- Tasks are ordered by dependency; Tasks 3–5 are mutually independent (parallelizable after Task 1).
- `go get` pins: libovsdb `main`, podman client `v5`, mgmt `master`, godbus `v5`, xenlight `xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight@<release-tag>` (record the pinned ref in the commit message).
- Every task ends green (`go test ./...` + `make lint`) before the next starts.
