# qlvm UI — Visual Control Surface for VM and Template Management

## 1. Purpose

A native desktop application providing full visual parity with the `qlvm` CLI:
every VM and template operation available through a graphical interface, with
a layout inspired by Qube Manager (VM list + context-sensitive configuration
panel). The UI runs as root (matching Qube Manager's privilege model) and
directly calls qlvm's existing Go internal packages — no D-Bus, no subprocess
shelling, no external language runtime.

## 2. Goals

- **Full CLI parity:** every `qlvm vm` and `qlvm template` operation accessible
  from the UI, including `install`, `cores`, and `apps sync`.
- **Visual layout:** left pane VM/template list, right pane context-sensitive
  configuration, action buttons, optional log pane.
- **Native on Sway/Wayland:** well-behaved Wayland client, no GNOME dependencies.
- **Ostree-compatible:** zero new system dependencies; builds in the existing
  `make container-build` pipeline; installs as a single binary.
- **Single binary:** one Go executable, no sidecar processes, no Python.

## 3. Non-goals

- No web interface.
- No separate privileged daemon — the UI runs as root directly.
- No replacement for the CLI — the CLI remains the primary interface; the UI
  is an alternative.
- No in-guest desktop mirroring beyond what `vm run` (waypipe) already does.
- No config file editing UI for `qlvm.yaml` — the YAML file remains the source
  of truth, edited externally; the UI reads and displays it.

## 4. Anti-goals

- A UI that drifts from CLI behavior (different validation, missing operations,
  silent failures).
- Heavy system dependencies (GTK, Qt, Electron) that complicate the ostree build.
- A UI that requires modifying the ostree image recipe for its own dependencies.

## 5. Tech Stack

- **Language:** Go (same codebase, same modules)
- **GUI toolkit:** Fyne (pure Go, bundles its own rendering, Wayland support)
- **Build:** existing `make container-build` pipeline (cgo + containerized)
- **Installation:** single binary to `/usr/local/bin/qlvm-gui`
- **Privilege:** runs as root (launched via `sudo qlvm-gui` or from root context)

## 6. Architecture

### 6.1 Process Model

Single-process Fyne application. The UI imports qlvm's existing internal
packages directly:

- `internal/vm` — VM metadata, create
- `internal/template` — template listing, create, delete
- `internal/xenctl` — Xen domain operations (start, stop, kill, list)
- `internal/ovn` — OVN network operations
- `internal/ovs` — OVS operations
- `internal/config` — config loading and validation
- `internal/coremap` — CPU core topology
- `internal/setup` — dom0 install orchestration
- `internal/provisioner` — dotfiles provisioning
- `internal/projection` — waypipe app launching
- `internal/apps` — desktop cache

No new backend service, no D-Bus, no subprocess calls to `qlvm` CLI.

### 6.2 Privilege Model

The UI runs as root (EUID 0), matching Qube Manager's model:

- Xen, OVN, OVS, firewall operations require root — satisfied.
- `vm run` (waypipe) requires the user's Wayland session. The UI reads the
  invoking user's `WAYLAND_DISPLAY` and `XDG_RUNTIME_DIR` from the environment
  (passed through at launch) and spawns waypipe with those variables.
  Alternatively, the user launches `qlvm-gui` from their Sway session and the
  env vars are inherited (the app drops privileges only for the waypipe spawn).
- Config file reads (`/etc/qvm/qlvm.yaml`, `/var/lib/qvm/vms/*/meta.yaml`)
  work as root.

### 6.3 Layout

**Window:** single main window, resizable, minimum 900x600.

**Left Pane (navigation):**
- Tab or toggle: "VMs" / "Templates"
- **VMs tab:** list rows showing name, type (app/disposable/agent), state
  (running/stopped), memory, vCPUs. Running VMs visually distinct (e.g., green
  dot or different background).
- **Templates tab:** list rows showing template dir, image ref, digest, size,
  count of VMs referencing it.
- Filter/search box at top of list.

**Right Pane (context-sensitive):**
- **VM selected:** configuration panel showing:
  - Identity: name, type, domain, IP, MAC, UUID
  - Resources: memory, vCPUs, cores (pinned), workspace size
  - Storage: template ref, image, digest, mounts (list with add/remove)
  - Status: created time, state
  - Editable fields: memory, vCPUs (apply on change or via "Apply" button)
- **Template selected:** details panel showing:
  - Image ref, digest, kernel version, disk size
  - Referencing VMs (list)
  - Status: complete/incomplete
- **Nothing selected:** empty state with "Create VM" and "Create Template"
  buttons.

**Action Bar (bottom of left pane or top of right pane):**
- Context-sensitive buttons based on selection:
  - VM: Start, Stop, Kill, Restart, Delete, Run App, Provision, Sync Kernel
  - Template: Delete, Re-bake
  - Global: Create VM, Create Template, Install Dom0, Refresh

**Create VM Dialog:**
- Name (text input)
- Template (dropdown from available templates)
- Domain (dropdown from config domains)
- Type (dropdown: app/disposable/agent)
- Memory (number input, default from config)
- vCPUs (number input, default from config)
- Workspace size (number input, agent VMs only)
- Mounts (list with add/remove: host path, guest path)
- Cores (text input for pinning, optional)

**Create Template Dialog:**
- Image ref (text input, e.g., `quay.io/fedora/fedora:44`)
- Force re-bake (checkbox)
- Progress bar and log output during bake (~5 min)

**Log/Output Pane (optional, collapsible bottom):**
- Shows output from long-running operations (template bake, install, provision)
- Scrollable, monospace font
- Clear button

**Cores View (accessible from menu or button):**
- CPU topology: P cores, E cores
- dom0 assignment
- VM assignments
- Free cores

## 7. Operations Mapping

| CLI Command | UI Element |
|---|---|
| `vm list` | VM list pane (auto-refresh on state change, manual refresh button) |
| `vm create` | Create VM dialog |
| `vm start` | Start button (VM selected) |
| `vm stop` | Stop button (VM selected) |
| `vm kill` | Kill button (VM selected) |
| `vm restart` | Restart button (VM selected) |
| `vm delete` | Delete button (VM selected, confirmation dialog) |
| `vm run` | Run App button (VM selected, app picker dialog) |
| `vm provision` | Provision button (VM selected) |
| `vm sync-kernel` | Sync Kernel button (VM selected) |
| `template list` | Templates pane |
| `template create` | Create Template dialog |
| `template delete` | Delete button (template selected, confirmation) |
| `install` | Install Dom0 button (with progress) |
| `cores` | Cores view |
| `apps sync` | Sync Apps button (per VM or all running) |

## 8. Constraints

- **Ostree dom0:** no `dnf` installs; Fyne bundles its own rendering, no system
  GUI libraries needed.
- **Container build:** must compile in `Dockerfile.builder` alongside the cgo
  xenlight binding. Fyne is pure Go (no cgo beyond what qlvm already uses),
  so it should link cleanly.
- **Xen version skew:** same constraint as the CLI binary — builder headers
  must match dom0 runtime.
- **No shelling out:** UI calls internal Go packages directly; the only local
  process spawned is `waypipe` (same exception as the CLI).
- **Wayland:** must work as a Sway client; Fyne's Wayland backend must be
  functional (verify during development).

## 9. Open Questions

- **Wayland env for waypipe:** when the UI runs as root, how does it get the
  user's `WAYLAND_DISPLAY` and `XDG_RUNTIME_DIR`? Options: (a) pass at launch
  (`sudo WAYLAND_DISPLAY=$WAYLAND_DISPLAY XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR
  qlvm-gui`), (b) read from the user's session via loginctl, (c) require the
  user to launch from their session and use `pkexec` only for the privileged
  parts. Resolution needed during implementation.
- **Fyne Wayland maturity:** verify Fyne works correctly on Sway before
  committing to it. If Wayland support is broken, fallback is XWayland or
  a different toolkit.
- **Auto-refresh:** how often to poll VM state? 1-2 seconds is reasonable for
  running VMs, less frequent for stopped ones.
- **Editable VM config:** can memory/vCPUs be changed after creation? The CLI
  doesn't have a `vm update` command — changes require delete+recreate. The UI
  should reflect this limitation (show config but not allow in-place edits, or
  implement a `vm update` path if feasible).
