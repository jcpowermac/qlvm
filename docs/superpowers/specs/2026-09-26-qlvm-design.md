# qlvm — Qubes-like VM Isolation on Xen, in Go

## 1. Purpose

`qlvm` is a clean-room Go rewrite of the bash `qvm-*` toolset: a lightweight,
Qubes-OS-inspired security compartmentalization system for a Fedora-based
dom0 running Xen in PVH mode. VMs boot container-based OS images (bootc/ostree),
network isolation is enforced by OVN ACLs in the OVS datapath, and GUI apps are
forwarded to the dom0 Wayland desktop via waypipe.

Hard constraints:

- **Clean repo.** No references to the original environment's config-management
  tooling, vendor branding, or real internal IP addresses. Documentation uses
  the RFC1918 example space `10.100.x` only.
- **No shelling out to management CLIs.** All system control planes are driven
  natively (cgo libxl, OVSDB JSON-RPC, D-Bus, podman client API, Go SSH).
  The single unavoidable local exec is `waypipe` itself (a GUI process that
  must exist on dom0; it is not a management plane).
- **Container-based installs only.** All VMs — persistent, disposable, agent —
  are built from a container image reference via osbuild image-builder. The
  legacy cloud-image / cloud-init / pyinfra / NFS paths are gone.
- Go best practices, `golangci-lint`, TDD.

## 2. VM model

One creation flow, generalized from the former "bolt" throwaway-VM tool:

- A **template** is a raw disk image built from a bootc container ref by
  `image-builder-cli` (run as a container via the podman client API). The
  template has identity (user + SSH key) and headless runtime units baked
  into its ostree deployment tree.
- A **VM** is a btrfs reflink copy (`FICLONE`) of the template. Per-VM
  networkd config is baked in by mounting the ostree root over a loop device.
- VMs are created directly through libxl (`xenlight`) — **no `.xl` files**.
- **Type** is `app` (persisted, default) or `disposable` (throwaway).
  - `app`: persisted, gets a `~/.ssh/config` entry.
  - `disposable`: throwaway, no ssh-config entry.
  - Both are mechanically identical today; type is stored metadata shown by
    `list`. An agent VM is simply an app/disposable VM whose container image
    ships the agent toolkit — no separate agent concept.
- **Mounts**: `--mount host:guest` creates a p9 share (Xen `xen_9pfsd`,
  `security_model=none`) passed natively in the libxl domain config. This
  replaces all NFS functionality.

## 3. CLI surface

Single binary `qlvm` (cobra), 9 subcommands, plus one small helper binary:

```
qlvm install                              # idempotent dom0 orchestration
qlvm create <name> --domain <d> \
      [--type app|disposable] --image <ref> \
      [--mount host:guest]... \
      [--memory MB] [--vcpus N]
qlvm start <name>
qlvm stop <name>
qlvm kill <name>
qlvm delete <name>
qlvm list                                 # columns: name, type, state, mem, vcpus
qlvm run <name> <app> [args...]           # waypipe ssh
qlvm provision <name> [--packages-only|--dotfiles-only]
qlvm sync-kernel <name>
qlvm apps [sync [vm]]                     # rofi launcher + desktop-file cache
```

Plus `cmd/qlvm-vif`: a second tiny binary installed at
`/etc/xen/scripts/vif-ovn` by `install`. libxl invokes it as the vif hotplug
script; it reads xenstore and programs the OVS port via libovsdb.

Deliberately dropped vs. the bash toolset: `qvm-nfs-manager`,
`qvm-harden-dom0` (content moves to `docs/hardening.md`), `qvm-agent` as a
concept (image-driven), `bake-template` (container images replace baked
alpine-agent images), `vif-ovn` as a subcommand, the rofi helper as a
standalone script (folded into `qlvm apps`), all NFS code.

## 4. Configuration

Single source of truth: `/etc/qvm/qlvm.toml` (TOML). `install` writes it;
editing it + re-running `install` is the change path (reconciliation).

```toml
[network]
nic = "enp1s0"                 # physical NIC
nic_connection = "Wired connection 1"
gateway = "192.168.1.1"        # physical LAN gateway
router_ip = "192.168.1.200"    # unused LAN IP for the OVN SNAT router
dns = ["1.1.1.1", "1.0.0.1"]

[[domain]]
name = "work"
subnet = "10.100.1"            # first three octets; VMs get .10, .11, ...
gateway = "10.100.1.1"

[[domain]]
name = "personal"
subnet = "10.100.2"
gateway = "10.100.2.1"

[vm]
user = "user"                  # VM user baked into templates
memory_mb = 4096
vcpus = 2

[disposable]
memory_mb = 4096
vcpus = 2

[firewall.egress]              # rich rules for the dom0-egress policy
allow_dns = true
allow_https = true
allow_ssh_to_vms = true        # SSH restricted to the VM supernet
allow_icmp = true
extra_rules = []               # additional firewalld rich-rule strings
```

Per-VM state lives under `/var/lib/qvm/vms/<name>/`:

- `disk.img` — reflink copy of the template
- `meta.toml` — name, type, image ref + digest, domain, ip, mac, memory, vcpus, mounts

Storage layout:

```
/var/lib/qvm/
├── templates/<slug>-<digest>/
│   ├── template.raw
│   ├── vmlinuz  initramfs
│   └── META     # image, digest, kernel version, root uuid, ostree path
└── vms/<name>/{disk.img,meta.toml}
```

## 5. `install` — idempotent orchestration

The dom0 image already ships Xen, OVS, OVN, firewalld, NetworkManager, podman.
`install` does **no package installation**; it is pure orchestration. Every
step is idempotent ("desired exists → skip") so `install` can be re-run at
will (TDD: integration test runs it twice).

1. **Verify dom0** — `/proc/xen/capabilities` contains `control_d`.
2. **Storage** — btrfs subvolume (or plain dir fallback) `/var/lib/qvm` + tree.
3. **Services** — enable+start openvswitch, ovn-northd, ovn-controller
   (systemd D-Bus `org.freedesktop.systemd1`).
4. **OVS** — `br-int` with OVN encap external-ids; `br-ex` with the physical
   NIC as a member port and an internal `br-ex-iface` port (DHCP) — OVS via
   libovsdb, NetworkManager connections via the NM D-Bus API. NIC migration
   warns and aborts (with `--skip-nic-migration`) if a controlling terminal
   is absent and `SSH_CONNECTION` is set, since the move drops the session.
5. **OVN topology** (reconciled from config, libovsdb):
   - logical router `gateway`
   - per-domain: logical switch `<name>`, router port `gw-<name>`
     (MAC `02:00:00:ff:<idx>:01`), switch router port `<name>-to-gw`
   - `external` switch with `ext-localnet` (localnet → `provider:br-ex`)
   - `gw-external` LRP at `router_ip/24`, `ext-to-gw` switch port
   - SNAT for the VM supernet (first domain subnet's `/16`) + default route
     via physical gateway
   - gateway chassis claim (highest HA priority)
   - inter-domain deny: `from-lport 900` drop to every other domain's subnet,
     `from-lport 800` allow ip4
   - repair path: domain switch exists but router port missing → recreate LRP
6. **Firewall** (reconciled from config, firewalld D-Bus):
   - zone `dom0` (SSH inbound) bound to `br-ex-iface` + NIC via NM `connection.zone`
   - policy `dom0-egress`: target DROP, priority 100, ingress HOST, egress ANY,
     rich rules from `[firewall.egress]` (DNS 53, 443, SSH→VM supernet, ICMP,
     `extra_rules`)
7. **vif script** — install `qlvm-vif` at `/etc/xen/scripts/vif-ovn` (copy
   binary, `chmod +x`).
8. **Config** — write `/etc/qvm/qlvm.toml`.

## 6. `create`

1. **ensureTemplate(image)** — idempotent on image digest:
   - podman client: `pull <ref>` → digest.
   - If `templates/<slug>-<digest>/` exists → done.
   - Else: run `ghcr.io/osbuild/image-builder-cli:latest` via podman
     (`build raw --bootc-ref <ref> --bootc-pull-container --bootc-default-fs xfs
     --output-dir /output`), adopt the produced raw as `template.raw`.
   - Ostree surgery (loop attach via `x/sys/unix` ioctls + `unix.Mount`):
     - locate the ostree root partition (the one with `/ostree/repo`) and the
       `/boot` partition (containing `ostree/<osid>/vmlinuz-*`); partition
       UUIDs/types from sysfs (`/sys/class/block/…`).
     - extract `vmlinuz` + `initramfs` to the template dir.
     - bake identity: user + `~/.ssh/authorized_keys` in the deployment tree's
       `/etc` and the state `var/home`; SELinux labels for unit files, home,
       ssh key.
     - bake headless units: `bolt-rundir` (create `/run/user/1000`), mask
       NetworkManager, enable systemd-networkd.
   - write `META` (image, digest, kernel version, root UUID + fstype + flags,
     ostree boot path).
   - If any mount fails (e.g. dirty xfs log after a `kill`): hard error
     printing the manual repair command (`xfs_repair -L <dev>`) — no auto
     repair, no exec.
2. **VM state** —
   - look up domain in config → subnet/gateway; next host number =
     existing ports on the switch (excluding `*to-gw`) + 10 →
     `IP = <subnet>.<n>`, `MAC = 02:00:00:00:<hi>:<lo>`.
   - OVN: logical switch port `<name>` with addresses + port-security
     (`libovsdb`).
   - `vms/<name>/disk.img` = reflink clone of `template.raw` (`FICLONE`);
     requires the same btrfs filesystem — validated with a clear error.
3. **Per-VM network bake** — loop-attach `disk.img`, mount ostree root rw,
   write `10-bolt.network` (Match MAC, static IP/gw/DNS) into the deployment
   tree's `/etc/systemd/network/`, umount, detach.
4. **Domain config** — the full libxl domain configuration (PVH type, name,
   stable UUID, kernel/ramdisk from template dir, `extra` =
   `root=UUID=… [rootflags] ostree=<path> systemd.default-target=multi-user.target
   console=hvc0`, disk `xvda` = `disk.img` (raw, rw), vif `mac=<MAC>,script=vif-ovn`,
   `P9S` entries for each `--mount`) is stored in `meta.toml`, not booted.
   Xen has no such thing as a stopped domain — "stopped" means "not created".
   `create` therefore prepares; `start` calls `DomainCreateNew` (mirrors the
   bash create-config-file-then-`xl create` split).
5. **Side effects** — `meta.toml`; ssh-config block for `app` type (idempotent
   add, removed by `delete`).

## 7. Lifecycle

- **start** — error if the domain already runs; cleanup stale OVS ports whose
  `external-ids:iface-id` matches the VM but whose netdev is gone (libovsdb
  scan + delete); then `DomainCreateNew` with the stored config from
  `meta.toml` + template.
- **stop** — `DomainShutdown` (ACPI).
- **kill** — `DomainDestroy`.
- **delete** — `DomainDestroy` (if running), remove OVN lswitch port, remove
  OVS port (stale-port cleanup as in start), remove `vms/<name>/` (reflink —
  only private extents reclaimed), remove ssh-config block (app type).
- **list** — `ListDomain` (xenlight) joined with `meta.toml` files; columns
  name, type (`app`/`disposable`), state, mem, vcpus; stopped VMs listed
  under "available".
- **run** — `exec waypipe ssh <name> <app>…` (only local exec in the project;
  requires `WAYLAND_DISPLAY`, errors clearly when absent).
- **sync-kernel** — over SSH (`x/crypto/ssh`): resolve the VM's current kernel
  version (`rpm -q kernel-core --last`), copy `vmlinuz` + `initramfs` from the
  VM into the matching template dir; tell the user to restart VMs.

## 8. `provision`

Uses **mgmt** (Go config-management, pyinfra replacement) over SSH:

- `--packages-only`: install packages listed in
  `provision/base/packages.txt` + `provision/<vm>/packages.txt` (dnf on the
  remote — remote package installs run through the remote shell, which is
  provisioning, not dom0 control plane).
- `--dotfiles-only`: rsync-style sync of `provision/base/dotfiles/` +
  `provision/<vm>/dotfiles/` into the VM's home.
- default: both. Repo ships a `provision/base/` scaffold (empty packages.txt,
  example dotfiles layout).

## 9. `apps`

- `apps sync [vm]` — over SSH (`x/crypto/ssh`): fetch
  `/usr/share/applications/*.desktop` from running VM(s) (default: all
  running), split into per-app cached files under
  `/var/lib/qvm/desktop-cache/<vm>/`.
- `apps` (rofi mode protocol on argv/env, like the former `qvm-appmenu`):
  `ROFI_RETV=0` → emit rofi entries from the cache (Type=Application only,
  NoDisplay filtered); `ROFI_RETV=1` → launch: if the VM is stopped, start it
  and wait for SSH, then `run`.

## 10. `qlvm-vif` hotplug binary

Installed at `/etc/xen/scripts/vif-ovn`; invoked by libxl with
`<command> <dev> <domid> <mac> <port>`:

- skip `-emu` devices (HVM guard), as before;
- on `add|online`: read xenstore (`/var/run/xenstore/socket`, small ~50-line
  text-protocol client): `frontend-id` → domain `name`/`uuid`, interface mac →
  then via libovsdb: delete-if-exists + add `<dev>` to `br-int` with
  `external-ids:iface-id=<name>` (+ xen-vm-uuid, attached-mac), set link up
  (netlink);
- on `remove|offline`: delete the port, set link down;
- exit 0/1 per Xen vif-script convention.

## 11. Integration layer

| Concern | Mechanism |
|---|---|
| Xen domain lifecycle | `xen-project/xen` `tools/golang/xenlight` (cgo/libxl, in-process; verified API: `DomainCreateNew` w/ PVH + kernel/ramdisk/extra/vcpus/memory, `DeviceDisk`, `DeviceNic` (mac+script), `DeviceP9`, `DomainDestroy`, `DomainShutdown`, `ListDomain`, `NameToDomid`) |
| OVN topology | `ovn-kubernetes/libovsdb`, custom `OVN_Northbound` models, unix socket `/run/ovn/ovnnb_db.sock` |
| OVS (br-int/br-ex, ports, encap) | `libovsdb`, OVSDB models, `/var/run/openvswitch/db.sock` |
| systemd services | D-Bus `org.freedesktop.systemd1` (godbus) |
| firewalld | D-Bus `org.fedoraproject.Firewalld1` (godbus) |
| NetworkManager | D-Bus API (godbus) |
| container pull/run (image-builder) | podman Go client (unix socket) |
| VM provisioning | `purpleidea/mgmt` |
| low-level SSH (sync-kernel, apps, wait-for-ssh) | `golang.org/x/crypto/ssh` |
| loop devices / mounts | `golang.org/x/sys/unix` (loop ioctls, `unix.Mount/Unmount`); partition UUIDs/types from sysfs |
| NIC link up/down | `vishvananda/netlink` |
| GUI forwarding | `waypipe` binary (local exec, unavoidable) |
| vif hotplug data | xenstore text-protocol client (~50 lines) |

cgo is used (xenlight requires libxl headers at build time).

## 12. Testing (TDD)

Every feature lands test-first.

- **Unit (default `go test ./...`)** — no root, no Xen:
  - domain/subnet parsing, IP/MAC allocation
  - config TOML load/validate/render (idempotent re-write)
  - OVN desired-state builder → expected model objects (switches, LRPs, ACLs,
    NAT, chassis) from a config fixture; firewall rich-rule generation
  - template dir layout + `META` parsing; ostree boot-path computation
  - libxl `DomainConfig` construction (disks/vif/p9/extra strings)
  - `meta.toml` round-trip; ssh-config add/remove (golden files)
  - networkd file rendering (golden)
  - rofi entry emission from a fake cache (golden)
  - desktop-file split (`.desktop` concatenation parsing)
  - xenstore client against an in-memory server
  - CLI wiring: command tree, flag parsing, error paths (cobra `Execute`)
- **Integration (`-tags integration`, need root + Xen dom0)**:
  - `install` twice → second run is a no-op (idempotency)
  - `create` → `start` → SSH round-trip → `sync-kernel` → `apps sync` → `delete`
- **CI** (GitHub Actions): `golangci-lint run` (with `go vet`), `go test
  ./...`, build with cgo enabled (libxl headers in the job image).

## 13. Repo layout

```
qlvm/
├── cmd/
│   ├── qlvm/main.go          # cobra root + subcommands
│   └── qlvm-vif/main.go      # vif hotplug binary
├── internal/
│   ├── cli/                  # cobra commands, one file per subcommand
│   ├── config/               # qlvm.toml load/validate/render
│   ├── xenctl/               # xenlight wrapper + DomainConfig builders
│   ├── ovn/                  # libovsdb OVN_Northbound models + reconciler
│   ├── ovs/                  # libovsdb OVSDB models
│   ├── fw/                   # firewalld D-Bus
│   ├── nm/                   # NetworkManager D-Bus
│   ├── systemd/              # systemd D-Bus helpers
│   ├── template/             # ensureTemplate: podman, image-builder, ostree surgery
│   ├── ostree/               # loop/mount helpers, partition discovery, dep-tree paths
│   ├── vm/                   # create/start/stop/kill/delete/list, meta.toml
│   ├── provisioner/          # mgmt-based provisioning
│   ├── sshx/                 # x/crypto/ssh helpers (sync-kernel, apps, wait)
│   ├── apps/                 # desktop cache + rofi mode
│   ├── mounts/               # FICLONE reflink + p9 spec
│   └── xenstore/             # text-protocol client
├── provision/base/           # packages.txt, dotfiles/ scaffold
├── docs/                     # hardening.md, ovn-gateway-runbook.md (sanitized)
├── .golangci.yml
├── Makefile
├── go.mod
└── README.md
```

## 14. Dropped vs. bash toolset (explicit)

| Dropped | Why |
|---|---|
| Fedora/Alpine cloud images, cloud-init templates, nbd kernel extraction | container-based installs only |
| `bake-template`, alpine-agent template | container images ship the toolkit |
| all NFS (storage VM, mounts, NFS ACLs) | p9 shares via `--mount` |
| `qvm-nfs-manager` | superseded |
| `qvm-harden-dom0` | environment-specific; content → `docs/hardening.md` |
| pyinfra + deploy/*.py + venv install | mgmt |
| `.xl` config files | direct libxl domain config |
| Splunk/llama egress rules, hard-coded IPs, personal usernames | clean repo; egress is config-driven |

## 15. Risks

1. **xenlight is experimental** (Xen-maintained, generated from libxl IDL).
   Mitigation: it is a direct in-process binding with exactly the needed API
   (verified against current generated types); `internal/xenctl` isolates it
   behind one interface so a thin hand-rolled cgo binding is a contained swap.
2. **libovsdb OVN models** must be maintained against OVN schema changes.
   Mitigation: modelgen + schema fetched at connect; integration test pins the
   OVN version.
3. **Dirty xfs log after `kill`** can make the ostree root unmountable during
   `create`. Mitigation: clear error + repair command (accepted trade-off for
   no-exec policy); `stop` is the recommended teardown.
4. **NIC migration drops network** — guarded (SSH detection + confirm),
   same as the bash original.

## 16. Open items (deliberately deferred)

- Harder distinction for `disposable` (e.g. skip template identity bake) —
  only if a real need appears.
- `provision` profiles / extra mgmt operations — mgmt supports it; add when
  needed.
- Multiple image digests per VM name / template GC — first-come.
