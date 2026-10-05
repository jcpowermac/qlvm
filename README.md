# qlvm

Qubes-like VM isolation on dom0: a native Go toolset that manages isolated,
subnet-scoped VM domains with per-domain gateways, a single OVN-backed SNAT
router to the physical LAN, and a declarative dom0 egress firewall —
configured through a single TOML file.

`qlvm` is one cobra binary (`cmd/qlvm`) plus a small libxl hotplug helper
(`cmd/qlvm-vif`, installed to `/etc/xen/scripts/vif-ovn`). It talks to every
control plane through libraries — OVN/OVS via libovsdb, firewalld and
systemd (networkd/system units) over D-Bus, Xen via xenlight — and never shells out to a
management CLI. The only local process it launches is `waypipe` (for
`qlvm vm run` GUI sessions).

## Quick start

```sh
# 1. Build (dom0: containerized cgo build, no dev packages on the host;
#    writes qlvm + qlvm-vif + bundled libyajl.so.2 to /usr/local/bin)
sudo make container-build

# 2. First install: writes /etc/qvm/qlvm.toml and drives the dom0
#    (OVS bridges, OVN router/switches, firewall, networkd config,
#    services, storage tree, vif-ovn script) toward the declared state
sudo qlvm install

# 3. Edit the config (NIC, gateway, domains, VM defaults, egress policy)
$EDITOR /etc/qvm/qlvm.toml
sudo qlvm install                   # reconciliation: edit + re-run is the change path

# 4. Bake a template once: pull the image + ostree bake (~5 min).
#    A bake is deliberate and user-triggered; VMs never trigger one.
sudo qlvm template create quay.io/fedora/fedora:44

# 5. Create a VM by REFERENCE (dir name or unique prefix — no pull, no
#    bake), and boot it
sudo qlvm vm create work-1 --domain work --template fedora
sudo qlvm vm start work-1
qlvm vm run work-1 xterm            # GUI via waypipe (dom0 Wayland session required)

# 6. Provision it (dotfiles from the layered provision dir)
qlvm vm provision work-1
```

If the upstream image changes, the existing template dir keeps its digest —
a VM is a reference to a bake you already made; re-bake deliberately with
`qlvm template create <ref> --force`. `qlvm vm list` shows VMs (name, type,
state, mem, vcpus); `qlvm vm delete work-1` tears the VM down completely
(Xen domain, OVN/OVS ports, state dir, ssh config block).

## Subcommands

| Command | What it does |
|---|---|
| `qlvm install` | Idempotent dom0 orchestration: drives OVS, OVN, firewalld, systemd-networkd (and masks NetworkManager), systemd services, the `/var/lib/qvm` storage tree, and `/etc/xen/scripts/vif-ovn` toward the state declared in the config. `--config PATH`, `--skip-nic-migration` |
| `qlvm vm create <name> --domain <d> --template <dir>` | Reference a baked template (exact dir name or unique prefix — pure filesystem lookup, never a pull or bake; `qlvm template create` first), then prepare the VM: OVN/OVS ports, reflinked disk, `meta.toml` (image+digest from the template META). `--type app\|disposable`, `--mount host:guest` (repeatable, p9), `--memory MB`, `--vcpus N`, `--config PATH` |
| `qlvm vm start <name>` | Boot a prepared VM (seeds the per-VM kernel files from the template dir if missing, cleans stale OVS vif ports first) |
| `qlvm vm stop <name>` | Graceful shutdown |
| `qlvm vm kill <name>` | Force destroy |
| `qlvm vm restart <name>` | Graceful stop, force-kill if it lingers, then start |
| `qlvm vm delete <name>` | Delete everything: Xen domain, OVN/OVS ports, state dir, ssh config block |
| `qlvm vm list` | name, type, state, mem, vcpus (running from Xen, stopped from `meta.toml`) |
| `qlvm vm run <vm> [app...]` | Run an app in the VM's GUI via waypipe (needs a dom0 Wayland session). `--connect ssh` (default) \| `tcp` \| `vsock` picks the channel |
| `qlvm vm provision <vm>` | Sync the layered `dotfiles/` into the VM's home over sftp. `--dir PATH` (default `/etc/qvm/provision`, with `base/` + per-vm layers). System packages are not provisioned — the VM root is an ostree deployment from the bootc container image (dnf disabled); extend the image for extra packages |
| `qlvm vm sync-kernel <vm>` | Fetch the VM's current kernel/initramfs from the VM's `/boot` into the VM's own state dir (`vms/<name>/`) so a restart picks up a kernel the VM upgraded in place. Per-VM: a sibling VM of the same template keeps its own kernel; the template dir is never touched |
| `qlvm template [list]` | List baked templates (dir, image, kernel, size, which VMs reference each); warns on incomplete dirs |
| `qlvm template create <ref> [--force]` | Pull + bake an image's template (~5 min) into `templates/<slug>-<digest>/`. Refuses if the dir already exists; `--force` is the re-bake: flat refusal while any VM references the dir, otherwise remove + bake. Reaps a killed bake's residue before baking on both paths: stale loop devices, leftover `/tmp/qlvm-ostree-*` mounts, exited image-builder containers |
| `qlvm template delete [dir...] [--force]` | Remove the named template dirs (exact dir name or unique prefix; flat refusal while a VM references one — delete the VMs first). With no args, removes every dir no VM references (a `vm create` that wants one fails until `qlvm template create <ref>` re-bakes it); `--force` also removes incomplete dirs (no META) |
| `qlvm apps` | rofi launcher: serves a menu of the cached VM desktops (rofi mode, `ROFI_RETV`) and launches the selection. Run rofi with `-field 4` so `ROFI_INFO` carries the selected `<vm>\|<exec>` |
| `qlvm apps sync [vm]` | Refresh the desktop-file cache from the VM(s) |

**In-guest reboot is unsupported.** A reboot issued from inside the guest
leaves the Xen domain a zombie: `xl list` shows `---sr-`, vCPUs halted,
no ping/SSH, and the ACPI stop never answers — the dom0 runs no toolstack
process to consume the guest's reboot event, so no libxl `on_reboot` policy
can ever fire. Recover with `qlvm vm restart <name>`: its graceful stop
does not wait for the zombie (libxl sends the ACPI event and returns),
so the command polls for ~60 s, force-kills if it lingers, then starts —
on a zombie, allow ~60–90 s before it returns (live-verified against a
real `---sr-` zombie, 2026-09-30). For a manual low-level recovery, use
`sudo xl destroy <name>` + `qlvm vm start <name>`. Plain `systemctl reboot`
in the guest was shown to hang deterministically (live evidence in
`AGENTS.md`, "Live-smoke pitfalls").

## Shared mounts

`vm create --mount host:guest` exports `host` into the guest as a 9p share (Xen
`xen9pfsd`, `security_model=none`). Each share gets a unique 9p tag
`<vm>-<i>` (0-based in `--mount` order). At create time qlvm bakes one
systemd unit per share into the VM disk's deployment `/etc` overlay — named
after the guest path with `/` runs collapsed and a `-<i>` suffix, e.g.
`etc/systemd/system/var-lib-qvm-data-0.mount` — with `Type=9p`,
`Options=trans=virtio`, `What=<tag>`, `Where=<guest path>` (a relative
guest path is mounted at `/<guest>`), `_netdev`, and an enabling symlink in
`multi-user.target.wants`, so the share is mounted at boot by systemd. There
is no kernel-cmdline mechanism involved.

## Configuration

Single source of truth: `/etc/qvm/qlvm.toml` (TOML). `install` writes it;
editing it + re-running `install` is the change path (reconciliation).

```toml
[network]
nic = "enp1s0"                 # physical NIC (install moves it into OVS br-ex)
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

## Architecture

```
 VM (10.100.1.10) --[vifN.0]--> br-int -- OVN logical switch per domain
                                            (per-domain subnet 10.100.x.0/24,
                                             per-domain gateway 10.100.x.1)
                                            |
                       OVN gateway router -- SNAT to the LAN IP (192.168.1.200)
                                            |
                                 br-ex --> enp1s0 --> LAN (192.168.1.0/24)

 dom0 egress: firewalld rich rules generated from [firewall.egress]
 GUI:         qlvm vm run <vm> <app> -- waypipe over ssh|tcp|vsock (dom0 Wayland session)
 hotplug:     libxl invokes /etc/xen/scripts/vif-ovn (qlvm-vif), which reads
              xenstore and programs the OVS vif port via libovsdb
```

Each domain is an isolation unit: its own OVN logical switch and subnet.
VMs route to their domain gateway; inter-VM traffic crosses domain
boundaries only through the gateway router. The dom0's own egress is
restricted by the `[firewall.egress]` policy — SSH is confined to the VM
supernet.

## Repo layout

```
qlvm/
├── cmd/
│   ├── qlvm/main.go            # entrypoint; cobra tree lives in internal/cli
│   └── qlvm-vif/main.go        # vif hotplug binary (reads xenstore, writes OVSDB)
├── internal/
│   ├── cli/                    # cobra commands, one file per subcommand
│   ├── config/                 # qlvm.toml load/validate/save
│   ├── setup/                  # install: idempotent orchestration of the planes
│   ├── xenctl/                 # xenlight wrapper (libxl cgo; stub without it)
│   ├── xenstore/               # xenstore text-protocol client (for vif-ovn)
│   ├── ovn/                    # libovsdb OVN_Northbound models + reconciler
│   ├── ovs/                    # libovsdb Open_vSwitch models + reconciler
│   ├── fw/                     # firewalld over D-Bus (rich-rule egress policy)
│   ├── netd/                   # systemd-networkd drop-ins (DHCP on NIC + br-ex)
│   ├── systemd/                # systemd over D-Bus (enable/start units)
│   ├── template/               # template ensure: podman pull, ostree bake
│   ├── ostree/                 # template bake: loop/mount, partition discovery
│   ├── vm/                     # create/start/stop/kill/delete/list, meta.toml
│   ├── provisioner/            # dotfile sync over sftp (Runner seam)
│   │                           #   + sftp dotfile upload (Runner seam)
│   ├── sshx/                   # x/crypto/ssh client (auth, wait, run, fetch)
│   ├── apps/                   # desktop-file cache + rofi menu
│   ├── mounts/                 # FICLONE reflink + p9 mount specs
│   └── itest/                  # real-plane integration tests (build tag: integration)
├── provision/base/             # default provision dir: dotfiles/
├── docs/                       # hardening.md, ovn-gateway-runbook.md
└── .github/workflows/ci.yml
```

## Development

```sh
make build          # bin/qlvm + bin/qlvm-vif
make test           # unit tests
make lint           # golangci-lint v2.14.0 (gosec, errcheck, revive)
make test-integration  # real-plane tests; touch the live dom0 (see internal/itest)
```

- The Makefile passes `GO_TAGS`: `exclude_graphdriver_btrfs
  exclude_graphdriver_zfs containers_image_openpgp` — the podman
  `pkg/bindings` dependency pulls storage graph drivers and gpgme that need
  cgo headers absent on a bare dom0; qlvm only talks to podman over its
  REST socket. Where the Xen dev packages exist the Makefile also adds the
  `libxl` tag so the real xenlight binding compiles; without them a stub
  builds cgo-free and lifecycle commands fail at runtime with a rebuild
  hint.
- Container build (the dom0 path): `sudo make container-build
  [OUT=/usr/local/bin]`
  compiles the real cgo binding inside a cached Fedora 44 container
  (`Dockerfile.builder`; dnf works in a plain container root where the
  ostree dom0 forbids it) and writes the binaries plus a bundled
  `libyajl.so.2` (loaded via an `$ORIGIN` rpath, so the dom0 gains no
  packages) to `OUT`. The container's Xen headers must be at or below the
  dom0's Xen runtime — the produced binary loads the dom0's
  `libxenlight.so.*` — so rebuild the builder image
  (`make container-builder`) after either side upgrades Xen. The Go
  toolchain and module cache persist in `~/.cache/qlvm-build`; repeat
  builds take seconds.
- TDD: every internal package has table-driven unit tests alongside the
  implementation; control planes are injected as interfaces.
- Integration tests (`internal/itest`, build tag `integration`) run
  `install` against the live planes twice to prove idempotency by
  OVN/OVS row counts, and a full create/start/ssh/delete cycle guarded by
  `QVM_ITEST=1` + `QVM_ITEST_IMAGE`.
- Linting: `make lint` runs
  `golangci-lint@v2.14.0` pinned in the Makefile; config in `.golangci.yml`.
