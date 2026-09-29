# Plan: `vm`/`template` command split — `vm create` references, `template create` bakes

Handoff doc for a fresh session. **The design spec is already updated** to the
target state: `docs/superpowers/specs/2026-09-26-qlvm-design.md` §2/§3/§6.
This plan records *why and how*; the spec records *what the system is*.
Live-smoke pitfalls: `AGENTS.md` (read the "Templates", "Podman",
"OVN / guest network", "Guest SSH / qlvm run", "Xen / build" sections first).

## Why

`qlvm create` currently runs `template.Ensure` (pull + ~5-minute bake) on
every create, so the base image is re-resolved against upstream whenever a
template dir is absent — the user explicitly does not want that: *if the base
image changed upstream, I don't care; a VM is a reference to a bake I
already made*. Baking is a deliberate, expensive, user-triggered act and
belongs in its own command group. The CLI surface also conflates two nouns
(templates and VMs) at one level; split into `qlvm vm …` and
`qlvm template …`, clean break, **no aliases** (confirmed by user
2026-09-30).

## Agreed design (brainstormed 2026-09-30)

Target surface (spec §3):

```
qlvm vm create <name> --template <dir> [--domain] [--type] [--mount]… [--memory] [--vcpus]
qlvm vm start|stop|kill|delete <name>
qlvm vm list
qlvm vm run <vm> [app...]
qlvm vm provision <vm> [--dir]
qlvm vm sync-kernel <vm>
qlvm template [list]
qlvm template create <ref> [--force]
qlvm template clean [--force]
qlvm install
qlvm apps [sync [vm]]            # unchanged, top-level (rofi launcher)
```

Decisions:

- **`--template` is required** and names a template dir (the TEMPLATE column
  of `qlvm template list`): exact dir name or a **unique prefix**; zero or
  multiple candidates → hard error listing what exists. Pure filesystem
  lookup.
- **`vm create` never touches podman** — no pull, no bake. The template's
  META supplies `image` + `digest` for the VM's `meta.toml`
  (`vm.Create` already takes `Digest` from `d.Tpl`; the CLI just sets
  `Spec.Image = tpl.Image`). Missing or incomplete (no META / no
  `template.raw`) template → hard error pointing at
  `qlvm template create <ref>`.
- **`template create <ref>`** (positional ref, replacing `rebuild --image`):
  pull → digest → dir. Dir exists, no `--force` → refuse (a bake is ~5 min;
  never silent). `--force` = today's rebuild path verbatim: flat refusal
  (no override flag) if any VM references the dir, reap residue, remove,
  bake. Reap also runs on the plain create path (a killed prior bake leaves
  a loop on a deleted file that breaks template attach — AGENTS.md pitfall).
- `template list` / `template clean` unchanged.
- `rebuild` is gone. Old flat top-level VM commands are gone (no aliases).

## Current state (verified 2026-09-30, at push 0baeb8a)

- `internal/cli/template.go` — `template` parent with `list` (default),
  `rebuild --image`, `clean [--force]`; helpers `reapBuilderResidue`,
  `removeStaleBakeMounts`, `templateRefs` (VM-reference scanning).
- `internal/cli/create.go` — `createCmd` (top-level) with required
  `--image`; owns `ensureTemplate` (podman wiring + `template.Ensure` +
  `ostree.BakeTemplate`), `podmanSocket`, `fstype`, and `sshAuthKeys()`.
- `internal/cli/lifecycle.go` — top-level `start/stop/kill/delete/list`;
  `start` loads kernel/initramfs via `template.LoadByRef(installRoot,
  m.Image, m.Digest)` — untouched by this change.
- `internal/vm/create.go` — `Spec{Image,…}`, `CreateDeps{Tpl,…}`; no
  changes needed there.
- `internal/cli/root_test.go` — command-tree expectations will need the new
  shape. `run`/`provision`/`sync-kernel`/`apps` files: only `init()`
  registration + `Use:` strings change.
- **Dom0 state**: template `jcpowermac-os-bolt-sha256:72e8ffbb…e` exists,
  complete, **unreferenced** (no VMs). So the live smoke of `vm create` can
  reference it without any bake; a `template create --force` bake is a 5-min
  optional extra check.
- `HANDOFF.md` (2026-09-29) is stale after this work — update or delete.

## Tasks

1. **Rename to `vm` group** — new `vmCmd()` parent in `internal/cli`
   (or extend `root.go`); move `create/start/stop/kill/delete/list/run/
   provision/sync-kernel` registration under it; `Use:` lines drop their
   parent; drop the old top-level registrations. Keep `apps`/`install`
   top-level. Update `root_test.go`.
2. **`vm create` rewrite** (`internal/cli/create.go`): drop `--image` +
   `ensureTemplate` call; add required `--template`; new
   `resolveTemplate(root, name) (dir, error)` — exact dir match, else
   unique prefix over existing dirs, else error listing candidates; load
   META (incomplete → hard error naming the dir + `qlvm template create`);
   set `Spec.Image` from the template META. `podmanSocket`/`fstype`/
   `sshAuthKeys()` move to `template.go` (they serve the bake path only).
3. **`template create`** (`internal/cli/template.go`): replace `rebuild`
   with `create <ref>` (positional) + `--force`; share the existing
   ensure/bake code path; refuse-if-exists without `--force`; keep the
   VM-referenced flat refusal + reap on `--force`; also reap on the plain
   path before baking.
4. **Tests** (TDD, fake-FS table style like the existing suites):
   `resolveTemplate` (exact / unique prefix / ambiguous / missing /
   incomplete-dir error shape); `template create` decision paths (exists
   without force / exists+referenced with force / fresh); command-tree test
   for the new surface; existing create/lifecycle tests updated to the new
   flag names.
5. **Docs** — spec §3/§6 already done; README Quick start (new surface,
   `--template`, the bake-vs-reference story); AGENTS.md "Templates
   (qlvm template)" section (`rebuild` → `create --force`); delete/replace
   `HANDOFF.md`; commit on `main`.
6. **Live smoke** (dom0, see Definition of done 3–4).

## Pitfalls this change specifically can step on

- **Rebuild the installed binary after ANY code change**: `sudo make
  container-build` → `/usr/local/bin/qlvm`. Repo `bin/qlvm` is the no-libxl
  stub and cannot talk to Xen.
- A bake costs ~5 minutes: `timeout 600+` on any command that may bake; a
  killed bake leaves the image-builder podman container RUNNING as root
  (`podman ps -a`, `podman rm`) — `template create`'s reap handles it.
- `run` is a user-session command (waypipe needs WAYLAND_DISPLAY); the
  smoke's `run` line runs as the desktop user, the `sudo` lines as root.
- `start` resolves the template from `meta.toml` Image+Digest — after the
  change both come from the template META, so the dir must stay consistent;
  don't rename dir layout.
- Host `go build ./...` fails on this machine (gpgme/btrfs headers); use
  `make test` / `make lint` for unit work.
- No shelling out (podman via the existing REST client in
  `internal/template`), no vendor names, no real IPs.

## Definition of done

1. `qlvm vm list`, `qlvm template list` work; the old top-level names
   (`qlvm create`, `qlvm list`, `qlvm rebuild`…) are unknown commands; no
   alias paths exist.
2. `qlvm template create <ref>` refuses when the dir exists; `--force`
   refuses while a VM references the dir and bakes (~5 min, `timeout 600+`)
   when nothing does.
3. `qlvm vm create t1 --template <prefix>` completes in seconds against the
   existing unreferenced bolt template (no pull, no bake — verify with
   `podman ps` quiet / strace-free, and the template dir mtime unchanged),
   writes `meta.toml` with the template's Image+Digest.
4. Full lifecycle on the smoke VM: `sudo qlvm vm create t1 --template
   … && sudo qlvm vm start t1` → `ping -c3 <t1-ip>` (ttl=63) →
   `sudo ovn-sbctl find Port_Binding` (`up:true`, chassis, MAC) →
   `qlvm vm run t1 true` (desktop user) → `sudo qlvm vm kill t1 && sudo
   qlvm vm delete t1`.
5. `make test`, `make lint`, `sudo make container-build` green.
6. README + AGENTS.md + HANDOFF.md current; commit on `main`; push via
   ssh-on-443 (port 22 blocked here): `GIT_SSH_COMMAND="ssh -p 443" git push
   ssh://git@ssh.github.com:443/jcpowermac/qlvm.git main` — after 3–4 pass
   live.

## Pointers

- `internal/cli/create.go` — `ensureTemplate`, `sshAuthKeys`, `fstype`
- `internal/cli/template.go` — `rebuild` (becomes `create`), reap helpers,
  `templateRefs`
- `internal/cli/lifecycle.go` — the `start…list` commands that move under
  `vm`; start's `template.LoadByRef` (unchanged)
- `internal/vm/create.go` — `Spec`/`CreateDeps` (unchanged)
- `internal/cli/root_test.go` — command-tree expectations
- `docs/superpowers/specs/2026-09-26-qlvm-design.md` §3/§6 — target state
