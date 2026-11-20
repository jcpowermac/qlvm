# `agent` binary design — cattle VMs launched like an app

Date: 2026-11-20

## Intent

The `agent` VM type is currently a **pet**: named identity, a per-VM 9p home
that carries pi's config (auth, model, settings), a first-boot `/login`
ritual, and separate create/start/delete ceremonies. The goal is a
**container-like, cattle experience securely wrapped in virtualization**:
launching an agent VM should feel like running a program on the dom0 —
one command, attaches to your terminal, dies with the command — while pi
sessions survive for later `/resume` or host-side inspection.

The existing three VM types stay: **persistent** (pet), **agent** (cattle),
**disposable** (cattle). This design changes only the agent path.

## Goals

- A new standalone binary `agent` on the user's PATH. Full surface:
  `agent [name]`. Nothing else.
- Launch = create + config-inject + start + attach, in one foreground
  process. The VM's lifetime is scoped to the command.
- No per-VM pi config state: auth/model/settings are injected from a
  single host-local dir at every launch. A fresh VM boots ready — no
  `/login`.
- pi **sessions** (and any files under the 9p home) survive stop/delete,
  at a predictable dom0 path, keyed by VM name.
- `qlvm vm list` / `stop` / `delete` remain the fleet-management surface;
  `agent` does not grow subcommands.

## Non-goals

- No `agent stop|list|delete` subcommands (use `qlvm vm ...`).
- No GUI/console-UI rework; the existing console protocol stands.
- No changes to persistent or disposable VM types.
- No per-VM distinct credentials in v1 (one shared host config dir).
- No `--detach` in v1 (background launches can be a later flag).

## Decided design

### 1. Binary surface

```
agent [name]
```

- `name` optional. If given and a VM of that name exists (any state), the
  command reuses it: start if stopped, attach if running. If not given,
  `agent` picks the next free `agent-N` (N = 1, 2, 3, ...).
- Foreground. The console attaches to the caller's TTY in raw mode
  (same UX as `sudo qlvm vm console`): Ctrl-C goes to the guest; **Ctrl-]
  detaches**, which `agent` treats as exit — it issues a graceful ACPI
  stop so the command returning == the VM going down. `--detach` is a
  documented non-goal for v1.
- Exit code: 0 on clean stop, non-zero if create/start failed (with the
  VM left in its failed state for `qlvm vm list` to show).

### 2. Config injection (host-local)

- Canonical dir: `~/.qlvm/agent/` (per-user, on the dom0). Contents are
  plain pi config files: `auth.json`, `settings.json`,
  `models-store.json` (any file present is copied; absent files are
  left untouched).
- At launch, `agent` copies the dir's contents into the VM's 9p home at
  `<home>/.pi/agent/`, **overwriting existing files of the same name,
  never deleting anything else** — so `sessions/` and any work files are
  untouched. Overwrite happens on **every** launch: the host dir is the
  single source of truth, and a host-side credential rotation
  propagates on the next launch with no per-VM drift.
- The 9p home stays where it is today:
  `/var/lib/qlvm/9p/<name>/data` (guest `/var/data` = pi's `$HOME`).
  Sessions therefore live at the predictable path
  `/var/lib/qlvm/9p/<name>/data/.pi/agent/sessions/` — browsable from the
  host or a host-side agent; a relaunch of the same name re-mounts it and
  pi's `/resume` finds prior sessions.
- Sync before delete needs no special handling: the guest mounts
  `trans=xen,cache=none` (write-through) and the dom0 daemon writes
  straight into the host dir. The only loss window is guest userspace
  (node) buffering, closed by the graceful ACPI stop.

### 3. VM defaults and template

- `agent` uses the `agent` section of `/etc/qlvm/qlvm.yaml`
  (`agent.memory_mb`, `agent.vcpus`, `agent.user`) for VM defaults.
- A new `agent.template` key (string, required for `agent` to launch)
  names the qlvm template ref, e.g. `pi-in-xen-sha256:<digest>`. Keeping
  the template in config (not a flag) is what makes `agent [name]` the
  whole surface; template swaps are a config edit + `qlvm template
  create`.
- Core placement: whatever `qlvm vm create` does today for agent-type
  VMs (auto E-core placement), no new behavior.

### 4. Implementation shape

- New main package `cmd/agent` in the qlvm repo (alongside `cmd/qlvm`,
  `cmd/qlvm-agent`). Thin: it reuses the existing user-side client that
  talks to the root `qlvm-agent` service for create/start/stop — the same
  path `qlvm vm create` uses — so no new root RPCs are needed for
  lifecycle.
- Console attach: v1 execs the existing root console path
  (`sudo qlvm vm console <name>`) after start. Known wart: one sudo
  prompt mid-flow, and detach is Ctrl-] (guest-visible Ctrl-C). If the
  prompt becomes annoying, the follow-up is a console-stream RPC on
  `qlvm-agent` (root service opens the libxl console TTY and streams over
  the existing unix socket) — deliberately deferred.
- Config injection runs as the user (the 9p home dir is user-owned after
  provisioning) between create and start, so a failed injection
  surfaces before any VM is started.
- `qlvm install` (agent's install-system-files) drops the `agent` binary
  on PATH.

## Open questions

1. Auto-name collision policy: `agent-N` numbering restarts from 1 and
   skips names that already exist — fine, or do we want timestamped
   names? (Restart-from-1 assumed.)
2. Reuse-on-existing: launching `agent foo` when `foo` exists in a
   **running** state just attaches (no config re-inject, since the VM is
   already up). Acceptable? (Assumed yes.)
3. Should `agent` refuse to launch when `~/.qlvm/agent/` is missing, or
   warn-and-continue (VM boots to the `/login` warning screen)? (Assumed:
   warn-and-continue.)
