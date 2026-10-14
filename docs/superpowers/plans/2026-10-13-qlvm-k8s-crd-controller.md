# qlvm Kubernetes CRD + Controller (Lightweight Option A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a lightweight Kubernetes control plane (kube-apiserver + kine/SQLite) and a controller binary to qlvm, exposing VMs, Templates, and Domains as CRDs with watch-based reconciliation. No kubelet, no scheduler, no container runtime — just the API server and qlvm's controller.

**Architecture:** Three processes at runtime: `kine` (SQLite etcd shim) → `kube-apiserver` (REST API) → `qlvm-controller` (controller-runtime watching CRDs, calling existing xenlight/libovsdb/godbus libraries). A new `qlvm k8s` CLI group starts/stops the control plane. Existing `qlvm vm/template` commands continue to work unchanged; users manage CRs via `kubectl` or the CLI.

**Tech Stack:** Go (existing), controller-runtime (sigs.k8s.io/controller-runtime), k8s.io/client-go, k8s.io/apimachinery, k8s.io/apiextensions-apiserver, controller-gen code generator, kine binary (k3s-io/kine), kube-apiserver binary. Existing qlvm packages (xenctl, ovn, ovs, template, vm, config) are reused as actuators.

**Spec:** Research notes from 2026-10-13 (Option A: apiserver + kine + controller). Reference implementations: [k8senv](https://github.com/giantswarm/k8senv) (subprocess management pattern), [kine](https://github.com/k3s-io/kine) (SQLite etcd shim).

**Preceding plan:** `docs/superpowers/plans/2026-09-26-qlvm-implementation.md` (existing qlvm codebase).

## Global Constraints

- Module path: `github.com/jcpowermac/qlvm` (unchanged).
- **TDD:** every task starts with a failing test; no implementation code before a test exists for it.
- `golangci-lint run` must pass at every commit.
- **Clean repo:** no vendor names, no real IPs. Example network space `10.100.x` / `192.168.1.x` only.
- **No `os/exec` for management planes** — but subprocess management of kine/kube-apiserver is explicitly permitted here (they are the control plane infrastructure, not management-plane operations). The existing `waypipe` exec exception still applies.
- cgo remains allowed (xenlight).
- All controller logic behind interfaces so unit tests use `envtest`-style fake clients or real in-process apiserver (via the kine+apiserver harness built in Task 2).
- Integration tests use `//go:build integration` and never run in default `go test ./...`.
- **Binary distribution:** kine and kube-apiserver are external binaries. The `qlvm k8s start` command must check for their presence and provide clear error messages with install instructions. Do not embed them in the qlvm binary.
- **Storage:** Single SQLite file at `/var/lib/qvm/k8s/kine.db`. API server state directory at `/var/lib/qvm/k8s/`.
- **API group:** `qlvm.io`, version `v1`.
- **Namespace:** All CRs live in the `default` namespace (single-node, no multi-tenancy).

## CRD Model

### VM (`qlvm.io/v1 VM`)
Maps to existing `internal/vm.Meta` + `internal/config.VMDefaults`.

```yaml
apiVersion: qlvm.io/v1
kind: VM
metadata:
  name: work-laptop
spec:
  template: fedora-44-a1b2c3d4    # template CR name or dir slug
  domain: work                    # domain CR name
  type: app                       # app | disposable
  memoryMB: 4096
  vcpus: 2
  mounts:                         # optional
    - host: /home/user/projects
      guest: /mnt/projects
  workspaceSizeMB: 0              # optional, 0 = none
status:
  state: running                  # absent | created | running | stopped
  ip: 10.100.1.10
  mac: aa:bb:cc:dd:ee:ff
  uuid: ...
  conditions:
    - type: Ready
      status: "True"
      reason: Running
      message: VM is running
```

### Template (`qlvm.io/v1 Template`)
Maps to existing template directory + META file.

```yaml
apiVersion: qlvm.io/v1
kind: Template
metadata:
  name: fedora-44
spec:
  imageRef: quay.io/example/fedora:44
  force: false                    # trigger re-bake when true (auto-resets)
status:
  state: ready                    # baking | ready | failed
  dir: fedora-44-a1b2c3d4         # dir slug
  digest: sha256:...
  kernel: 6.15.0-...
  rootUUID: ...
  ostreePath: ...
  sizeBytes: ...
  conditions:
    - type: Ready
      status: "True"
      reason: Built
      message: Template built successfully
```

### Domain (`qlvm.io/v1 Domain`)
Maps to existing `internal/config.Domain`.

```yaml
apiVersion: qlvm.io/v1
kind: Domain
metadata:
  name: work
spec:
  subnet: "10.100.1"              # first three octets
  gateway: "10.100.1.1"
status:
  state: ready                    # provisioning | ready | failed
  switchName: qlvm-ds-work
  routerName: qlvm-gw-work
  conditions:
    - type: Ready
      status: "True"
      reason: Provisioned
      message: OVN switch and router created
```

## Review Focus

1. **kine/apiserver startup ordering**: apiserver must not start until kine's TCP port is ready and serving. → test in Task 2 (startup sequence).
2. **CRD installation before controller start**: controller must not begin watching until all 3 CRDs are Established. → test in Task 3 (controller wiring).
3. **VM controller idempotency**: reconcile must be safe to call multiple times with the same spec (no duplicate OVN ports, no duplicate libxl domains). → test in Task 4 (reconcile idempotency).
4. **Template bake failure handling**: if image-builder fails, status must be `failed` with a condition message containing the error, and the CR must be re-queueable (not permanently stuck). → test in Task 5 (failure path).
5. **Graceful shutdown**: `qlvm k8s stop` must stop controller → apiserver → kine in reverse order, preserving the SQLite DB. Running VMs must NOT be killed on control plane stop. → test in Task 7 (shutdown).
6. **Port binding conflicts**: if ports 6443 or 2379 are in use, startup must fail with a clear error, not hang. → test in Task 2 (port conflict).

---

### Task 1: Controller-runtime scaffolding + CRD types

**Files:**
- Create: `api/qlvm.io/v1/groupversion_info.go`
- Create: `api/qlvm.io/v1/vm_types.go`, `api/qlvm.io/v1/vm_types_test.go`
- Create: `api/qlvm.io/v1/template_types.go`, `api/qlvm.io/v1/template_types_test.go`
- Create: `api/qlvm.io/v1/domain_types.go`, `api/qlvm.io/v1/domain_types_test.go`
- Create: `api/qlvm.io/v1/zz_generated.deepcopy.go` (generated by controller-gen)
- Create: `config/crd/qlvm.io_vms.yaml`, `config/crd/qlvm.io_templates.yaml`, `config/crd/qlvm.io_domains.yaml` (generated)
- Create: `Makefile` targets: `generate`, `manifests`, `controller-gen`
- Modify: `go.mod` (add deps)

**Ruling (controller):** CRD Go types live in `api/qlvm.io/v1/` following the standard kubebuilder layout. `groupversion_info.go` registers the scheme. Each `*_types.go` defines the Spec/Status structs + `GetConditions`/`SetConditions` helpers using `k8s.io/apimachinery/pkg/apis/meta/v1` conditions. Deepcopy is generated by controller-gen, not hand-written. CRD YAML manifests are generated by controller-gen and committed to `config/crd/`.

**Interfaces:**
- Produces:
  - `type VM struct { metav1.TypeMeta; metav1.ObjectMeta; Spec VMSpec; Status VMStatus }`
  - `type VMSpec struct { Template string; Domain string; Type string; MemoryMB int; VCPUs int; Mounts []MountSpec; WorkspaceSizeMB int }`
  - `type VMStatus struct { State string; IP string; MAC string; UUID string; Conditions []metav1.Condition }`
  - `type Template struct { metav1.TypeMeta; metav1.ObjectMeta; Spec TemplateSpec; Status TemplateStatus }`
  - `type TemplateSpec struct { ImageRef string; Force bool }`
  - `type TemplateStatus struct { State string; Dir string; Digest string; Kernel string; RootUUID string; OstreePath string; SizeBytes int64; Conditions []metav1.Condition }`
  - `type Domain struct { metav1.TypeMeta; metav1.ObjectMeta; Spec DomainSpec; Status DomainStatus }`
  - `type DomainSpec struct { Subnet string; Gateway string }`
  - `type DomainStatus struct { State string; SwitchName string; RouterName string; Conditions []metav1.Condition }`
  - `type MountSpec struct { Host string; Guest string }`
  - `func SchemeBuilder runtime.SchemeBuilder`
  - `func AddToScheme(s *runtime.Scheme) error`
  - `func (vm *VM) GetConditions() []metav1.Condition` (and SetConditions)
  - `func (tmpl *Template) GetConditions() []metav1.Condition` (and SetConditions)
  - `func (dom *Domain) GetConditions() []metav1.Condition` (and SetConditions)

- [ ] **Step 1: Write the failing tests**

`api/qlvm.io/v1/vm_types_test.go`: `TestVMDefaulting` (create VM with minimal spec, assert Type defaults to "app"); `TestVMValidation` (subcases: MemoryMB < 64 → invalid, empty Template → invalid, empty Domain → invalid, Type not app/disposable → invalid); `TestVMConditions` (SetConditions then GetConditions round-trip).

`api/qlvm.io/v1/template_types_test.go`: `TestTemplateDefaulting` (Force defaults to false); `TestTemplateValidation` (empty ImageRef → invalid); `TestTemplateConditions`.

`api/qlvm.io/v1/domain_types_test.go`: `TestDomainValidation` (subcases: Subnet with 4 octets → invalid, Subnet with 2 octets → invalid, Gateway not in subnet → invalid); `TestDomainConditions`.

`api/qlvm.io/v1/groupversion_info_test.go`: `TestAddToScheme` (register scheme, assert all 3 CRD types are in the scheme).

- [ ] **Step 2: Verify failure**

Run: `go test ./api/qlvm.io/v1/...` — FAIL: package/identifier undefined.

First, add deps: `go get sigs.k8s.io/controller-runtime@latest k8s.io/apiextensions-apiserver@latest k8s.io/apimachinery@latest k8s.io/client-go@latest sigs.k8s.io/controller-tools/cmd/controller-gen@latest`.

- [ ] **Step 3: Implement**

`api/qlvm.io/v1/groupversion_info.go`: standard kubebuilder group/version info (Group `qlvm.io`, Version `v1`).

`api/qlvm.io/v1/vm_types.go`: VM/VMSpec/VMStatus/MountSpec structs with kubebuilder markers (`+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:printcolumn` for state/ip/domain). Validation logic in a `Validate() error` method.

`api/qlvm.io/v1/template_types.go`: Template/TemplateSpec/TemplateStatus with markers. Validation.

`api/qlvm.io/v1/domain_types.go`: Domain/DomainSpec/DomainStatus with markers. Validation (subnet format, gateway in subnet).

`Makefile` targets:
```makefile
CONTROLLER_GEN = $(GOBIN)/controller-gen

generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

manifests: controller-gen
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=config/crd

controller-gen:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
```

Run `make generate manifests` to produce `zz_generated.deepcopy.go` and `config/crd/*.yaml`.

- [ ] **Step 4: Verify pass**

Run: `go test ./api/qlvm.io/v1/... && make lint` — PASS. Verify `config/crd/qlvm.io_vms.yaml` exists and contains the VM CRD schema with the expected fields.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: CRD types for VM, Template, Domain"
```

---

### Task 2: Control plane manager (kine + kube-apiserver)

**Files:**
- Create: `internal/apimgr/apimgr.go`, `internal/apimgr/apimgr_test.go`
- Create: `internal/apimgr/kine.go`, `internal/apimgr/kine_test.go`
- Create: `internal/apimgr/apiserver.go`, `internal/apimgr/apiserver_test.go`
- Create: `internal/apimgr/certs.go`, `internal/apimgr/certs_test.go`
- Create: `internal/apimgr/config.go`
- Create: `hack/boilerplate.go.txt` (if not present)

**Ruling (controller):** `internal/apimgr` manages the kine and kube-apiserver subprocesses. It follows the k8senv pattern: start kine first, wait for TCP readiness, then start kube-apiserver. Shutdown is reverse order. TLS certificates are generated on first start and cached. Token auth is used for client authentication (simple, no CA needed for client certs). The manager exposes a `rest.Config` for the controller to use.

State directory: `/var/lib/qvm/k8s/`
- `kine.db` — SQLite database
- `certs/` — TLS certificates (ca.crt, server.crt, server.key, client.crt, client.key)
- `tokens.csv` — auth tokens
- `kubeconfig` — generated kubeconfig for kubectl/controller

Ports: apiserver on `:6443`, kine on `:2379` (both configurable via `apimgr.Config`).

**Interfaces:**
- Produces:
  - `type Config struct { StateDir string; APIServerAddr string; KineAddr string; APIServerBinary string; KineBinary string }`
  - `type Manager struct { /* unexported */ }`
  - `func New(cfg Config) (*Manager, error)`
  - `func (m *Manager) Start(ctx context.Context) error` // starts kine, waits, starts apiserver, waits
  - `func (m *Manager) Stop(ctx context.Context) error` // stops apiserver, then kine
  - `func (m *Manager) RESTConfig() (*rest.Config, error)` // returns config for client-go
  - `func (m *Manager) IsRunning() bool`
  - `func (m *Manager) APIServerURL() string`
  - `func (m *Manager) PIDFile() string` // for status command
  - `func (m *Manager) WriteKubeconfig(path string) error` // for kubectl integration
  - `func FindBinary(name string, fallbacks []string) (string, error)` // locate kine/kube-apiserver

- [ ] **Step 1: Write the failing tests**

`internal/apimgr/config_test.go`: `TestConfigDefaults` (empty Config fills in defaults: StateDir=/var/lib/qvm/k8s, ports); `TestConfigValidate` (invalid port → error).

`internal/apimgr/certs_test.go`: `TestGenerateCerts` (generate certs in temp dir, verify files exist, server cert is valid TLS, can be parsed); `TestCertsReuse` (generate twice, second time should reuse existing certs).

`internal/apimgr/kine_test.go`: `TestKineStartStop` (if kine binary available, start, wait for port, stop, verify port closed; skip if binary not found); `TestKinePortConflict` (bind port first, try start, expect error).

`internal/apimgr/apiserver_test.go`: `TestAPIServerStartStop` (if kube-apiserver available, start with kine, wait for /healthz, stop; skip if binary not found).

`internal/apimgr/apimgr_test.go`: `TestManagerStartStop` (full lifecycle: Start, RESTConfig, Stop; skip if binaries not found); `TestManagerStopWithoutStart` (no error); `TestFindBinary` (subcases: binary in PATH, binary in fallback path, binary not found).

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/apimgr/...` — FAIL: package/identifier undefined.

- [ ] **Step 3: Implement**

`internal/apimgr/config.go`: Config struct with defaults and validation.

`internal/apimgr/certs.go`: Generate CA, server cert (CN=localhost, SANs: 127.0.0.1, ::1), client cert. Use `crypto/tls` + `crypto/x509`. Cache in `certs/` dir; reuse if exists.

`internal/apimgr/kine.go`:
```go
type Kine struct {
    cmd      *exec.Cmd
    addr     string
    dbPath   string
    readyCh  chan struct{}
}

func (k *Kine) Start(ctx context.Context) error {
    k.cmd = exec.CommandContext(ctx, k.binary,
        "--endpoint", "sqlite://"+k.dbPath,
        "--listen", k.addr,
    )
    // capture stdout/stderr to log
    k.cmd.Stdout = logWriter
    k.cmd.Stderr = logWriter
    err := k.cmd.Start()
    // wait for TCP readiness
    go k.waitReady()
}
```

`internal/apimgr/apiserver.go`:
```go
func (a *APIServer) Start(ctx context.Context, kineAddr string) error {
    a.cmd = exec.CommandContext(ctx, a.binary,
        "--etcd-servers", "https://"+kineAddr,
        "--etcd-cafile", caCert,
        "--etcd-certfile", clientCert,
        "--etcd-keyfile", clientKey,
        "--advertise-address", "127.0.0.1",
        "--secure-port", "6443",
        "--tls-cert-file", serverCert,
        "--tls-private-key-file", serverKey,
        "--service-account-key-file", saKey,
        "--service-account-signing-key-file", saKey,
        "--service-account-issuer", "https://kubernetes.default.svc",
        "--token-auth-file", tokensCSV,
        "--authorization-mode", "RBAC",
        "--enable-admission-plugins", "NodeRestriction",
        "--service-cluster-ip-range", "10.96.0.0/12",
        "--kubelet-preferred-address-types", "InternalIP",
        "--profiling", "false",
        "--audit-log-path", "/dev/null",
    )
    // wait for /healthz
}
```

`internal/apimgr/apimgr.go`: Manager that coordinates kine + apiserver lifecycle. Writes PID file. Generates kubeconfig.

`internal/apimgr/tokens.go`: Generate a random token, write to `tokens.csv` in kube-apiserver token format.

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/apimgr/...` — PASS (tests with binaries will skip if not installed). Verify with manual test: install kine + kube-apiserver binaries, run the integration test with `-tags=integration`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: kine + kube-apiserver control plane manager"
```

---

### Task 3: Controller framework wiring + CRD installation

**Files:**
- Create: `cmd/qlvm-controller/main.go`
- Create: `internal/controller/controller.go`, `internal/controller/controller_test.go`
- Modify: `Makefile` (add `controller` build target)

**Ruling (controller):** `cmd/qlvm-controller` is the controller binary. It starts a controller-runtime Manager with the REST config from apimgr, installs CRDs from `config/crd/*.yaml`, registers the VM/Template/Domain controllers (stubs in this task), and runs until stopped. The controller framework code in `internal/controller/controller.go` handles CRD installation and manager setup.

CRD installation: read YAML files from the embedded `config/crd/` directory (using `embed.FS`) or from a path on disk, apply them via the apiextensions client. Wait for all CRDs to be Established before starting controllers.

**Interfaces:**
- Produces:
  - `func NewManager(ctx context.Context, cfg apimgr.Config) (manager.Manager, error)`
  - `func InstallCRDs(ctx context.Context, client client.Client) error`
  - `func WaitForCRDs(ctx context.Context, client client.Client, crdNames []string) error`
  - `func RunControllers(ctx context.Context, mgr manager.Manager) error`
  - `cmd/qlvm-controller/main.go` — flags: `--state-dir`, `--kine-binary`, `--apiserver-binary`, `--log-level`

- [ ] **Step 1: Write the failing tests**

`internal/controller/controller_test.go`:
- `TestInstallCRDs` (start a test apiserver via apimgr or envtest, install CRDs, verify they exist in the API);
- `TestWaitForCRDs` (verify returns when CRDs are established);
- `TestNewManager` (create manager with fake config, verify it has the expected scheme);
- `TestRunControllersStubs` (register stub controllers, verify they start without error).

Note: For unit tests, use `sigs.k8s.io/controller-runtime/pkg/envtest` if available, or the apimgr harness with `-tags=integration`.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/controller/...` — FAIL.

- [ ] **Step 3: Implement**

`cmd/qlvm-controller/main.go`:
```go
func main() {
    var stateDir, kineBin, apiserverBin string
    var logLevel string
    flag.StringVar(&stateDir, "state-dir", "/var/lib/qvm/k8s", "...")
    // ... parse flags

    cfg := apimgr.Config{StateDir: stateDir, KineBinary: kineBin, APIServerBinary: apiserverBin}

    ctx := signals.SetupSignalHandler()

    mgr, err := controller.NewManager(ctx, cfg)
    if err != nil { log.Fatal(err) }

    err = mgr.Start(ctx)
    if err != nil { log.Fatal(err) }
}
```

`internal/controller/controller.go`:
```go
func NewManager(ctx context.Context, cfg apimgr.Config) (manager.Manager, error) {
    // start apimgr
    am := apimgr.New(cfg)
    if err := am.Start(ctx); err != nil { return nil, err }

    // get rest config
    restCfg := am.RESTConfig()

    // create controller-runtime manager
    scheme := runtime.NewScheme()
    qlvmv1.AddToScheme(scheme)
    // + standard k8s schemes

    mgr, err := manager.New(restCfg, manager.Options{
        Scheme: scheme,
        Metrics: metricsserver.Options{BindAddress: "0"}, // disable metrics
        LeaderElection: false, // single node
    })

    // install CRDs
    if err := InstallCRDs(ctx, mgr.GetClient()); err != nil { return nil, err }

    // register controllers (stubs)
    if err := (&VMReconciler{}).SetupWithManager(mgr); err != nil { return nil, err }
    // ...

    return mgr, nil
}

func InstallCRDs(ctx context.Context, client client.Client) error {
    // read config/crd/*.yaml, unmarshal into CustomResourceDefinition, apply
}
```

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/controller/...` — PASS. Build the controller binary: `go build -o bin/qlvm-controller ./cmd/qlvm-controller`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: controller framework wiring + CRD installation"
```

---

### Task 4: VM controller

**Files:**
- Create: `internal/controller/vm_controller.go`, `internal/controller/vm_controller_test.go`
- Create: `internal/controller/vm_actuator.go`, `internal/controller/vm_actuator_test.go`
- Create: `internal/controller/vm_actuator_fake.go`

**Ruling (controller):** The VM controller watches VM CRs and reconciles them. Reconcile logic:
- **Create**: If spec exists and no VM exists on disk, create VM (reflink template, bake networkd, create OVN port, create libxl domain).
- **Start**: If spec exists and VM is stopped, start it.
- **Stop**: If spec has `status.state=running` but VM is stopped, no-op. If user wants stop, they patch the CR or use CLI.
- **Delete**: If VM is deleted (finalizer), clean up OVN port, remove libxl domain, remove disk/meta.
- **Update**: If spec changes (memory, vcpus), update and restart VM. If template/domain changes, recreate VM.

The actuator interface wraps the existing `internal/vm`, `internal/ovn`, `internal/xenctl` packages so the controller can be unit tested with fakes.

**Interfaces:**
- Produces:
  - `type VMActuator interface {
      Create(ctx context.Context, spec VMSpec, tmplDir string) (*vm.Meta, error)
      Start(ctx context.Context, meta *vm.Meta) error
      Stop(ctx context.Context, meta *vm.Meta) error
      Delete(ctx context.Context, meta *vm.Meta) error
      GetState(ctx context.Context, name string) (string, error)
      List(ctx context.Context) ([]*vm.Meta, error)
    }`
  - `type RealVMActuator struct { /* uses internal/vm, internal/ovn, internal/xenctl */ }`
  - `func NewRealVMActuator(cfg *config.Config) *RealVMActuator`
  - `type VMReconciler struct { client client.Client; actuator VMActuator; config *config.Config }`
  - `func (r *VMReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error)`
  - `func (r *VMReconciler) SetupWithManager(mgr manager.Manager) error`

- [ ] **Step 1: Write the failing tests**

`internal/controller/vm_controller_test.go` (using fake client + fake actuator):
- `TestReconcileCreate` (VM CR with no finalizer → add finalizer, call actuator.Create, update status to created);
- `TestReconcileStart` (VM CR with finalizer, status absent → call actuator.Start, update status to running);
- `TestReconcileDelete` (VM CR with deletion timestamp → call actuator.Delete, remove finalizer);
- `TestReconcileIdempotent` (reconcile twice, actuator called once for create);
- `TestReconcileCreateError` (actuator.Create returns error → status failed, requeue);
- `TestReconcileSpecChange` (template changes → delete + recreate).

`internal/controller/vm_actuator_test.go` (integration, -tags=integration):
- `TestRealVMActuatorCreate` (create a real VM via actuator, verify disk exists, meta exists, OVN port exists);
- `TestRealVMActuatorStartStop` (start then stop, verify state transitions);
- `TestRealVMActuatorDelete` (delete, verify disk removed, OVN port removed).

`internal/controller/vm_actuator_fake.go`: FakeVMActuator that records calls and returns configurable results.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/controller/... -run TestReconcile` — FAIL.

- [ ] **Step 3: Implement**

`internal/controller/vm_actuator.go`: RealVMActuator that wraps existing packages:
```go
func (a *RealVMActuator) Create(ctx context.Context, spec VMSpec, tmplDir string) (*vm.Meta, error) {
    // 1. Find template dir
    // 2. Reflink disk (internal/mounts)
    // 3. Bake networkd config (internal/netd, internal/ostree)
    // 4. Create OVN port (internal/ovn)
    // 5. Create libxl domain (internal/xenctl)
    // 6. Write meta.yaml
    // 7. Update ssh config (internal/vm)
}
```

`internal/controller/vm_controller.go`:
```go
func (r *VMReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    var vm qlvmv1.VM
    if err := r.client.Get(ctx, req.NamespacedName, &vm); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // validate spec
    if err := vm.Validate(); err != nil {
        return ctrl.Result{}, r.setFailed(&vm, err)
    }

    // handle deletion
    if !vm.DeletionTimestamp.IsZero() {
        return r.reconcileDelete(ctx, &vm)
    }

    // add finalizer
    if !containsFinalizer(vm, vmFinalizer) {
        return r.addFinalizer(ctx, &vm)
    }

    // check current state
    state, err := r.actuator.GetState(ctx, vm.Name)
    // ...

    // reconcile based on state vs spec
    switch state {
    case "absent":
        return r.reconcileCreate(ctx, &vm)
    case "created":
        return r.reconcileStart(ctx, &vm)
    case "running":
        return r.reconcileUpdate(ctx, &vm)
    case "stopped":
        return r.reconcileStart(ctx, &vm)
    }
}
```

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/controller/...` — PASS. Run integration test with `-tags=integration` (requires kine + apiserver + Xen running).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: VM controller with reconcile loop"
```

---

### Task 5: Template controller

**Files:**
- Create: `internal/controller/template_controller.go`, `internal/controller/template_controller_test.go`
- Create: `internal/controller/template_actuator.go`, `internal/controller/template_actuator_test.go`
- Create: `internal/controller/template_actuator_fake.go`

**Ruling (controller):** The Template controller watches Template CRs. Reconcile logic:
- **Create**: If spec exists and no template dir, bake template via image-builder (existing `internal/template` code).
- **Force rebuild**: If spec.Force is true, delete existing dir and re-bake.
- **Status update**: After bake, update status with digest, kernel, rootUUID, ostreePath, sizeBytes.
- **Delete**: Remove template dir if no VMs reference it.

**Interfaces:**
- Produces:
  - `type TemplateActuator interface {
      Bake(ctx context.Context, imageRef string) (dir string, digest string, err error)
      Remove(ctx context.Context, dir string) error
      GetMeta(ctx context.Context, dir string) (*template.Meta, error)
      List(ctx context.Context) ([]string, error)
      ReferencedBy(ctx context.Context, dir string) ([]string, error)
    }`
  - `type RealTemplateActuator struct { /* uses internal/template */ }`
  - `type TemplateReconciler struct { client client.Client; actuator TemplateActuator }`
  - `func (r *TemplateReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error)`
  - `func (r *TemplateReconciler) SetupWithManager(mgr manager.Manager) error`

- [ ] **Step 1: Write the failing tests**

`internal/controller/template_controller_test.go`:
- `TestReconcileBake` (Template CR → call actuator.Bake, update status to ready);
- `TestReconcileForceRebuild` (spec.Force=true → delete + re-bake, reset Force to false);
- `TestReconcileBakeError` (actuator returns error → status failed, requeue after backoff);
- `TestReconcileDelete` (deletion timestamp → if not referenced, remove dir, remove finalizer; if referenced, block deletion with condition);
- `TestReconcileIdempotent` (status ready, no changes → no actuator calls).

`internal/controller/template_actuator_fake.go`: FakeTemplateActuator.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/controller/... -run TestTemplate` — FAIL.

- [ ] **Step 3: Implement**

`internal/controller/template_actuator.go`: RealTemplateActuator wrapping `internal/template`:
```go
func (a *RealTemplateActuator) Bake(ctx context.Context, imageRef string) (string, string, error) {
    // call existing internal/template.Create or equivalent
    // returns dir slug and digest
}
```

`internal/controller/template_controller.go`:
```go
func (r *TemplateReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    var tmpl qlvmv1.Template
    if err := r.client.Get(ctx, req.NamespacedName, &tmpl); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // handle deletion
    if !tmpl.DeletionTimestamp.IsZero() {
        return r.reconcileDelete(ctx, &tmpl)
    }

    // add finalizer
    if !containsFinalizer(tmpl, templateFinalizer) {
        return r.addFinalizer(ctx, &tmpl)
    }

    // check current state
    meta, err := r.actuator.GetMeta(ctx, tmpl.Status.Dir)
    if err != nil && errors.Is(err, os.ErrNotExist) {
        // need to bake
        if tmpl.Status.State == "baking" {
            // already baking, check if still in progress
            return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
        }
        tmpl.Status.State = "baking"
        r.client.Status().Update(ctx, &tmpl)

        // bake (this is long-running; in production, use a job or requeue)
        dir, digest, err := r.actuator.Bake(ctx, tmpl.Spec.ImageRef)
        if err != nil {
            tmpl.Status.State = "failed"
            r.setCondition(&tmpl, "Ready", metav1.ConditionFalse, "BakeFailed", err.Error())
            r.client.Status().Update(ctx, &tmpl)
            return ctrl.Result{RequeueAfter: time.Minute}, nil
        }

        tmpl.Status.State = "ready"
        tmpl.Status.Dir = dir
        tmpl.Status.Digest = digest
        // ... fill in other fields
        r.setCondition(&tmpl, "Ready", metav1.ConditionTrue, "Built", "Template built")
        r.client.Status().Update(ctx, &tmpl)
        return ctrl.Result{}, nil
    }

    // check force rebuild
    if tmpl.Spec.Force {
        return r.reconcileForceRebuild(ctx, &tmpl)
    }

    // already ready, update status from meta if needed
    return ctrl.Result{}, nil
}
```

Note: Baking is long-running (minutes). The controller should either:
1. Run the bake synchronously and requeue while in progress (simple, blocks the reconcile loop)
2. Spawn a goroutine and requeue (risky, hard to track)
3. Use a requeue-based polling approach (check if bake process is running)

For simplicity, use approach 1 with requeue: start bake, update status to "baking", return. On next reconcile, check if bake is done (dir exists). This requires the bake to be idempotent and trackable.

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/controller/...` — PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: Template controller with bake reconciliation"
```

---

### Task 6: Domain controller

**Files:**
- Create: `internal/controller/domain_controller.go`, `internal/controller/domain_controller_test.go`
- Create: `internal/controller/domain_actuator.go`, `internal/controller/domain_actuator_test.go`
- Create: `internal/controller/domain_actuator_fake.go`

**Ruling (controller):** The Domain controller watches Domain CRs. Reconcile logic:
- **Create**: If spec exists and OVN switch/router don't exist, create them via `internal/ovn.Reconcile`.
- **Update**: If subnet/gateway changes, update OVN.
- **Delete**: If no VMs in the domain, remove OVN switch/router. If VMs exist, block deletion.

**Interfaces:**
- Produces:
  - `type DomainActuator interface {
      Provision(ctx context.Context, domain config.Domain) (switchName, routerName string, err error)
      Remove(ctx context.Context, switchName string) error
      GetState(ctx context.Context, switchName string) (bool, error) // exists?
      VMsInDomain(ctx context.Context, domainName string) ([]string, error)
    }`
  - `type RealDomainActuator struct { /* uses internal/ovn */ }`
  - `type DomainReconciler struct { client client.Client; actuator DomainActuator }`
  - `func (r *DomainReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error)`
  - `func (r *DomainReconciler) SetupWithManager(mgr manager.Manager) error`

- [ ] **Step 1: Write the failing tests**

`internal/controller/domain_controller_test.go`:
- `TestReconcileProvision` (Domain CR → call actuator.Provision, update status to ready with switchName/routerName);
- `TestReconcileProvisionError` (actuator error → status failed, requeue);
- `TestReconcileDeleteEmpty` (no VMs → remove OVN, remove finalizer);
- `TestReconcileDeleteWithVMs` (VMs exist → block deletion, set condition);
- `TestReconcileIdempotent` (status ready, OVN exists → no actuator calls).

`internal/controller/domain_actuator_fake.go`: FakeDomainActuator.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/controller/... -run TestDomain` — FAIL.

- [ ] **Step 3: Implement**

`internal/controller/domain_actuator.go`: RealDomainActuator wrapping `internal/ovn`:
```go
func (a *RealDomainActuator) Provision(ctx context.Context, domain config.Domain) (string, string, error) {
    // call internal/ovn.Reconcile with the domain
    // returns switch name and router name
}
```

`internal/controller/domain_controller.go`: Similar pattern to VM/Template controllers.

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/controller/...` — PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: Domain controller with OVN reconciliation"
```

---

### Task 7: CLI k8s subcommands

**Files:**
- Create: `internal/cli/k8s.go`, `internal/cli/k8s_test.go`
- Modify: `internal/cli/root.go` (add k8s command group) — or register via init() pattern

**Ruling (controller):** `qlvm k8s` is a command group with subcommands:
- `qlvm k8s start` — start kine + apiserver + controller
- `qlvm k8s stop` — stop controller + apiserver + kine
- `qlvm k8s status` — show status of all 3 components
- `qlvm k8s logs` — show logs (tail)
- `qlvm k8s kubeconfig` — print/write kubeconfig for kubectl

The CLI uses `internal/apimgr` to manage the control plane. The controller is started as a subprocess (`cmd/qlvm-controller`).

**Interfaces:**
- Produces:
  - `func NewK8sCmd() *cobra.Command` (registered via init() pattern)
  - Subcommands: `start`, `stop`, `status`, `logs`, `kubeconfig`
  - Flags: `--state-dir`, `--kine-binary`, `--apiserver-binary`, `--controller-binary`

- [ ] **Step 1: Write the failing tests**

`internal/cli/k8s_test.go`:
- `TestK8sStartCmd` (command parses flags correctly);
- `TestK8sStopCmd` (command exists);
- `TestK8sStatusCmd` (command exists);
- `TestK8sKubeconfigCmd` (command writes kubeconfig to stdout/file).

Note: Actual start/stop tests require binaries; mark as integration.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cli/... -run TestK8s` — FAIL.

- [ ] **Step 3: Implement**

`internal/cli/k8s.go`:
```go
func init() {
    NewRootCmd().AddCommand(NewK8sCmd())
}

func NewK8sCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "k8s",
        Short: "Manage the lightweight Kubernetes control plane",
    }
    cmd.AddCommand(newStartCmd(), newStopCmd(), newStatusCmd(), newLogsCmd(), newKubeconfigCmd())
    return cmd
}

func newStartCmd() *cobra.Command {
    var stateDir, kineBin, apiserverBin, controllerBin string
    cmd := &cobra.Command{
        Use:   "start",
        Short: "Start kine, apiserver, and controller",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg := apimgr.Config{StateDir: stateDir, KineBinary: kineBin, APIServerBinary: apiserverBin}
            am := apimgr.New(cfg)

            // start kine + apiserver
            if err := am.Start(cmd.Context()); err != nil {
                return fmt.Errorf("start control plane: %w", err)
            }

            // start controller as subprocess
            ctrlCmd := exec.CommandContext(cmd.Context(), controllerBin,
                "--state-dir", stateDir,
            )
            ctrlCmd.Stdout = os.Stdout
            ctrlCmd.Stderr = os.Stderr
            if err := ctrlCmd.Start(); err != nil {
                am.Stop(cmd.Context())
                return fmt.Errorf("start controller: %w", err)
            }

            // write PIDs
            // ...

            fmt.Println("Control plane started")
            fmt.Printf("  kine: %d\n", am.KinePID())
            fmt.Printf("  apiserver: %d\n", am.APIServerPID())
            fmt.Printf("  controller: %d\n", ctrlCmd.Process.Pid)

            // if --foreground flag, wait; otherwise daemonize or return
            return nil
        },
    }
    cmd.Flags().StringVar(&stateDir, "state-dir", "/var/lib/qvm/k8s", "State directory")
    // ...
    return cmd
}
```

Note: Running as a daemon vs foreground. For simplicity, run in foreground with `--foreground` flag (default). To run as a service, use systemd unit files (documented in README).

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/cli/...` — PASS. Manual test: `qlvm k8s start --foreground` (requires binaries).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: qlvm k8s CLI commands"
```

---

### Task 8: End-to-end integration test

**Files:**
- Create: `internal/itest/k8s_e2e_test.go`

**Ruling (controller):** Full end-to-end test that verifies the entire system works:
1. Start control plane (kine + apiserver + controller)
2. Apply Domain CR via kubectl or client-go
3. Verify Domain is provisioned (OVN switch exists)
4. Apply Template CR via kubectl or client-go
5. Verify Template is baked
6. Apply VM CR via kubectl or client-go
7. Verify VM is created and running (Xen domain exists)
8. Patch VM CR to change memory
9. Verify VM is updated
10. Delete VM CR
11. Verify VM is cleaned up
12. Delete Template CR
13. Delete Domain CR
14. Stop control plane
15. Verify VMs are NOT killed on control plane stop (if any running)

This test requires a live Xen environment, so it's guarded by `//go:build integration` and an environment variable.

**Interfaces:**
- Produces:
  - `func TestK8sE2E(t *testing.T)` (guarded by `QVM_K8S_ITEST=1`)

- [ ] **Step 1: Write the failing test**

`internal/itest/k8s_e2e_test.go`:
```go
//go:build integration

package itest

import (
    "context"
    "os"
    "testing"
    "time"

    qlvmv1 "github.com/jcpowermac/qlvm/api/qlvm.io/v1"
    "github.com/jcpowermac/qlvm/internal/apimgr"
    "k8s.io/client-go/kubernetes/scheme"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestK8sE2E(t *testing.T) {
    if os.Getenv("QVM_K8S_ITEST") != "1" {
        t.Skip("set QVM_K8S_ITEST=1 to run")
    }

    ctx := context.Background()

    // 1. Start control plane
    cfg := apimgr.Config{StateDir: t.TempDir()}
    am := apimgr.New(cfg)
    if err := am.Start(ctx); err != nil {
        t.Fatalf("start control plane: %v", err)
    }
    defer am.Stop(ctx)

    // 2. Create client
    restCfg, err := am.RESTConfig()
    // ...
    k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
    // ...

    // 3. Apply Domain CR
    domain := &qlvmv1.Domain{
        ObjectMeta: metav1.ObjectMeta{Name: "test-work"},
        Spec: qlvmv1.DomainSpec{Subnet: "10.100.99", Gateway: "10.100.99.1"},
    }
    if err := k8sClient.Create(ctx, domain); err != nil {
        t.Fatalf("create domain: %v", err)
    }

    // 4. Wait for domain ready
    if err := waitForDomainReady(ctx, k8sClient, domain.Name, time.Minute); err != nil {
        t.Fatalf("wait domain ready: %v", err)
    }

    // 5. Apply Template CR (use a small test image)
    tmpl := &qlvmv1.Template{
        ObjectMeta: metav1.ObjectMeta{Name: "test-template"},
        Spec: qlvmv1.TemplateSpec{ImageRef: os.Getenv("QVM_K8S_ITEST_IMAGE")},
    }
    if err := k8sClient.Create(ctx, tmpl); err != nil {
        t.Fatalf("create template: %v", err)
    }

    // 6. Wait for template ready (baking takes minutes)
    if err := waitForTemplateReady(ctx, k8sClient, tmpl.Name, 10*time.Minute); err != nil {
        t.Fatalf("wait template ready: %v", err)
    }

    // 7. Apply VM CR
    vm := &qlvmv1.VM{
        ObjectMeta: metav1.ObjectMeta{Name: "test-vm"},
        Spec: qlvmv1.VMSpec{
            Template: "test-template",
            Domain:   "test-work",
            Type:     "app",
            MemoryMB: 512,
            VCPUs:    1,
        },
    }
    if err := k8sClient.Create(ctx, vm); err != nil {
        t.Fatalf("create vm: %v", err)
    }

    // 8. Wait for VM running
    if err := waitForVMRunning(ctx, k8sClient, vm.Name, time.Minute); err != nil {
        t.Fatalf("wait vm running: %v", err)
    }

    // 9. Verify Xen domain exists
    // ...

    // 10. Delete VM
    if err := k8sClient.Delete(ctx, vm); err != nil {
        t.Fatalf("delete vm: %v", err)
    }

    // 11. Verify VM cleaned up
    // ...

    // 12-14. Cleanup template, domain
    // 15. Stop control plane (deferred)
}
```

- [ ] **Step 2: Verify failure**

Run: `QVM_K8S_ITEST=1 go test -tags=integration ./internal/itest/... -run TestK8sE2E` — FAIL (expected, this is the first full integration test).

- [ ] **Step 3: Fix issues found**

Run the test and fix any issues in the controller, apimgr, or CLI code. This is the task where real bugs will surface (timing issues, race conditions, missing error handling).

- [ ] **Step 4: Verify pass**

Run: `QVM_K8S_ITEST=1 go test -tags=integration ./internal/itest/... -run TestK8sE2E` — PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "test: end-to-end K8s CRD controller integration test"
```

---

### Task 9: Documentation + README updates

**Files:**
- Modify: `README.md` (add K8s section)
- Create: `docs/k8s.md` (detailed K8s control plane documentation)
- Create: `hack/systemd/qlvm-k8s.service` (systemd unit file example)
- Modify: `Makefile` (add `install-k8s` target)

**Ruling (controller):** Document the K8s control plane: architecture, installation, usage, CRD reference, troubleshooting. Include systemd unit file for running as a service. Update README with a "Kubernetes Control Plane" section.

**Interfaces:**
- Produces:
  - `docs/k8s.md`: Architecture diagram, installation instructions (kine + kube-apiserver binaries), usage (kubectl examples), CRD reference, troubleshooting
  - `README.md`: New section "Kubernetes Control Plane (Optional)" with quick start
  - `hack/systemd/qlvm-k8s.service`: Systemd unit for running qlvm k8s as a service
  - `Makefile`: `install-k8s` target to install binaries + systemd unit

- [ ] **Step 1: Write documentation**

`docs/k8s.md`:
- Architecture diagram (ASCII)
- How it works (kine → apiserver → controller → Xen/OVN)
- Installation: `go install github.com/k3s-io/kine/cmd/kine@latest`, download kube-apiserver
- Usage: `qlvm k8s start`, `kubectl apply -f vm.yaml`, `kubectl get vm`
- CRD reference (full field documentation)
- Examples: create a VM via kubectl, watch status, delete
- Troubleshooting: common errors, logs location, debug tips
- Limitations: single-node, no multi-tenancy, no RBAC (token auth only)

`README.md`: Add "Kubernetes Control Plane (Optional)" section after the existing Quick Start.

`hack/systemd/qlvm-k8s.service`:
```ini
[Unit]
Description=qlvm Kubernetes Control Plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/qlvm k8s start --state-dir /var/lib/qvm/k8s --kine-binary /usr/local/bin/kine --apiserver-binary /usr/local/bin/kube-apiserver
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Verify**

Read through docs for accuracy. Verify systemd unit syntax.

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "docs: K8s control plane documentation"
```

---

## Execution Order

Tasks are sequential where there are dependencies, but some can be parallelized:

1. **Task 1** (CRD types) — must come first
2. **Task 2** (Control plane manager) — can start after Task 1 deps are in
3. **Task 3** (Controller wiring) — depends on Tasks 1 + 2
4. **Task 4** (VM controller) — depends on Task 3
5. **Task 5** (Template controller) — depends on Task 3 (parallel with Task 4)
6. **Task 6** (Domain controller) — depends on Task 3 (parallel with Tasks 4 + 5)
7. **Task 7** (CLI) — depends on Task 2
8. **Task 8** (E2E test) — depends on all above
9. **Task 9** (Docs) — depends on all above

Critical path: 1 → 2 → 3 → 4 → 8 → 9
Parallelizable: 5, 6 (after 3), 7 (after 2)

## Estimated Effort

- Task 1: 1-2 hours (CRD types + generation)
- Task 2: 3-4 hours (subprocess management, certs, TLS)
- Task 3: 2-3 hours (manager wiring, CRD install)
- Task 4: 4-6 hours (VM controller is the most complex)
- Task 5: 2-3 hours (Template controller)
- Task 6: 2-3 hours (Domain controller)
- Task 7: 2-3 hours (CLI commands)
- Task 8: 2-4 hours (E2E test + bug fixing)
- Task 9: 1-2 hours (docs)

Total: ~20-32 hours
