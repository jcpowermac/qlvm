# qlvm

Go toolset for managing Xen app/disposable VMs from an ostree Fedora dom0:
`qlvm` (CLI, D-Bus client), `qlvm-agent` (root daemon, D-Bus server),
`qlvm-gui` (Fyne GUI, D-Bus client), `qlvm-vif` (Xen vif hotplug script).
Usage: `README.md`. Original rewrite brief: `rewrite-golang.md`.

## Hard constraints

- **Always rebuild the binaries after any change**: `sudo make container-build`
  before re-running live commands. The stubs in `bin/` cannot talk to Xen —
  only `/usr/local/bin/qlvm*` (the cgo builds) can. If you changed code and
  ran a live command without rebuilding, the results are from the OLD binary
  and you are debugging ghost behavior.
- **Container build is the completion gate**: Never mark work complete, claim
  tests pass, or commit code changes without first running
  `sudo make container-build` and verifying it succeeds. The local `go build`
  without the `libxl` tag is NOT sufficient — it skips the cgo xenlight
  binding and will miss build errors that only surface with `libxl`. The
  container build (`make container-build`) compiles against the builder
  image's Xen headers with `libxl` enabled; it is the only reliable
  verification that the code actually compiles for the target environment.
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
- **Don't `systemctl restart dbus` under a live graphical session.** The
  system D-Bus bus is a long-lived connection for sway, the greetd login
  manager, and other session daemons; restarting the daemon drops every
  live peer, which can kill the running sway session and the login
  manager (recurring pitfall — it has broken this dom0's UI before). When
  a D-Bus *policy* change (`/etc/dbus-1/system.d/*.conf`) must take
  effect, prefer restarting only the affected service (e.g.
  `qlvm-agent`); if you must restart the bus itself, do it from a TTY/VGA
  (not the graphical session) or after logging out. `busctl call`
  verifies methods without touching the bus lifecycle.
- **TEST against the live system, not just the build.** A successful
  `make container-build` means the code compiles — it does NOT mean the
  code works. Every D-Bus method, systemd unit, and integration point must
  be verified against the live dom0:
  - D-Bus methods: call each one with `busctl call` and check the response
    (don't assume ExportAll works — verify with `busctl call`, not just
    `busctl list` or `busctl introspect` which godbus doesn't support).
  - CLI commands: run every affected `qlvm` subcommand end-to-end.
  - systemd units: test stop/start/restart/failure-recovery (SIGKILL the
    daemon, verify it auto-restarts).
  - **Never mark work complete without live verification.** The
    `Pin *bool` godbus marshaling bug (found 2026-10-16) passed all
    builds and unit tests but hung every `GetConfig` call silently —
    only a live `busctl call` would have caught it. This is not a
    hypothetical: it cost a full session of debugging.

## Docs

- Design spec: `docs/superpowers/specs/2026-09-26-qlvm-design.md`
  (source of truth for architecture, CLI surface, VM model)
- Implementation plan: `docs/superpowers/plans/2026-09-26-qlvm-implementation.md`
- D-Bus daemon + GUI spec: `docs/superpowers/specs/2026-10-15-qlvm-ui-design.md`
- D-Bus daemon plan: `docs/superpowers/plans/2026-10-15-qlvm-dbus-daemon.md`
- Dom0 hardening guide: `docs/hardening.md` (rpm-ostree-based)
- Build/usage: `README.md` (Quick start, Development sections)
- Agent VM (pi-in-xen): `../pi-in-xen/` (Alpine initramfs-based agent VMs)

## Pitfalls (by subsystem)

Read the relevant file when touching that subsystem. Unit fakes and the
in-memory ovsdb server cannot catch these; each cost a live round-trip.
**General rule: every D-Bus/OVSDB method call must be verified against the
live service (busctl introspect / a scratch object), never against memory
or older docs.**

| Topic | File |
|-------|------|
| Waypipe control channel (`vm run --connect`) | `docs/pitfalls/waypipe-control-channel.md` |
| OVSDB (libovsdb) | `docs/pitfalls/ovsdb-libovsdb.md` |
| D-Bus (godbus) / firewalld / networkd | `docs/pitfalls/dbus-godbus.md` |
| OVN / guest network | `docs/pitfalls/ovn-guest-network.md` |
| Guest SSH / `qlvm vm run` | `docs/pitfalls/guest-ssh-vm-run.md` |
| CI (GitHub Actions) | `docs/pitfalls/ci-github-actions.md` |
| Xen / build | `docs/pitfalls/xen-build.md` |
| In-guest reboot → zombie `---sr-` | `docs/pitfalls/in-guest-reboot-zombie.md` |
| Podman (pkg/bindings REST) | `docs/pitfalls/podman-rest.md` |
| Templates (`qlvm template`) | `docs/pitfalls/templates.md` |
| D-Bus daemon (`qlvm-agent`) | `docs/superpowers/specs/2026-10-15-qlvm-ui-design.md` §6 |
