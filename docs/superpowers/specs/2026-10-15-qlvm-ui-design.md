# qlvm UI — Visual Control Surface for VM and Template Management

## 1. Purpose

A native desktop application providing full visual parity with the `qlvm` CLI:
every VM and template operation available through a graphical interface, with
a layout inspired by Qube Manager (VM list + context-sensitive configuration
panel). The UI runs as the user and communicates with a root daemon (`qlvm-agent`)
over D-Bus for privileged operations — no direct internal package imports for
privileged work, no subprocess shelling, no external language runtime.

## 2. Goals

- **Full CLI parity:** every `qlvm vm` and `qlvm template` operation accessible
  from the UI, including `cores` and `apps sync`. Dom0 provisioning (`install`)
  is external and not part of the UI.
- **Visual layout:** left pane VM/template list, right pane context-sensitive
  configuration, action buttons, optional log pane.
- **Native on Sway/Wayland:** well-behaved Wayland client, no GNOME dependencies.
- **Ostree-compatible:** zero new system dependencies; builds in the existing
  `make container-build` pipeline; installs as two Go binaries (GUI and daemon).
- **Two binaries:** `qlvm-gui` (user, D-Bus client) and `qlvm-agent` (root,
  D-Bus server); no sidecar processes, no Python.

## 3. Non-goals

- No web interface.
- No dom0 provisioning — `qlvm install` is removed; provisioning is external
  (ostree recipe or dedicated script).
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
- **Installation:** GUI binary to `/usr/local/bin/qlvm-gui`, daemon binary to
  `/usr/local/bin/qlvm-agent`
- **Privilege:** GUI and CLI run as the user; privileged operations go through
  the root daemon via D-Bus
- **D-Bus daemon:** `qlvm-agent`, runs as root under systemd. Both CLI and GUI
  are D-Bus clients.

## 6. Architecture

### 6.1 Process Model

Two processes:

- **`qlvm-agent`** — root daemon, D-Bus server. Exposes `io.qlvm.Agent` on the
  system bus. Imports qlvm's internal packages for privileged operations:
  - `internal/vm` — VM metadata, create
  - `internal/template` — template listing, create, delete
  - `internal/xenctl` — Xen domain operations (start, stop, kill, list)
  - `internal/ovn` — OVN network operations
  - `internal/ovs` — OVS operations
  - `internal/config` — config loading and validation
  - `internal/coremap` — CPU core topology
  - `internal/provisioner` — dotfiles provisioning
  - `internal/projection` — waypipe server-side support
  - `internal/apps` — desktop cache
- **`qlvm-gui` / `qlvm` CLI** — user processes, D-Bus clients. Neither imports
  internal packages directly for privileged operations; both use
  `internal/agent.Client` to call the daemon.

The daemon is started by external provisioning (see §6.7). No subprocess
calls to the `qlvm` CLI.

### 6.2 Privilege Model

CLI and GUI run as the user. The daemon runs as root via systemd (started by
external provisioning).

- Read operations (list VMs, templates, domains; get config, coremap) open on
  the system bus and require no authentication.
- Mutation operations (create, delete, start, stop, kill, restart VM/template)
  require polkit authentication (`auth_admin`). The daemon checks polkit for
  each mutation and returns an error if denied; the polkit agent shows the
  auth prompt.
- `vm run` (waypipe) is spawned by the GUI as the user with the user's
  Wayland environment (`WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR` inherited from the
  session). The daemon is not involved in waypipe.

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
  - Global: Create VM, Create Template, Refresh

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
- Shows output from long-running operations (template bake, provision)
- Scrollable, monospace font
- Clear button

**Cores View (accessible from menu or button):**
- CPU topology: P cores, E cores
- dom0 assignment
- VM assignments
- Free cores

### 6.7 Provisioning

dom0 setup (OVS, OVN, firewalld, systemd services, storage tree, vif script,
daemon service) is external to qlvm. The ostree recipe or a provisioning
script handles it. `qlvm` assumes the dom0 is provisioned and the daemon is
running. The `qlvm-agent` binary embeds its systemd unit, D-Bus policy,
and polkit action files so the provisioning step can extract them (e.g.,
`qlvm-agent install-system` or similar).

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
- **No shelling out:** UI and CLI call the daemon via D-Bus; the daemon calls
  internal Go packages. The only local process spawned outside the daemon is
  `waypipe` (same exception as the CLI).
- **Wayland:** must work as a Sway client; Fyne's Wayland backend must be
  functional (verify during development).
- **Daemon service file must be installable by external provisioning:** the
  systemd unit, D-Bus policy, and polkit action are embedded in the
  `qlvm-agent` binary for the provisioning step to extract.

## 9. Open Questions

- **Fyne Wayland maturity:** verify Fyne works correctly on Sway before
  committing to it. If Wayland support is broken, fallback is XWayland or
  a different toolkit.
- **Auto-refresh:** how often to poll VM state? 1-2 seconds is reasonable for
  running VMs, less frequent for stopped ones.
- **Editable VM config:** can memory/vCPUs be changed after creation? The CLI
  doesn't have a `vm update` command — changes require delete+recreate. The UI
  should reflect this limitation (show config but not allow in-place edits, or
  implement a `vm update` path if feasible).
