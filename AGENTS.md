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
