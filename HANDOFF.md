# Handoff — qlvm template subcommand (2026-09-29)

Session restarted; this doc orients the next one. Delete when stale.

## Where things stand

- **Work complete, committed to `main` as `6e82add`** — the only unpushed
  commit (origin = `git@github.com:jcpowermac/qlvm`). **Push is the one
  remaining step; it needed user confirmation, which has not been given.**
  Push via the ssh-on-443 path per the plan
  (`docs/superpowers/plans/2026-09-29-qlvm-template-subcommand.md`).
- Plan doc: `docs/superpowers/plans/2026-09-29-qlvm-template-subcommand.md`
  (task 246b0d4 is its handoff docs commit).
- `make test` and `make lint` clean at commit time.

## What was implemented

`qlvm template` subcommand (cache lifecycle for baked OS templates):

- `template [list]` (default): table of `/var/lib/qvm/templates/*` — dir,
  image, kernel, size, which VMs reference it (resolved from
  `vms/*/meta.toml` Image+Digest). Incomplete dirs (no META / no
  template.raw) get stderr warnings; empty dir → "no templates", exit 0.
- `template rebuild --image <ref>`: **flat refusal** (no --force) if any VM
  references the target dir. Otherwise: reap → remove dir → full ensure
  (pull + image-builder + bake, ~5 min).
- `template clean [--force]`: removes complete unreferenced dirs; `--force`
  also removes incomplete ones. Next `create` re-bakes.
- **Reap step** (before rebuild): stale loop devices whose backing file
  under `/var/lib/qvm` no longer exists (incl. the `losetup -a`
  `(deleted)` suffix), leftover `/tmp/qlvm-ostree-*` scratch mounts
  (`unix.Unmount` — Go stdlib has no `os.Unmount`), exited image-builder
  containers (Podman REST `ancestor` filter + `Exited`). Best-effort;
  failures warn, rebuild proceeds.

Files: `internal/cli/template.go` + `template_test.go` (new),
`internal/cli/create.go` (ensure extraction),
`internal/template/template.go` (`Podman` interface +=
`ListBuilderContainers`/`RemoveContainer`, `ReapBuilderContainers`,
`DirOfRef`, `NormalizeDigest` export, META Image backfill) + tests,
`internal/ostree/surgery.go` (`MountPrefix` exported), README, AGENTS.md.

## Bugs found during live smoke (all fixed in 6e82add)

1. **`losetup -a` `(deleted)` suffix broke the loop-line regex** —
   force-killed VMs leave a loop attached to a deleted file; rebuild's
   reap would have missed it. Fixed: regex group 2 + unit test.
2. **Empty `image = ""` in template META** — the ostree bake enricher
   re-persists META without the Image field (legacy behavior, not
   introduced here). `Ensure` now backfills `Image` from the ref and
   re-saves when the baked META has an empty one. Pre-existing dom0 dirs
   show an empty IMAGE column until their next ensure/rebuild.

## Live dom0 state (after smoke, 2026-09-29)

- Template `jcpowermac-os-bolt-sha256:72e8ffbb…e` freshly re-baked 3x,
  now with `image = "ghcr.io/jcpowermac/os-bolt:latest"` in META.
  **Unreferenced — no VMs exist** (smoke and d1 deleted; smoke was the
  leftover VM from the prior session's run work).
- `/usr/local/bin/qlvm` is current (last `sudo make container-build` ran
  after all fixes).
- Stale `boot`/`cloud-init` template dirs removed via `clean --force`.
- No stale loops (`losetup -a` empty), no stale LSPs.
- Verified end-to-end: create → start (ping ttl via OVN, Port_Binding
  `up:true`, `vif<domid>.0` in OVS) → run (ssh + waypipe reached guest;
  `gnome-calculator` is simply not in the os-bolt image) → kill → delete.

## If resuming

1. Push: confirm with user, then push main (one commit).
2. If touching the template control plane: read the new `### Templates
   (qlvm template)` section in AGENTS.md.
3. After ANY code change, `sudo make container-build` before live runs —
   the repo `bin/qlvm` is the no-libxl stub.
4. Full re-bakes cost ~5 min: `timeout 600+` on commands that may bake; a
   killed create leaves an image-builder podman container RUNNING as
   root (`podman ps -a`, `podman rm` to clean) — which is exactly what
   `template rebuild` now reaps.
