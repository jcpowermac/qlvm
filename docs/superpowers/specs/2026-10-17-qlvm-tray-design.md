# qlvm tray + app whitelist — design

Date: 2026-10-17
Status: approved in chat by the owner (2026-10-17); see "Builder calls" for
decisions made without the owner. Builder-check pass 2026-10-17
(questions Q1–Q4 answered in §Builder calls, flagged for owner review).

## Intent and why

Qubes has a tray "quick start" (pre-4.2 quick-start tray; 4.2's `qui`
Domains tray kept only running VMs + actions). The owner wants the
Qubes-style shape for qlvm: a tray icon listing every VM, each with an
**app submenu** and **actions**, where clicking an app starts the VM if
stopped and then launches the app (waypipe) — the same path the rofi
mode (`qlvm apps`) uses today.

Second goal: a better applications story. Qubes solves this with a
dom0-side cache of each VM's `.desktop` files **plus a per-VM
whitelist** (`/var/lib/qubes/appmenus/<vm>/whitelisted-appmenus.list`,
edited via the Qubes Manager app tab / new-AppVM wizard) that controls
which apps are exposed. qlvm has the cache (`qlvm apps sync` →
`/var/lib/qlvm/desktop-cache/<vm>/`) but no selection. This design adds
the whitelist, a CLI for it, an "Apps" tab in the existing Fyne VM
details dialog (sync + checkbox selection, Qubes-Manager-style), and the
tray.

## Goals

- `qlvm-tray`: a system-tray icon (wlroots/StatusNotifierItem, so it
  appears in the existing Sway + waybar tray next to nm-applet) that
  lists all VMs with run state, per-VM app submenu (whitelist-filtered),
  and per-VM actions. App click = start-if-stopped → launch via waypipe.
- Per-VM app whitelist with Qubes-compatible semantics: a
  `whitelisted-appmenus.list` file per VM in the desktop cache; **absent
  file = all apps visible**.
- CLI: `qlvm apps whitelist list|add|remove|clear <vm>`.
- GUI: new **Apps** tab in `ShowVMDetailDialog` (internal/gui/vmdetails.go)
  with a Sync button and a checkbox list of the VM's cached apps that
  writes the whitelist.
- No new dependencies beyond what is already in `go.mod`
  (`fyne.io/systray` is present as an indirect dep and is promoted to
  direct; it is the SNI+DBusMenu Go implementation with no cgo).

## Non-goals (v1)

- App **icons** in the tray menu or GUI list (`.desktop` Icon= names
  resolve inside the VM, not on dom0; caching icon files is a follow-up).
- Default-whitelist inheritance from templates (Qubes
  `--set-default-whitelist`).
- Editing the whitelist from the tray itself (tray reads it).
- Menu icons for VM rows beyond text state markers.
- GUI whitelist for templates; per-app launch from the GUI tab (the GUI
  already has RunApp for the selected VM).

## Constraints

- Hard repo constraints from `AGENTS.md` apply: `sudo make
  container-build` is the completion gate; live-verify D-Bus methods with
  `busctl call`; no shelling out to management CLIs (godbus/xenlight/
  libovsdb/sshx/waypipe are the native planes); no vendor names or real
  IPs in the repo; dom0 is ostree (no ad-hoc `dnf`).
- `qlvm vm run` is a **user-session command** (needs the desktop
  `WAYLAND_DISPLAY`); the tray therefore runs as the desktop user, not
  root.
- The daemon's D-Bus policy (`internal/agent/embedded/io.qlvm.agent.conf`)
  currently allows all local users to call all `io.qlvm.Agent` methods
  (polkit enforcement is a documented TODO); the tray and GUI rely on
  the same.
- The Fyne GUI is a pure D-Bus client (internal/gui/client.go interface
  over `agent.Client`); the new GUI tab must follow that — no direct
  file access from the GUI.

## Existing pieces this builds on

- **Desktop cache**: `/var/lib/qlvm/desktop-cache/<vm>/<name>.desktop`,
  written by `qlvm apps sync` (CLI-side: `fetchDesktops` in
  internal/cli/apps.go — LoadMeta → sshx.Connect → FetchFile of
  `/usr/share/applications/*.desktop` → `apps.SplitDesktops` →
  `apps.WriteCache`).
- **Launch path**: `apps.Launch` (internal/apps/apps.go) —
  `running? → start → waitSSH(WaitControl) → runApp(projection.Run with
  waypipe)`. `launchApp` in internal/cli/apps.go wires the callbacks.
- **D-Bus daemon**: `io.qlvm.Agent` / `/io/qlvm/Agent`,
  `ListVMs`, `StartVM`, `StopVM`, ... plus `agent.NewClient()` used by
  CLI and GUI. `agent.VMInfo.State` ∈ {running, stopped, ...}.
- **GUI**: `ShowVMDetailDialog` (internal/gui/vmdetails.go) is a tabbed
  Fyne dialog (Identity/Resources/Storage/Status), opened from the VM
  table row double-tap; `AppModel` polls on a ticker.
- **Tray library**: `fyne.io/systray` (getlantern/systray fork, GTK and
  legacy-tray support removed → Linux is StatusNotifierItem +
  `com.canonical.dbusmenu` over the session bus). Submenus
  (`AddSubMenuItem`), checkboxes, tooltips, raw-RGBA icon
  (`SetIcon([]byte)`) supported.

## Design

### 1. Whitelist core — `internal/apps`

New file(s) in the existing package:

- `WhitelistFileName = "whitelisted-appmenus.list"`; lives at
  `<cacheDir>/<vm>/whitelisted-appmenus.list`, one `.desktop` basename
  per line (Qubes-compatible format).
- `ReadWhitelist(cacheDir, vm string) ([]string, error)` — returns
  `nil` when the file is absent (meaning: all apps visible); sorted,
  deduplicated.
- `WriteWhitelist(cacheDir, vm string, names []string) error` — writes
  sorted, deduplicated; an empty list **deletes** the file (returns to
  "all apps"), Qubes-default style.
- `VisibleApps(cacheDir, vm string) ([]AppEntry, error)` —
  `AppEntry{Name, Icon, Exec string}`. Extract the per-VM `.desktop`
  scan from `EmitRofi` (Type=Application, NoDisplay≠true, non-empty
  Name+Exec, Exec field-codes stripped, Icon defaulted to
  `application-x-executable`) and filter through the whitelist when
  present. Sorted by Name. `EmitRofi` is refactored to render over
  `VisibleApps` so the rofi menu, tray, and GUI all agree.

Cache readability: the tray (desktop user) must read the cache.
`WriteCache` today creates dirs/files with umask defaults as root;
v1 sets cache dirs to 0755 and `.desktop`/whitelist files to 0644
(desktop entries are low-sensitivity: names + exec lines).

**Whitelist survival across syncs (builder check Q1):** the whitelist
must survive a cache refresh — it is user state, and "Sync" is the most
common maintenance action. Because `WriteCache` atomically replaces the
whole per-VM dir (RemoveAll + rename), every refresh path must
save/restore the whitelist file around the swap. Put that logic in one
shared helper in `internal/apps` (e.g. `SyncCache(cacheDir, vm,
entries) (count, error)` = read-whitelist-if-present → `WriteCache` →
rewrite whitelist) so the CLI `apps sync`, the daemon `SyncApps`, and
the tray sync all use it. Test: sync with an existing whitelist keeps
it; sync without one creates no file.

### 2. CLI — `qlvm apps whitelist`

New subcommand group under the existing `apps` command
(internal/cli/apps.go), CLI-side (direct file operations, same stance as
`apps sync` — no daemon round trip):

- `qlvm apps whitelist list <vm>` — one line per visible app:
  `<basename>.desktop  <Name>`; when a whitelist exists, a header note
  `# whitelist active (N/M apps)` is printed.
- `qlvm apps whitelist add <vm> <app...>` — `<app>` is a `.desktop`
  basename (unique prefix accepted, e.g. `firef` matches
  `firefox.desktop`) or the app Name; errors on no/ambiguous match.
  If no whitelist exists yet, add starts from the full app list and
  then adds the given apps (i.e. the first `add` materializes the
  whitelist as all+given).
- `qlvm apps whitelist remove <vm> <app...>` — same matching.
- `qlvm apps whitelist clear <vm>` — deletes the whitelist file
  (= all apps).

Matching is against the VM's cached apps (`VisibleApps` with whitelist
bypass for matching purposes, i.e. the full cached set); a clear error
names the cache dir and suggests `qlvm apps sync <vm>`.

### 3. Tray — `cmd/qlvm-tray`

New binary, runs as the desktop user.

**Stack**: `fyne.io/systray` (session-bus SNI + DBusMenu) +
`agent.NewClient()` (system-bus D-Bus) + the existing internal packages
(`apps`, `vm`, `sshx`, `projection`, `config`). No cgo → buildable in
the existing container build with the same tags as `qlvm-gui`.

**Structure** (one small package is fine; suggested layout):

- `internal/tray/` — all tray logic, testable without a display:
  - `Model`: the poll loop and menu-tree computation. Every 2 s call
    `ListVMs`; compute the desired menu (VM list, states, per-VM visible
    apps from the cache, actions for each state); apply to the
    `systray` menu only when the computed tree changed (compare a
    compact signature — VM names+states+app names — to avoid
    churning the DBusMenu while idle).
  - `Actions`: start/stop/sync/launch callbacks wrapping
    `agent.Client` and the `apps.Launch`-style wiring from
    `launchApp` (running check → `StartVM` → `projection.WaitControl` →
    `projection.Run` with waypipe). Launch runs in a goroutine; failures
    are reported via `systray.SetTooltip` (no window) — v1.
- `cmd/qlvm-tray/main.go` — flags (`-cache-dir` default
  `/var/lib/qlvm/desktop-cache`), icon, `systray.Run(onReady, onExit)`.
  **Connect mode (builder check Q2):** v1 launches and waits on the
  control channel in **ssh** mode only (`projection.ConnectSSH`, the
  CLI default); no `-config` flag — the config file is only needed for
  the CLI's `tcp` mode (dom0 router IP). Documented as a v1
  limitation; a `-connect` flag is a follow-up.
- `cmd/qlvm-tray/install-system-files.go` (or subcommand) — writes a
  systemd **user** unit to `~/.config/systemd/user/qlvm-tray.service`
  (`After=graphical-session.target`, `WantedBy=default.target`,
  `Restart=on-failure`) and enables it, mirroring the
  `qlvm-agent install-system-files` pattern.

**Menu shape** (Qubes 4.1/4.2 hybrid, owner-approved):

```
qlvm (disabled header)
├── <vm1> ● running
│   ├── <app A>            (whitelisted apps; tooltip = exec)
│   ├── <app B>
│   ├── ─────
│   ├── Sync app list
│   └── Stop
├── <vm2> ○ stopped
│   ├── <app A>
│   ├── ─────
│   ├── Sync app list
│   └── Start
├── ─────
├── Refresh all apps       (sync every running VM)
└── Quit
```

- State marker: text prefix/suffix (●/○) — SNI menu items carry no
  colors in v1.
- A VM with no cached apps shows a disabled `No apps cached — use
  "Sync app list"` item.
- App click = `apps.Launch` semantics: if stopped → `StartVM`, wait
  control channel, then waypipe the app; if running → straight to
  waypipe.
- `Sync app list` / `Refresh all apps`: CLI-side sync per VM
  (`fetchDesktops`-equivalent against the desktop user's SSH identity),
  VM must be running (clear tooltip on "not running").
- Icon: a small embedded monochrome PNG generated at build time from a
  simple bitmap (a rounded square with "QL" is overkill; a plain
  filled square/dot suffices — builder's call), set via
  `systray.SetIcon`.

**Lifecycle**: `Quit` item unregisters cleanly (SNI unregistered from
the session bus); SIGTERM from systemd handled the same way. Crash
recovery via the user unit's `Restart=on-failure`.

### 4. Daemon — new app methods (for the GUI)

The GUI is a pure D-Bus client, so the daemon gains three methods on
`io.qlvm.Agent` (agent.go exports them; agent.Client wraps them;
bus-policy file gains the members):

- `ListApps(vm) -> [(name, icon, whitelisted)]` — the VM's full cached
  app set (not filtered) plus the whitelist state, so the GUI can render
  a checkbox list where unchecked-but-cached apps are still selectable.
  (Tuple type per the existing godbus conventions in this codebase.)
- `SyncApps(vm) -> (count, error)` — daemon-side fetch, writes the
  cache as root via the shared whitelist-preserving helper (Q1). SSH
  identity (builder check Q3): the daemon runs as root under systemd,
  where `os.UserHomeDir()` has no keys; rule = try root's home first,
  then each wheel-group member's home in sorted order, using the first
  identity that connects (reuse the existing wheel-member discovery in
  agent.go). A clear error names the homes tried when none works.
- `SetAppWhitelist(vm, []string) -> error` — writes/deletes the
  whitelist file (empty = delete = all apps).

Polkit is not yet enforced in the daemon (documented TODO), so no new
polkit actions are required in v1; the bus config simply allows the new
members for local users like the existing ones.

`SyncApps` is daemon-side (root cache write, root-owned SSH identity
resolution), while `qlvm apps sync` stays CLI-side — the asymmetry is
intentional: the GUI cannot do root file writes, the CLI can.

### 5. GUI — Apps tab

`ShowVMDetailDialog` (internal/gui/vmdetails.go) gains a fifth tab,
**Apps**, between Storage and Status (Fyne tab order):

- Top row: **Sync** button. Clicking it calls `SyncApps`, disables
  itself while in flight, then refreshes the list. Result feedback
  (builder check Q4): an **inline status label** in the tab is primary
  (`synced N apps from <vm>` or the error), because the bottom log pane
  starts hidden; the same line is also appended to the log pane.
- Body: a checkbox list of **all cached apps** (`ListApps`), name as
  row label, checked state = in the whitelist (a VM with no whitelist
  file shows all checked).
- Bottom: **Save** button — collects the checked set and calls
  `SetAppWhitelist`. Apply-on-Save, not live toggling (builder call;
  Qubes Manager applies on window close — Save is the v1 analogue).
- If the VM has no cache yet: an empty-state label suggesting Sync.

The GUI `Client` interface (internal/gui/client.go) gains
`ListApps`, `SyncApps`, `SetAppWhitelist`; the fake client in
internal/gui/fakeclient.go implements them for tests.

## Testing

- Unit: whitelist read/write (absent → all, empty → delete, dedup,
  sort), `VisibleApps` filtering + scan extraction, prefix/ambiguous
  app matching (table tests, no live system).
- Tray: model-level tests — menu tree computation from fake
  `ListVMs` + temp cache dir (signature-based rebuild asserted), launch
  callback wiring with fakes (start-then-run order, running short-circuit).
- Agent: `ListApps`/`SyncApps`/`SetAppWhitelist` with a fake/fetched
  cache dir; follow existing agent_test patterns.
- GUI: fakeclient-driven tests for the tab (sync refreshes, save sends
  the checked set), following existing gui test patterns.
- CLI: `whitelist` subcommand tests via the existing fake-client test
  harness (these are file-side, so direct file assertions in a temp dir).
- **Live verification** (required by AGENTS.md before "done"):
  `busctl call` each new daemon method; `qlvm apps whitelist ...`
  round trip; tray icon visible in the waybar tray with correct menu
  (start a stopped VM via the menu, launch an app, confirm waypipe
  window); GUI Apps tab sync + whitelist round trip; `sudo make
  container-build` green.

## Builder calls (decided without the owner; flagged for review)

1. Apply-on-**Save** for the GUI checkbox list (not live, not
   on-close).
2. First `whitelist add` materializes the whitelist as the **full** app
   set (+ given apps); `remove` similarly starts from the full set.
   Rationale: absent = all, so an edit must first pin the full set to
   have anything to remove from.
3. Tray polls at **2 s** and rebuilds the menu only when the computed
   tree changes.
4. Tray error reporting via tooltip text (no error dialog in v1).
5. Cache perms 0755/0644 for user readability.
6. `SetAppWhitelist` with an empty list deletes the file (= all apps).
7. Tray binary + user unit installed from the repo (embedded unit,
   `install-system-files`), not from the ostree recipe, in v1.
8. Plain embedded PNG tray icon (builder picks the exact bitmap).
9. `ListApps` returns the **full** cached set with per-app whitelist
   state rather than a filtered list, so the GUI can show everything.

Builder-check answers (2026-10-17, my calls — owner to confirm):

10. **Q1:** whitelist survives syncs; one shared save/restore helper
    around the `WriteCache` swap used by all three refresh paths.
11. **Q2:** tray is ssh-connect-only in v1; no `-config` flag.
12. **Q3:** daemon `SyncApps` SSH identity = root home, then wheel
    members sorted, first that connects.
13. **Q4:** GUI Sync feedback = inline tab status label (primary) plus
    log-pane append.
