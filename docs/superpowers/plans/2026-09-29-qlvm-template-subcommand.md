# Plan: `qlvm template` subcommand (cache lifecycle for baked templates)

Handoff doc for a fresh session. Source-of-truth design spec:
`docs/superpowers/specs/2026-09-26-qlvm-design.md`. Live-smoke pitfalls:
`AGENTS.md` (read the "Podman", "OVN / guest network", "Guest SSH / run",
"Xen / build" sections before touching anything here).

## Why

`qlvm create` runs `template.Ensure` every time: skopeo-pull of the bootc
image + (when the digest dir is absent) a ~5-minute image-builder container
build + ostree bake, writing ~10 GB. Iterating VMs re-pulls always and
re-bakes whenever a template dir was deleted, for no reason — wasted time
and NVMe wear. Templates should be first-class, inspectable state.

## Current state (verified 2026-09-29)

- Template dir: `/var/lib/qvm/templates/<slug>-<digest>/` where slug =
  image ref minus registry/path (e.g. `jcpowermac-os-bolt-sha256:9bc2...`).
  Contents after success: `template.raw`, `META` (JSON: Dir, Digest,
  Image, KernelVer, RootDev, RootFlags, OstreePath), `vmlinuz`, `initramfs`,
  plus image-builder `buildlog`/`osbuild-manifest.json`.
- Cache key: `Ensure` skips bake when `META` for the ref's current digest
  exists (`internal/template/template.go`, `DirFor`/`LoadMeta`/`LoadByRef`).
- `internal/cli/create.go` wires: `template.NewPodman(ctx,
  "unix:///run/podman/podman.sock")` → `template.Ensure(ctx, pod,
  EnsureOpts{Root: "/var/lib/qvm", Ref: image, Log: stdout, Bake:
  ostree.BakeTemplate(ctx, fs, dir, "xfs", sshAuthKeys())})`.
- **`start` needs the template dir**: it loads kernel/initramfs via
  `template.LoadByRef(installRoot, m.Image, m.Digest)`
  (`internal/cli/lifecycle.go`). VMs keep `Image` + `Digest` in
  `vms/<name>/meta.toml`. A template dir referenced by any VM must NOT be
  deleted (or those VMs become unstartable).
- An aborted/timeout-killed `create` can leave: a template dir with only
  buildlog/manifest (no `template.raw`/META) and a still-running
  image-builder podman container (`sudo podman ps -a`). Cleanup must cope.

## CLI surface (add to spec §"Command surface" in a follow-up edit)

```
qlvm template list                      # default
qlvm template rebuild --image <ref>     # force fresh pull + bake
qlvm template clean [--force]           # drop unreferenced template dirs
```

- **list**: table of `NAME-DIGEST  IMAGE  KERNEL  SIZE  REFERENCED-BY-VMs`
  (walk `/var/lib/qvm/templates/*/META` via `template.LoadMeta`; scan
  `vms/*/meta.toml` for `Image`+`Digest` matches). Warn on incomplete dirs
  (no `template.raw`).
- **rebuild**: compute ref digest, remove that digest's dir (refuse if any
  VM references it unless `--force`... simpler: refuse outright, tell user
  to delete VMs first — VMs reflink from the raw so the OLD dir stays
  usable by existing VMs; deleting it only breaks *starting* those VMs),
  then run the same `template.Ensure` path `create` uses. Extract the
  Ensure+Bake wiring from `internal/cli/create.go` into one exported-ish
  helper (e.g. `internal/cli` func `ensureTemplate(ctx, out, image)`) so
  `create` and `template rebuild` share it — one seam, no new abstraction.
- **clean**: delete template dirs no VM meta references. `--force` also
  deletes incomplete (no `template.raw`) dirs. Print what it removes.

No new packages: reuse `internal/template` (Dir/Load/Ensure),
`internal/vm` (LoadMeta over `vms/*`), cobra wiring in `internal/cli/`
(new `template.go`, register in `init()` like the other commands).

## Pitfalls this feature specifically can step on

- Template dir names carry the digest captured AT BAKE TIME (config-digest
  vs manifest-digest confusion has already bitten once: `ls templates/`
  before `rm`; never reconstruct the name from a fresh pull's manifest).
- `rebuild` is ~5–7 min cold: keep `Log` streaming progress (the
  image-builder "still baking" lines come from `template.Ensure` already)
  and never wrap the command in a short timeout; a killed bake leaves the
  orphan states listed above. `rebuild` should first offer to reap:
  stale loops (`losetup -a` attached to deleted files under
  `/var/lib/qvm`), leftover `/tmp/qlvm-ostree-*` mounts, and exited
  image-builder containers.
- `sudo qlvm create` must keep working from a root shell — the
  `sshAuthKeys()` collection (root + `SUDO_USER` pubkeys) is part of the
  Bake closure; extract it along with the closure, don't duplicate.
- Host `go build ./...` fails (gpgme/btrfs headers missing — ostree host).
  Use `make test` / `make lint` (tag-set `exclude_graphdriver_btrfs
  exclude_graphdriver_zfs containers_image_openpgp`) for unit work;
  `sudo make container-build` to install into `/usr/local/bin`.
- Repo hygiene: no vendor names, no real IPs (10.100.x.x virtual net OK),
  no shelling out to podman/ovs/xl CLIs (podman via existing
  `internal/template` REST client only).

## Definition of done

1. `qlvm template list` shows the current bolt template + which VMs
   reference it; exits 0 on an empty templates dir.
2. `qlvm template rebuild --image ghcr.io/jcpowermac/os-bolt:latest`
   produces a fresh complete dir; `qlvm create` right after takes seconds
   (no pull-bake stall) and the VM lifecycle still passes:
   `sudo qlvm create --type disposable t1 && sudo qlvm start t1 &&
   qlvm run t1 true && sudo qlvm kill t1 && sudo qlvm delete t1`
   (`run` line executes as the desktop user).
3. `qlvm template clean` removes a synthetic unreferenced dir and refuses
   to touch a referenced one (unit-test both with fake dirs).
4. Unit tests for list/clean/rebuild decision logic (dir-name/marker-file/
   reference scanning) — follow existing fake-FS table-test style in
   `internal/cli/*_test.go` + `internal/template/template_test.go`.
5. `make test`, `make lint`, `sudo make container-build` green.
6. README Quick start gains the three commands; AGENTS.md gets a pitfall
   line if anything non-obvious surfaced.
7. Commit on `main`; push via ssh-on-443 (port 22 is blocked from this
   host): `GIT_SSH_COMMAND="ssh -p 443" git push
   ssh://git@ssh.github.com:443/jcpowermac/qlvm.git main`. Do not push
   until 2 passes live.

## Pointers

- `internal/template/template.go` — Ensure/DirFor/LoadMeta/LoadByRef
- `internal/cli/create.go` — wiring + `sshAuthKeys()` (extract here)
- `internal/cli/lifecycle.go` — `vmDirOf`, start's `LoadByRef` use
- `internal/vm/meta.go` — Meta{Image, Digest} persisted per VM
- `AGENTS.md` "Live-smoke pitfalls" — the machine rules of this repo
