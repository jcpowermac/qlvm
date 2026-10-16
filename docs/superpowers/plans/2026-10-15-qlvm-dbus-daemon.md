# Plan: qlvm D-Bus Daemon — Privilege Boundary for qlvm-gui

**Spec:** `docs/superpowers/specs/2026-10-15-qlvm-ui-design.md` (this plan overrides
its §3 non-goal "No separate privileged daemon" and §6.2 privilege model; the
updated spec will be produced as Task 1).

**Why:** Both the CLI and GUI currently run as root and call qlvm's internal
packages directly. This exposes both interfaces to full dom0 privilege: a bug
in either is a dom0 compromise. Moving all privileged operations to a small
systemd daemon with a D-Bus interface gives:

- Both CLI and GUI run as the user (no sudo needed for read ops; mutations
  prompt via polkit).
- A single, auditable privilege boundary: the daemon.
- One code path for all operations — no drift between CLI and GUI behavior.
- waypipe runs as the user from the GUI, not dropped-privileges from root.

Additionally, `qlvm install` (dom0 provisioning) is removed from the tool
entirely. Provisioning is dom0 setup (OVS, OVN, firewalld, systemd services,
storage tree), not VM management — it belongs in the ostree recipe or an
external provisioning step, not in the runtime tool. Removing it:

- Eliminates the bootstrap chicken-and-egg (install can't use the daemon it
  starts).
- Narrows the tool's responsibility to VM lifecycle only.
- Lets the daemon be started by external provisioning, not by `qlvm`.

This follows the project's existing D-Bus pattern (see `internal/fw`,
`internal/systemd`, `internal/netd`) and its rule of "talk to control planes
through libraries, never shell out."

## Execution Strategy

Tasks are ordered to allow parallel execution where dependencies allow.
Independent tasks are marked with `∥` (can run in parallel with others in the
same group).

**Group A (foundation — must be sequential):**
1. Updated spec
2. D-Bus interface definition + daemon skeleton
3. D-Bus client library

**Group B (daemon implementation — parallel within group):**
4. ∥ Read methods (ListVMs, ListTemplates, ListDomains, GetConfig, GetCoremap)
5. ∥ VM lifecycle methods (Create, Delete, Start, Stop, Kill, Restart)
6. ∥ Template methods (Create, Delete)
7. ∥ Auxiliary methods (Provision, SyncKernel)
8. ∥ Progress signals for long-running ops

**Group C (integration — sequential):**
9. Systemd service + D-Bus policy files
10. Makefile build target

**Group D (GUI migration — sequential):**
11. GUI switches to D-Bus client
12. GUI drops root requirement
13. waypipe runs as user

**Group E (CLI migration — sequential):**
14. CLI switches to D-Bus client
15. Remove `qlvm install` from CLI
16. CLI drops root requirement

**Group F (verification):**
17. Live smoke test

## Task 1: Updated spec

**File:** `docs/superpowers/specs/2026-10-15-qlvm-ui-design.md` (edit in place)

Changes:
- §3 non-goals: remove "No separate privileged daemon." Add "No dom0
  provisioning — `qlvm install` is removed; provisioning is external (ostree
  recipe or dedicated script)."
- §5 tech stack: add "D-Bus daemon: `qlvm-agent`, runs as root under systemd.
  Both CLI and GUI are D-Bus clients."
- §6.1 process model: two processes — `qlvm-agent` (root, D-Bus server) and
  `qlvm-gui`/`qlvm` CLI (user, D-Bus clients). Neither imports internal
  packages directly for privileged operations; both use `internal/agent.Client`.
- §6.2 privilege model: CLI and GUI run as the user. Daemon runs as root via
  systemd (started by external provisioning). Read operations open on the
  system bus. Mutations require polkit (`auth_admin`). waypipe is spawned by
  the GUI as the user with the user's Wayland environment.
- §6.7 (new) provisioning: dom0 setup (OVS, OVN, firewalld, systemd services,
  storage tree, vif script, daemon service) is external to qlvm. The ostree
  recipe or a provisioning script handles it. `qlvm` assumes the dom0 is
  provisioned and the daemon is running.
- §8 constraints: remove "Daemon must be installable via `qlvm install`."
  Add "Daemon service file must be installable by external provisioning"
  (embedded in the binary for the provisioning step to extract).
- §9 open questions: resolve "Wayland env for waypipe" — the GUI is the user,
  so it inherits the session environment naturally. Remove the question.

**Tests:** none (documentation).
**Verify:** spec reads coherently; no contradictions with existing spec sections.

## Task 2: D-Bus interface definition + daemon skeleton

**Files:**
- `internal/agent/agent.go` — interface, types, daemon implementation
- `internal/agent/agent_test.go`

**Design:**

```go
// Package agent implements the privileged qlvm D-Bus daemon (qlvm-agent)
// and its client library. The daemon runs as root under systemd, exposes
// io.qlvm.Agent on the system bus, and delegates to qlvm's internal packages.
// The client library is used by qlvm-gui (running as the user) to call the
// daemon.

package agent

// D-Bus constants.
const (
	ServiceName = "io.qlvm.Agent"
	ObjectPath  = "/io/qlvm/Agent"
	Iface       = "io.qlvm.Agent"
)

// VMInfo is a single row of the VM list, as returned by D-Bus.
type VMInfo struct {
	Name      string
	Type      string // app|disposable|agent
	State     string // running|stopped|dying|paused|blocked
	MemoryMB  uint64
	VCPUs     uint8
	Domain    string
	Image     string
	Digest    string
	Mounts    []string // "host:guest" pairs
	CreatedAt int64    // unix seconds
}

// TemplateInfo is a single row of the template list.
type TemplateInfo struct {
	Dir          string
	Image        string
	Digest       string
	Kernel       string
	SizeBytes    uint64
	VMCount      int
	Complete     bool
}

// DomainInfo is one row of the Xen domain list.
type DomainInfo struct {
	Name  string
	ID    uint32
	MemMB uint64
	VCPUs uint8
	State string
}

// CreateVMOptions are the parameters for CreateVM.
type CreateVMOptions struct {
	Name     string
	Domain   string
	Template string
	Type     string
	MemoryMB uint64
	VCPUs    uint8
	Mounts   []string
	Cores    string
}

// Agent is the D-Bus object exported by the daemon. Methods are mapped to
// D-Bus by the register functions below. Long-running methods emit
// Progress signals and return on completion (or error).
type Agent struct {
	// Control planes (wired by the daemon's main).
	xen    xenctl.Xen
	ovn    vm.OVNPorter
	ovs    ovs.VifPorter
	cfg    *config.Config
	coremapPath string

	// Progress signal state (mutex-protected).
	mu       sync.Mutex
	progress map[string]*ProgressTracker
}

// ProgressTracker tracks a long-running operation for signal emission.
type ProgressTracker struct {
	ID      string
	Message string
	Done    bool
}

// New creates an Agent with the given control planes.
func New(x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath string) *Agent

// Run registers the Agent on the system bus and blocks until the bus
// connection closes or ctx is cancelled. Returns the acquired bus name.
func (a *Agent) Run(ctx context.Context) (*dbus.Name, error)

// --- Read methods ---
func (a *Agent) ListVMs() ([]VMInfo, error)
func (a *Agent) ListTemplates() ([]TemplateInfo, error)
func (a *Agent) ListDomains() ([]DomainInfo, error)
func (a *Agent) GetVM(name string) (*VMInfo, error)
func (a *Agent) GetTemplate(dir string) (*TemplateInfo, error)
func (a *Agent) GetConfig() (*config.Config, error)
func (a *Agent) GetCoremap() (*coremap.Coremap, error)

// --- VM lifecycle methods ---
func (a *Agent) CreateVM(opts *CreateVMOptions) (string, error) // returns VM name
func (a *Agent) DeleteVM(name string) error
func (a *Agent) StartVM(name string) error
func (a *Agent) StopVM(name string) error
func (a *Agent) KillVM(name string) error
func (a *Agent) RestartVM(name string) error

// --- Template methods ---
func (a *Agent) CreateTemplate(image string, force bool) (string, error) // returns dir
func (a *Agent) DeleteTemplate(dir string) error

// --- Auxiliary methods ---
func (a *Agent) ProvisionVM(name string) error
func (a *Agent) SyncKernel(name string) error

// Note: Install is NOT a daemon method. Dom0 provisioning is external to
// qlvm (see spec §6.7). The daemon is started by external provisioning.

// Note: RunApp is NOT a daemon method. waypipe needs the user's Wayland
// session, which the root daemon doesn't have. The GUI handles waypipe
// locally (see Task 14).

// --- Progress ---
func (a *Agent) EmitProgress(id, message string)
func (a *Agent) EmitProgressDone(id string)
```

**D-Bus registration:** Use `dbus.Export` for the object, `bus.ObjectServer`.
Each method maps to a D-Bus method call. Structs marshal to D-Bus variants
(`a{sv}`) or typed arrays. The existing internal packages return Go structs;
the Agent converts them to the D-Bus-friendly types above.

**Daemon main (`cmd/qlvm-agent/main.go`):**

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		cancel()
	}()

	cfg, err := config.Load("/etc/qlvm/qlvm.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	x, err := xenctl.New()
	if err != nil {
		log.Fatalf("xen: %v", err)
	}
	defer x.Close()

	// OVN/OVS reconcilers (reuse existing construction from CLI).
	ovnr := ovn.NewReconciler(...)
	ovsr := ovs.NewReconciler(...)

	a := agent.New(x, ovnr, ovsr, cfg, "")
	name, err := a.Run(ctx)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	log.Printf("qlvm-agent running on system bus as %s", name)
}
```

**Tests (`agent_test.go`):**
- `TestListVMs`: fake Xen + filesystem fixture → correct VMInfo slice.
- `TestCreateVM`: mock the control planes → verify they receive the right
  arguments.
- `TestRunRegistersBus`: verify `Run` acquires the service name and exports
  the object (use an in-memory bus or a fake conn).

**Verify:** `go build ./cmd/qlvm-agent` compiles; `go test ./internal/agent`
passes.

## Task 3: D-Bus client library

**Files:**
- `internal/agent/client.go`
- `internal/agent/client_test.go`

**Design:**

```go
// Client is the D-Bus client used by qlvm-gui. It mirrors Agent's methods
// but calls them over the system bus. The GUI uses Client instead of
// importing internal packages directly.

type Client struct {
	bus *dbus.Conn
	obj dbus.Object
}

// New connects to the system bus and returns a Client bound to the Agent.
// Returns an error if the daemon is not running.
func New() (*Client, error)

// Read methods (mirror Agent).
func (c *Client) ListVMs() ([]VMInfo, error)
func (c *Client) ListTemplates() ([]TemplateInfo, error)
func (c *Client) ListDomains() ([]DomainInfo, error)
func (c *Client) GetVM(name string) (*VMInfo, error)
func (c *Client) GetTemplate(dir string) (*TemplateInfo, error)
func (c *Client) GetConfig() (*config.Config, error)
func (c *Client) GetCoremap() (*coremap.Coremap, error)

// Write methods (mirror Agent; require polkit auth on the daemon side).
func (c *Client) CreateVM(opts *CreateVMOptions) (string, error)
func (c *Client) DeleteVM(name string) error
func (c *Client) StartVM(name string) error
func (c *Client) StopVM(name string) error
func (c *Client) KillVM(name string) error
func (c *Client) RestartVM(name string) error
func (c *Client) CreateTemplate(image string, force bool) (string, error)
func (c *Client) DeleteTemplate(dir string) error
func (c *Client) ProvisionVM(name string) error
func (c *Client) SyncKernel(name string) error

// Note: Install is not a client method — dom0 provisioning is external.

// Note: RunApp is not a client method — the GUI spawns waypipe directly.

// SubscribeProgress registers a callback for progress signals. Returns a
// cancel function. The GUI uses this to update progress bars during
// long-running operations.
func (c *Client) SubscribeProgress(callback func(id, message string)) func()

// Close releases the bus connection.
func (c *Client) Close() error
```

**Progress signals:** The daemon emits `io.qlvm.Agent.Progress(id, message)`
and `io.qlvm.Agent.ProgressDone(id)` signals. The client subscribes via
`bus.AddMatchSignal` and dispatches to the callback.

**Tests (`client_test.go`):**
- `TestClientConnects`: fake daemon on a test bus → client connects and
  calls methods.
- `TestProgressSignals`: daemon emits progress → client callback receives it.

**Verify:** `go test ./internal/agent` passes.

## Task 4: Read methods implementation

**File:** `internal/agent/agent.go` (implement the read methods)

Implementation details:
- `ListVMs`: read `/var/lib/qlvm/vms/*/meta.yaml`, merge with Xen domain
  states from `ListDomains`.
- `ListTemplates`: read `/var/lib/qlvm/templates/*/META`, count referencing
  VMs.
- `ListDomains`: call `xen.List()`, map to DomainInfo.
- `GetVM`: load single VM meta + domain state.
- `GetTemplate`: load single template meta.
- `GetConfig`: return the loaded config.
- `GetCoremap`: call `coremap.Load(a.coremapPath)`.

**Tests:** Extend `agent_test.go` with fixtures for each read method.
**Verify:** `go test ./internal/agent` passes.

## Task 5: VM lifecycle methods

**File:** `internal/agent/agent.go` (implement CreateVM, DeleteVM, StartVM,
StopVM, KillVM, RestartVM)

Implementation details:
- Delegate to `xenctl.Start`, `xenctl.Delete`, etc., using the same
  construction as the CLI (`internal/cli/lifecycle.go`,
  `internal/cli/create.go`).
- `CreateVM`: validate options, construct `vm.Meta`, call the create
  orchestration. Emit progress signals.
- `DeleteVM`: call `xenctl.Delete`.
- `StartVM`: call `xenctl.Start`.
- `StopVM`: call `xen.Shutdown`.
- `KillVM`: call `xen.Destroy`.
- `RestartVM`: Kill + Start.

**Tests:** Mock control planes, verify correct delegation.
**Verify:** `go test ./internal/agent` passes.

## Task 6: Template methods

**File:** `internal/agent/agent.go` (implement CreateTemplate, DeleteTemplate)

Implementation details:
- `CreateTemplate`: call `template.Create` (or equivalent). This is
  long-running (~5 min); emit progress signals for each phase (pull,
  ostree bake, finalize). Return the template dir.
- `DeleteTemplate`: call `template.Delete`.

**Tests:** Mock the template operations.
**Verify:** `go test ./internal/agent` passes.

## Task 7: Auxiliary methods

**File:** `internal/agent/agent.go` (implement ProvisionVM, SyncKernel)

Implementation details:
- `ProvisionVM`: call `provisioner.Provision`.
- `SyncKernel`: call the kernel sync logic from `internal/cli`.

**Note:** `Install` is not a daemon method (dom0 provisioning is external).
`RunApp` is not a daemon method (waypipe needs the user's Wayland session;
the GUI handles it locally, see Task 13).

**Correction:** Remove `RunApp` from Task 2's Agent interface. The GUI
handles waypipe locally.

**Tests:** Mock the underlying packages.
**Verify:** `go test ./internal/agent` passes.

## Task 8: Progress signals

**File:** `internal/agent/agent.go` (wire progress emission into long-running
methods)

Implementation details:
- Each long-running method (CreateVM, CreateTemplate, Install) gets a unique
  operation ID (UUID or counter).
- Before starting, emit `Progress(id, "starting")`.
- During the operation, emit `Progress(id, phase)` at meaningful points.
- On completion or error, emit `ProgressDone(id)` (include error in the
  method return, not the signal).
- The client's `SubscribeProgress` dispatches these to the GUI callback.

**Tests:** Verify signals are emitted in order for a mocked long operation.
**Verify:** `go test ./internal/agent` passes.

## Task 9: Systemd service + D-Bus policy

**Files:**
- `internal/agent/service.go` — embeds the systemd unit file and D-Bus policy
- `internal/agent/service_test.go`

**Systemd unit (`qlvm-agent.service`):**

```ini
[Unit]
Description=qlvm VM management daemon
Requires=dbus.socket
After=dbus.socket network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/qlvm-agent
Restart=on-failure
RestartSec=5
# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/qlvm /var/run/qlvm /run/xen /run/openvswitch /run/ovn /run/podman
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictNamespaces=true
LockPersonality=true
RestrictRealtime=true
RestrictSUIDSGID=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
```

**D-Bus policy (`/etc/dbus-1/system.d/io.qlvm.agent.conf`):**

```xml
<!DOCTYPE busconfig PUBLIC
 "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <policy user="root">
    <allow own="io.qlvm.Agent"/>
  </policy>
  <policy context="default">
    <!-- Read methods: any local user -->
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="ListVMs"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="ListTemplates"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="ListDomains"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="GetVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="GetTemplate"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="GetConfig"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="GetCoremap"/>
    <!-- Mutation methods: require active session auth (polkit) -->
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="CreateVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="DeleteVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="StartVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="StopVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="KillVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="RestartVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="CreateTemplate"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="DeleteTemplate"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="ProvisionVM"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="SyncKernel"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="Install"/>
    <!-- Signals: any local user can subscribe -->
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="Progress"/>
    <allow send_destination="io.qlvm.Agent"
           send_interface="io.qlvm.Agent"
           send_member="ProgressDone"/>
  </policy>
</busconfig>
```

**Note:** The D-Bus policy above allows all local users to call mutation
methods. Polkit enforcement happens **inside the daemon**: each mutation
method calls a polkit check before executing. If the check fails, the method
returns an error and the polkit agent shows the auth prompt. This is the
standard pattern (see `org.freedesktop.systemd1` which does the same).

**Polkit integration in the daemon:**

```go
// polkitCheck verifies that the caller is authorized for the given action.
// It calls org.freedesktop.PolicyKit1.Authorize on the system bus.
func (a *Agent) polkitCheck(method string, sender dbus.Sender) error {
	// Map method to polkit action ID (io.qlvm.<method>).
	action := "io.qlvm." + method
	// Call polkit with the sender's UID as the subject.
	// If authorization is denied, return an error.
	// If authorization requires interaction, polkit shows the prompt.
}
```

**Polkit action file (`/usr/share/polkit-1/actions/io.qlvm.agent.policy`):**

```xml
<policyconfig>
  <vendor>qlvm</vendor>
  <vendor_url>https://github.com/jcpowermac/qlvm</vendor_url>
  <action id="io.qlvm.CreateVM">
    <description>Create a VM</description>
    <message>Authentication is required to create a VM</message>
    <defaults>
      <allow_any>no</allow_any>
      <allow_inactive>no</allow_inactive>
      <allow_active>auth_admin</allow_active>
    </defaults>
  </action>
  <!-- Repeat for DeleteVM, StartVM, StopVM, KillVM, RestartVM,
       CreateTemplate, DeleteTemplate, ProvisionVM, SyncKernel, Install -->
</policyconfig>
```

**Files to embed:** Use Go's `embed` package to embed the service file, D-Bus
policy, and polkit action. `install` writes them to the system paths.

**Tests:** Verify the embedded files are valid (parse the XML, check the
systemd unit syntax).
**Verify:** Files are syntactically correct.

## Task 10: External provisioning support

**File:** `internal/agent/service.go` (embed service files for extraction)

Since `qlvm install` is removed, the daemon's systemd service, D-Bus policy,
and polkit action must be installable by external provisioning. The binary
embeds these files (via Go's `embed` package) and provides a `qlvm-agent
install-system-files` subcommand that a provisioning script can call to
extract them to the system paths.

```go
// cmd/qlvm-agent/main.go (new subcommand)
case "install-system-files":
    // Write the embedded service file to /etc/systemd/system/
    // Write the embedded D-Bus policy to /etc/dbus-1/system.d/
    // Write the embedded polkit action to /usr/share/polkit-1/actions/
    // systemctl daemon-reload && systemctl enable qlvm-agent
```

The provisioning script (external to this repo) would:
1. Install the `qlvm-agent` binary to `/usr/local/bin/`.
2. Run `sudo qlvm-agent install-system-files`.
3. Run `sudo systemctl start qlvm-agent`.
4. (Separately) provision OVS, OVN, firewalld, networkd, storage tree,
   vif script — this is outside qlvm's scope.

**Tests:** Verify the embedded files are valid and the subcommand writes them
 correctly.
**Verify:** `qlvm-agent install-system-files` produces correct files.

## Task 11: Makefile build target

**File:** `Makefile`

Changes:
- Add `qlvm-agent` to the container build targets alongside `qlvm` and
  `qlvm-gui`.
- Install `qlvm-agent` to `/usr/local/bin/`.

**Verify:** `make container-build` produces all three binaries.

## Task 11: GUI switches to D-Bus client

**Files:**
- `internal/gui/app.go` — replace direct package imports with `agent.Client`
- `internal/gui/actions.go` — replace direct calls with client methods
- `internal/gui/model.go` — update to use client for data loading

Changes:
- `New()` and `NewWithDependencies()` accept an `agent.Client` instead of
  `xenctl.Xen`, `vm.OVNPorter`, `ovs.VifPorter`.
- `loadData()` calls `client.ListVMs()`, `client.ListTemplates()`,
  `client.ListDomains()`.
- `Actions` holds a `*agent.Client` instead of the control planes.
- Progress callbacks are wired via `client.SubscribeProgress`.

**Tests:** Update `app_test.go` and `model_test.go` to use a fake client.
**Verify:** `go test ./internal/gui` passes.

## Task 12: GUI drops root requirement

**File:** `cmd/qlvm-gui/main.go`

Changes:
- Remove any root checks or privilege assumptions.
- The GUI connects to the D-Bus daemon as the user.
- If the daemon is not running, show a clear error ("qlvm-agent is not
  running; run `sudo systemctl start qlvm-agent`").

**Tests:** Verify the GUI starts without root privileges (unit test the
connection logic).
**Verify:** `qlvm-gui` (unprivileged) connects to the daemon and lists VMs.

## Task 13: waypipe runs as user

**Files:**
- `internal/gui/projection.go` (new) — waypipe spawning from the GUI
- `internal/projection` — verify it works as the user

**Note:** This removes the `RunApp` method from the Agent interface defined in
Task 2. The GUI's `Actions` struct will have a `RunApp` method that calls
`projection` directly, not via the client.

Changes:
- The GUI (running as the user) spawns waypipe directly. It has the user's
  `WAYLAND_DISPLAY` and `XDG_RUNTIME_DIR` naturally.
- Remove any privilege-dropping logic that was needed when the GUI ran as
  root.
- The `RunApp` path: GUI calls the VM's waypipe server (via SSH/TCP/vsock
  as the user), connects waypipe to the user's Wayland session.

**Tests:** Verify waypipe spawns with the correct environment (integration
test or manual verification).
**Verify:** `qlvm vm run` from the GUI works as the user.

## Task 14: CLI switches to D-Bus client

**Files:**
- `internal/cli/*.go` — replace direct package calls with `agent.Client`
- `cmd/qlvm/main.go` — connect to the daemon at startup

Changes:
- The CLI connects to the D-Bus daemon at startup (same `agent.Client` the
  GUI uses).
- All commands (vm list, vm start, template create, etc.) call the client
  methods.
- Read commands work without auth; mutation commands trigger polkit prompts.
- If the daemon is not running, the CLI exits with a clear error.
- `vm run` stays CLI-side (needs the user's SSH key and Wayland env for
  waypipe) — it doesn't go through the daemon.
- Remove root checks from the CLI; it runs as the user.

**Tests:** Update CLI tests to use a fake client.
**Verify:** `go test ./internal/cli` passes.

## Task 15: Remove `qlvm install` from CLI

**Files:**
- `internal/cli/install.go` — delete
- `internal/cli/root.go` — remove the `install` subcommand registration
- `internal/setup/` — delete the entire package (or move to a separate
  provisioning tool outside this repo)
- `README.md` — update Quick start to reflect external provisioning

Changes:
- Remove the `install` subcommand from the CLI.
- Delete `internal/setup/` (the provisioning orchestration is no longer part
  of qlvm).
- Update the README: the Quick start now assumes the dom0 is provisioned
  externally. Document what external provisioning must do (install the daemon
  service, set up OVS/OVN/firewalld/networkd/storage, etc.) or point to the
  ostree recipe.

**Tests:** Remove install-related tests.
**Verify:** `go build ./cmd/qlvm` succeeds without the install command.

## Task 16: CLI drops root requirement

**File:** `cmd/qlvm/main.go`

Changes:
- The CLI no longer requires root for any command.
- Read commands: no auth needed.
- Mutation commands: polkit prompts via the daemon.
- `vm run`: runs as the user (SSH + waypipe), no root needed.

**Tests:** Verify CLI commands work without root.
**Verify:** `qlvm vm list` (no sudo) works; `qlvm vm start` prompts for auth.

## Task 17: Live smoke test

**Steps:**
1. `sudo make container-build` (produces `qlvm`, `qlvm-gui`, `qlvm-agent`).
2. External provisioning: install the daemon service (`sudo qlvm-agent
   install-system-files`), set up OVS/OVN/firewalld/networkd/storage (via
   ostree recipe or script), `sudo systemctl start qlvm-agent`.
3. `systemctl status qlvm-agent` (verify it's running).
4. **CLI (no sudo):**
   - `qlvm vm list` (read, no auth)
   - `qlvm vm start <vm>` (mutation, polkit prompt)
   - `qlvm vm stop <vm>`
   - `qlvm template list`
   - `qlvm vm run <vm> xterm` (CLI-side, waypipe)
5. **GUI (no sudo):**
   - Launch `qlvm-gui` from a Sway session.
   - VM list populates (read via D-Bus).
   - Start/Stop a VM (mutation, polkit prompt).
   - Create a VM (mutation, polkit prompt).
   - Delete the created VM.
   - Run an app via waypipe (GUI-side, no daemon involvement).
   - Create a template (long-running, progress signals appear).
6. `systemctl stop qlvm-agent`; verify both CLI and GUI show a connection
   error and recover when the daemon restarts.

**Verify:** All operations work via both interfaces; polkit prompts appear
for mutations; neither CLI nor GUI requires root; waypipe works as the user.

## Notes

### D-Bus method signatures

godbus marshals Go structs to D-Bus variants. For list methods, use slices
of structs which marshal to `a{a{sv}}` or similar. Keep the signatures
simple: prefer flat structs over nested types. The existing `internal/fw`
and `internal/systemd` packages demonstrate the pattern.

### Polkit interaction model

When the GUI calls a mutation method, the daemon checks polkit. If the user
is not authorized, polkit shows a desktop auth prompt (via the session bus).
The user enters their password, polkit returns `AUTHORIZATION_OK`, and the
daemon proceeds. This is synchronous from the GUI's perspective: the method
call blocks until the polkit decision is made.

For a smoother UX, the GUI can check authorization **before** calling the
method (via `org.freedesktop.PolicyKit1.CheckAuthorization`) and show its own
auth dialog. This is optional for v1.

### Daemon restart on config change

If `/etc/qlvm/qlvm.yaml` changes, the daemon needs to reload. Options:
1. `SIGHUP` triggers a config reload.
2. External provisioning restarts the daemon after writing the config.
3. The daemon watches the config file with inotify.

v1: use option 1 (`SIGHUP` reload). The user or provisioning script sends
`systemctl reload qlvm-agent` after editing the config.

### Existing GUI code in worktree

The `qlvm-ui` worktree has a partial GUI implementation that imports internal
packages directly. Tasks 11-13 migrate that code to use the D-Bus client.
The worktree's `internal/gui/` files are the starting point for the
migration.

### External provisioning requirements

With `qlvm install` removed, external provisioning (ostree recipe or script)
must handle:
1. Installing the `qlvm` and `qlvm-agent` binaries to `/usr/local/bin/`.
2. Running `qlvm-agent install-system-files` (writes service, D-Bus policy,
   polkit action).
3. Enabling and starting `qlvm-agent.service`.
4. Setting up OVS bridges, OVN switches/routers, firewalld rules, networkd
   config, `/var/lib/qlvm` storage tree, `/etc/xen/scripts/vif-ovn`.
5. Writing the initial `/etc/qlvm/qlvm.yaml`.

This is a significant shift: the provisioning logic currently in
`internal/setup` must be moved elsewhere. Options:
- Into the ostree recipe (`../os/recipes/recipe.yml`) as post-install hooks.
- Into a separate `qlvm-provision` tool (outside this repo).
- Into a one-time script in the repo's `provision/` dir (already exists).

The reconciliation property of `install` (idempotent re-run to fix drift)
 must be preserved in whatever replaces it.

### Security hardening

The systemd unit includes standard Fedora hardening directives
(`ProtectSystem=strict`, `NoNewPrivileges`, etc.). Tune `ReadWritePaths` as
needed based on live testing — the initial list covers the known paths
(`/var/lib/qlvm`, `/run/xen`, `/run/openvswitch`, `/run/ovn`, `/run/podman`).
