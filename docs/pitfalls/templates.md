# Templates (`qlvm template`) Pitfalls

- `vm create` only references: `--template` resolves (exact dir name or
  unique prefix, pure filesystem) and the VM's Image+Digest come from the
  template META — it never pulls or bakes. Baking is `template create
  <ref>`, a deliberate user action.
- `template list` / `delete` never touch podman. `create <ref>` pulls
  first, then: dir absent → reap + bake (~5 min); dir exists → refuse
  unless `--force`; `--force` is the re-bake: flat refusal (no override
  flag) if any VM's `vms/<n>/meta.yaml` (Image+Digest) resolves to the
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
  devices whose backing file under `/var/lib/qlvm` is gone (a force-killed
  VM leaves one attached to `(deleted)` — `losetup -a` shows the suffix,
  parsers must allow it), leftover `/tmp/qlvm-ostree-*` mounts
  (`unix.Unmount`; std has no `os.Unmount`), and exited image-builder
  containers (Podman REST `ancestor` filter + `Exited`).
- Legacy META files can have `image = ""` (pre-`o.Ref` builds); `Ensure`
  backfills it from the ref and re-saves — `template list`'s IMAGE column
  is empty only until the next ensure/create touches the dir.
