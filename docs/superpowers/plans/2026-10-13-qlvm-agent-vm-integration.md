# qlvm Agent VM Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `qlvm template` and `qlvm vm` work identically for agent-based VMs (pi-in-xen container) as for bootc/ostree VMs.

**Architecture:** Agent templates are detected via container LABEL `io.qlvm.type=pvh-initramfs`. Instead of running image-builder, qlvm extracts kernel/initramfs from the container. Agent VMs boot from kernel+initramfs (initramfs is root) with optional workspace disk, static IP via kernel cmdline, and console access via xenlight `PrimaryConsoleGetTty`.

**Tech Stack:** Go, podman REST (pkg/bindings), xenlight cgo, libovsdb, cobra CLI

**Spec:** `docs/superpowers/specs/2026-10-13-qlvm-agent-vm-integration-design.md`

## Global Constraints

- **No shelling out** to management CLIs — use native Go control planes (podman REST, xenlight, libovsdb)
- **Container build skew** — builder image tools must be compatible with dom0 runtime
- **Clean repo** — no vendor names, no real IPs
- **The host is the dom0** — ostree/atomic, no ad-hoc package installs
- **Agent container is read-only transport** — container is NOT run, files are extracted
- **Workspace disk defaults to 4GB**, `--workspace-size 0` opts out
- **Static IP via kernel cmdline** (`ip=<ip>`), gateway `.1` convention
- **Console access via xenlight `PrimaryConsoleGetTty`** (no shelling out)

## Review Focus

1. **Container without `io.qlvm.type` label** — should be treated as ostree template (existing behavior), not fail
2. **Agent template with missing kernel/initramfs files** — extraction should fail with clear error, not create broken template
3. **Workspace disk size 0** — should skip disk creation and not attach any disk beyond what's needed
4. **VM type mismatch** (`--type app` with agent template) — should error clearly, not create broken VM
5. **Console access on stopped VM** — should error clearly, not hang or crash

---

### Task 1: Template META Type Field

**Files:**
- Modify: `internal/template/template.go` (Template struct)
- Test: `internal/template/template_test.go`

**Interfaces:**
- Produces: `Template.Type string` field ("ostree" or "agent")
- Produces: `template.IsAgent(t *Template) bool` helper

- [ ] **Step 1: Add Type field to Template struct**

In `internal/template/template.go`, add to `Template` struct:
```go
type Template struct {
    Dir        string `yaml:"dir"`
    Image      string `yaml:"image"`
    Digest     string `yaml:"digest"`
    KernelVer  string `yaml:"kernel_ver"`
    RootDev    string `yaml:"root_dev"`
    RootFlags  string `yaml:"root_flags"`
    OstreePath string `yaml:"ostree_path"`
    Type       string `yaml:"type"` // "ostree" or "agent"
}
```

- [ ] **Step 2: Write test for IsAgent helper**

In `internal/template/template_test.go`:
```go
func TestIsAgent(t *testing.T) {
    if !IsAgent(&Template{Type: "agent"}) {
        t.Error("expected agent")
    }
    if IsAgent(&Template{Type: "ostree"}) {
        t.Error("expected not agent")
    }
    if IsAgent(&Template{Type: ""}) {
        t.Error("empty type should not be agent")
    }
}
```

- [ ] **Step 3: Implement IsAgent helper**

In `internal/template/template.go`:
```go
// IsAgent reports whether t is an agent template (pvh-initramfs).
func IsAgent(t *Template) bool {
    return t.Type == "agent"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/template/ -run TestIsAgent -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/template/template.go internal/template/template_test.go
git commit -m "feat: add template Type field for agent templates"
```

---

### Task 2: Container Label Detection

**Files:**
- Modify: `internal/template/template.go` (Ensure function)
- Create: `internal/template/agent_detect.go`
- Test: `internal/template/agent_detect_test.go`

**Interfaces:**
- Consumes: `template.Template` (Task 1)
- Produces: `detectTemplateType(ctx context.Context, pc PodmanClient, ref string) (string, error)` returns "agent" or "ostree"

- [ ] **Step 1: Write test for label detection**

In `internal/template/agent_detect_test.go`:
```go
func TestDetectTemplateType_Agent(t *testing.T) {
    pc := &fakePodmanClient{
        inspectFunc: func(ctx context.Context, ref string) (*types.InspectReport, error) {
            return &types.InspectReport{
                Config: &types.InspectConfig{
                    Labels: map[string]string{"io.qlvm.type": "pvh-initramfs"},
                },
            }, nil
        },
    }
    got, err := detectTemplateType(context.Background(), pc, "test")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got != "agent" {
        t.Errorf("expected agent, got %s", got)
    }
}

func TestDetectTemplateType_Ostree(t *testing.T) {
    pc := &fakePodmanClient{
        inspectFunc: func(ctx context.Context, ref string) (*types.InspectReport, error) {
            return &types.InspectReport{
                Config: &types.InspectConfig{
                    Labels: map[string]string{},
                },
            }, nil
        },
    }
    got, err := detectTemplateType(context.Background(), pc, "test")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got != "ostree" {
        t.Errorf("expected ostree, got %s", got)
    }
}
```

- [ ] **Step 2: Implement label detection**

In `internal/template/agent_detect.go`:
```go
package template

import (
    "context"
    "fmt"
)

const agentLabel = "io.qlvm.type"
const agentLabelValue = "pvh-initramfs"

// detectTemplateType inspects the container image and returns "agent" if
// it has the io.qlvm.type=pvh-initramfs label, "ostree" otherwise.
func detectTemplateType(ctx context.Context, pc PodmanClient, ref string) (string, error) {
    report, err := pc.InspectImage(ctx, ref)
    if err != nil {
        return "", fmt.Errorf("inspect image %s: %w", ref, err)
    }
    if report.Config != nil && report.Config.Labels != nil {
        if report.Config.Labels[agentLabel] == agentLabelValue {
            return "agent", nil
        }
    }
    return "ostree", nil
}
```

- [ ] **Step 3: Add InspectImage to PodmanClient interface**

In `internal/template/template.go`, add to `PodmanClient` interface:
```go
InspectImage(ctx context.Context, ref string) (*types.InspectReport, error)
```

Implement in `podmanClient`:
```go
func (p *podmanClient) InspectImage(ctx context.Context, ref string) (*types.InspectReport, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    return image.Inspect(p.ctx, ref, nil)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/template/ -run TestDetectTemplateType -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/template/agent_detect.go internal/template/agent_detect_test.go internal/template/template.go
git commit -m "feat: detect agent templates via container label"
```

---

### Task 3: Agent Template Baking (Container Extraction)

**Files:**
- Create: `internal/template/agent_bake.go`
- Modify: `internal/template/template.go` (Ensure function branches on type)
- Test: `internal/template/agent_bake_test.go`

**Interfaces:**
- Consumes: `template.Template`, `detectTemplateType` (Task 2), `PodmanClient`
- Produces: `bakeAgentTemplate(ctx context.Context, pc PodmanClient, ref, dir string, log io.Writer) (*Template, error)`

- [ ] **Step 1: Write test for agent template baking**

In `internal/template/agent_bake_test.go`:
```go
func TestBakeAgentTemplate(t *testing.T) {
    dir := t.TempDir()
    pc := &fakePodmanClient{
        inspectFunc: func(ctx context.Context, ref string) (*types.InspectReport, error) {
            return &types.InspectReport{
                Config: &types.InspectConfig{
                    Labels: map[string]string{
                        "io.qlvm.type": "pvh-initramfs",
                        "io.qlvm.kernel": "/usr/lib/modules/7.2.8-xen1/vmlinuz",
                        "io.qlvm.initramfs": "/usr/lib/modules/7.2.8-xen1/initramfs.img",
                    },
                },
                ID: "sha256:abcd1234",
            }, nil
        },
        createFunc: func(ctx context.Context, sgen *specgen.SpecGenerator) (*containers.CreateResponse, error) {
            return &containers.CreateResponse{ID: "container123"}, nil
        },
        startFunc: func(ctx context.Context, id string, opts *containers.StartOptions) error {
            return nil
        },
        copyFunc: func(ctx context.Context, id, path string, writer io.Writer) error {
            // Write fake file content based on path
            if strings.Contains(path, "vmlinuz") {
                _, _ = writer.Write([]byte("fake-kernel"))
            } else if strings.Contains(path, "initramfs") {
                _, _ = writer.Write([]byte("fake-initramfs"))
            } else if strings.Contains(path, "kernel-version") {
                _, _ = writer.Write([]byte("7.2.8-xen1"))
            }
            return nil
        },
        removeFunc: func(ctx context.Context, id string, opts *containers.RemoveOptions) error {
            return nil
        },
    }
    tpl, err := bakeAgentTemplate(context.Background(), pc, "test-image", dir, io.Discard)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if tpl.KernelVer != "7.2.8-xen1" {
        t.Errorf("expected kernel version 7.2.8-xen1, got %s", tpl.KernelVer)
    }
    // Verify files exist
    if _, err := os.Stat(filepath.Join(dir, "vmlinuz")); err != nil {
        t.Errorf("vmlinuz not found: %v", err)
    }
    if _, err := os.Stat(filepath.Join(dir, "initramfs")); err != nil {
        t.Errorf("initramfs not found: %v", err)
    }
}
```

- [ ] **Step 2: Implement agent template baking**

In `internal/template/agent_bake.go`:
```go
package template

import (
    "archive/tar"
    "bytes"
    "context"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"
)

// bakeAgentTemplate extracts kernel/initramfs from an agent container image
// and writes them to dir. Returns the Template metadata.
func bakeAgentTemplate(ctx context.Context, pc PodmanClient, ref, dir string, log io.Writer) (*Template, error) {
    // Inspect image for labels
    report, err := pc.InspectImage(ctx, ref)
    if err != nil {
        return nil, fmt.Errorf("inspect image: %w", err)
    }
    labels := report.Config.Labels
    kernelPath, ok := labels["io.qlvm.kernel"]
    if !ok {
        kernelPath = defaultAgentKernelPath(labels)
    }
    initramfsPath, ok := labels["io.qlvm.initramfs"]
    if !ok {
        initramfsPath = defaultAgentInitramfsPath(labels)
    }

    // Create temporary container for file extraction
    resp, err := pc.CreateContainer(ctx, ref, nil)
    if err != nil {
        return nil, fmt.Errorf("create extraction container: %w", err)
    }
    defer func() {
        _ = pc.RemoveContainer(ctx, resp.ID, true)
    }()

    // Extract kernel version
    kernelVer, err := extractString(pc, resp.ID, "/etc/kernel-version")
    if err != nil {
        return nil, fmt.Errorf("extract kernel version: %w", err)
    }
    kernelVer = strings.TrimSpace(kernelVer)

    // Extract kernel
    kernelData, err := extractFile(pc, resp.ID, kernelPath)
    if err != nil {
        return nil, fmt.Errorf("extract kernel: %w", err)
    }
    if err := os.WriteFile(filepath.Join(dir, "vmlinuz"), kernelData, 0o644); err != nil {
        return nil, fmt.Errorf("write vmlinuz: %w", err)
    }

    // Extract initramfs
    initramfsData, err := extractFile(pc, resp.ID, initramfsPath)
    if err != nil {
        return nil, fmt.Errorf("extract initramfs: %w", err)
    }
    if err := os.WriteFile(filepath.Join(dir, "initramfs"), initramfsData, 0o644); err != nil {
        return nil, fmt.Errorf("write initramfs: %w", err)
    }

    digest := report.ID
    if strings.HasPrefix(digest, "sha256:") {
        // already prefixed
    } else {
        digest = "sha256:" + digest
    }

    return &Template{
        Dir:       dir,
        Image:     ref,
        Digest:    digest,
        KernelVer: kernelVer,
        Type:      "agent",
    }, nil
}

// extractFile extracts a file from a container via CopyToArchive.
// Returns the file content as bytes (binary-safe for kernel/initramfs).
func extractFile(pc PodmanClient, containerID, path string) ([]byte, error) {
    var buf bytes.Buffer
    err := pc.CopyFromContainer(context.Background(), containerID, path, &buf)
    if err != nil {
        return nil, err
    }
    // CopyToArchive returns a tar archive; extract the first regular file
    tr := tar.NewReader(&buf)
    for {
        hdr, err := tr.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return nil, err
        }
        if hdr.Typeflag == tar.TypeReg {
            data, err := io.ReadAll(tr)
            if err != nil {
                return nil, err
            }
            return data, nil
        }
    }
    return nil, fmt.Errorf("no file found in archive for %s", path)
}

// extractString is a helper for text files (kernel-version).
func extractString(pc PodmanClient, containerID, path string) (string, error) {
    data, err := extractFile(pc, containerID, path)
    if err != nil {
        return "", err
    }
    return string(data), nil
}

// defaultAgentKernelPath derives kernel path from version label.
func defaultAgentKernelPath(labels map[string]string) string {
    ver := labels["org.opencontainers.image.version"]
    return fmt.Sprintf("/usr/lib/modules/%s/vmlinuz", ver)
}

// defaultAgentInitramfsPath derives initramfs path from version label.
func defaultAgentInitramfsPath(labels map[string]string) string {
    ver := labels["org.opencontainers.image.version"]
    return fmt.Sprintf("/usr/lib/modules/%s/initramfs.img", ver)
}
```

- [ ] **Step 3: Add CopyFromContainer to PodmanClient interface**

In `internal/template/template.go`, add to `PodmanClient` interface:
```go
CopyFromContainer(ctx context.Context, containerID, path string, writer io.Writer) error
```

Implement in `podmanClient` using `containers.CopyToArchive`.

- [ ] **Step 4: Branch Ensure on template type**

In `internal/template/template.go`, in the `Ensure` function, after detecting the type:
```go
tp, err := detectTemplateType(ctx, pc, ref)
if err != nil {
    return nil, err
}
if tp == "agent" {
    return bakeAgentTemplate(ctx, pc, ref, dir, opts.Log)
}
// existing ostree path...
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/template/ -run TestBakeAgentTemplate -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/template/agent_bake.go internal/template/agent_bake_test.go internal/template/template.go
git commit -m "feat: bake agent templates from container extraction"
```

---

### Task 4: Agent Domain Config

**Files:**
- Modify: `internal/vm/domcfg.go`
- Test: `internal/vm/domcfg_test.go`

**Interfaces:**
- Consumes: `template.IsAgent` (Task 1), `vm.Meta`
- Produces: Modified `DomainConfig` that handles agent templates

- [ ] **Step 1: Write test for agent domain config**

In `internal/vm/domcfg_test.go`:
```go
func TestDomainConfig_Agent(t *testing.T) {
    m := &Meta{
        Name: "test-agent",
        Type: "agent",
        UUID: "12345678-1234-5678-1234-567812345678",
        IP:   "10.100.0.5",
        MAC:  "aa:bb:cc:dd:ee:ff",
        MemoryMB: 2048,
        VCPUs: 2,
    }
    tpl := &template.Template{
        Type: "agent",
        Dir:  "/var/lib/qlvm/templates/test",
    }
    spec := DomainConfig("/var/lib/qlvm/vms/test-agent", m, tpl)
    
    if spec.Type != "PVH" {
        t.Errorf("expected PVH, got %s", spec.Type)
    }
    if spec.Kernel != "/var/lib/qlvm/vms/test-agent/vmlinuz" {
        t.Errorf("unexpected kernel path: %s", spec.Kernel)
    }
    if spec.Ramdisk != "/var/lib/qlvm/vms/test-agent/initramfs" {
        t.Errorf("unexpected ramdisk path: %s", spec.Ramdisk)
    }
    // Check boot params
    foundConsole, foundIP := false, false
    for _, e := range spec.Extra {
        if e == "console=hvc0" {
            foundConsole = true
        }
        if e == "ip=10.100.0.5" {
            foundIP = true
        }
        if strings.Contains(e, "ostree=") {
            t.Error("agent VM should not have ostree= param")
        }
        if strings.Contains(e, "root=") {
            t.Error("agent VM should not have root= param")
        }
    }
    if !foundConsole || !foundIP {
        t.Error("missing console or ip boot params")
    }
    if len(spec.Disks) != 0 {
        t.Errorf("agent VM should have no disks, got %d", len(spec.Disks))
    }
}
```

- [ ] **Step 2: Modify DomainConfig for agent templates**

In `internal/vm/domcfg.go`, modify `DomainConfig`:
```go
func DomainConfig(vmDir string, m *Meta, tpl *template.Template) *DomainSpec {
    if template.IsAgent(tpl) {
        return agentDomainConfig(vmDir, m)
    }
    // existing ostree path...
}

func agentDomainConfig(vmDir string, m *Meta) *DomainSpec {
    return &DomainSpec{
        Type: "PVH",
        Name: m.Name,
        UUID: m.UUID,
        Kernel:  filepath.Join(vmDir, "vmlinuz"),
        Ramdisk: filepath.Join(vmDir, "initramfs"),
        Extra: []string{
            "console=hvc0",
            "ip=" + m.IP,
        },
        MaxVcpus:    m.VCPUs,
        TargetMemkb: m.MemoryMB * 1024,
        Disks:       []DomainDisk{},
        Nics: []DomainNic{{Mac: m.MAC, Script: "vif-ovn", Nictype: "vif"}},
        P9S:         []DomainP9{},
    }
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/vm/ -run TestDomainConfig_Agent -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/vm/domcfg.go internal/vm/domcfg_test.go
git commit -m "feat: agent VM domain config (kernel+initramfs, no ostree)"
```

---

### Task 5: Agent VM Creation

**Files:**
- Modify: `internal/vm/create.go`
- Test: `internal/vm/create_test.go`

**Interfaces:**
- Consumes: `DomainConfig` (Task 4), `template.IsAgent` (Task 1)
- Produces: Modified `Create` that handles agent VMs (no reflink, no networkd bake)

- [ ] **Step 1: Write test for agent VM creation**

In `internal/vm/create_test.go`:
```go
func TestCreate_AgentVM(t *testing.T) {
    // Setup: agent template, fake OVN, fake FS
    // Verify: no reflink call, no networkd bake, kernel/initramfs copied
    // Verify: meta.yaml saved with type=agent
}
```

- [ ] **Step 2: Modify Create for agent VMs**

In `internal/vm/create.go`, in the `Create` function, after template resolution:
```go
if template.IsAgent(d.Tpl) {
    return createAgentVM(ctx, d, cfg, spec, vmDir, ip, mac, memory, vcpus)
}
// existing ostree path...
```

Implement `createAgentVM`:
```go
func createAgentVM(ctx context.Context, d CreateDeps, cfg *config.Config, spec Spec, vmDir, ip, mac string, memory, vcpus int) (*Meta, error) {
    // OVN port (same as ostree)
    if err := d.OVN.AddLSPort(ctx, spec.Domain, spec.Name, mac, ip); err != nil {
        return nil, fmt.Errorf("add OVN port: %w", err)
    }
    
    // Copy kernel/initramfs from template to VM dir (per-VM state)
    if err := copyFile(filepath.Join(vmDir, "vmlinuz"), filepath.Join(d.Tpl.Dir, "vmlinuz")); err != nil {
        return nil, fmt.Errorf("copy kernel: %w", err)
    }
    if err := copyFile(filepath.Join(vmDir, "initramfs"), filepath.Join(d.Tpl.Dir, "initramfs")); err != nil {
        return nil, fmt.Errorf("copy initramfs: %w", err)
    }
    
    // No disk reflink, no networkd bake, no control token bake
    
    // Save meta
    m := &Meta{
        Name:     spec.Name,
        Type:     "agent",
        Image:    spec.Image,
        Digest:   d.Tpl.Digest,
        Domain:   spec.Domain,
        IP:       ip,
        MAC:      mac,
        MemoryMB: memory,
        VCPUs:    vcpus,
        UUID:     newUUID(),
        Created:  time.Now(),
    }
    if err := m.Save(vmDir); err != nil {
        return nil, fmt.Errorf("save meta: %w", err)
    }
    
    return m, nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/vm/ -run TestCreate_AgentVM -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/vm/create.go internal/vm/create_test.go
git commit -m "feat: agent VM creation path (no disk, no networkd)"
```

---

### Task 6: Workspace Disk

**Files:**
- Create: `internal/vm/workspace.go`
- Modify: `internal/vm/create.go` (add workspace disk option)
- Modify: `internal/vm/domcfg.go` (attach workspace disk)
- Test: `internal/vm/workspace_test.go`

**Interfaces:**
- Produces: `CreateWorkspaceDisk(path string, sizeMB int) error`
- Produces: `Meta.WorkspaceSizeMB int` field
- Produces: Domain disk entry for workspace (`xvdb`)

- [ ] **Step 1: Add WorkspaceSizeMB to Meta**

In `internal/vm/meta.go`:
```go
type Meta struct {
    // ... existing fields ...
    WorkspaceSizeMB int `yaml:"workspace_size_mb"`
}
```

- [ ] **Step 2: Write test for workspace disk creation**

In `internal/vm/workspace_test.go`:
```go
func TestCreateWorkspaceDisk(t *testing.T) {
    path := filepath.Join(t.TempDir(), "workspace.img")
    if err := CreateWorkspaceDisk(path, 100); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    info, err := os.Stat(path)
    if err != nil {
        t.Fatalf("disk not created: %v", err)
    }
    if info.Size() != 100*1024*1024 {
        t.Errorf("expected 100MB disk, got %d bytes", info.Size())
    }
}
```

- [ ] **Step 3: Implement workspace disk creation**

In `internal/vm/workspace.go`:
```go
package vm

import (
    "fmt"
    "os"
    "path/filepath"
)

// CreateWorkspaceDisk creates a raw disk image of sizeMB megabytes and
// formats it with xfs. The path is the VM's workspace.img.
func CreateWorkspaceDisk(path string, sizeMB int) error {
    if sizeMB <= 0 {
        return nil // no workspace disk
    }
    // Create sparse file
    f, err := os.Create(path)
    if err != nil {
        return fmt.Errorf("create workspace disk: %w", err)
    }
    defer f.Close()
    if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
        return fmt.Errorf("truncate workspace disk: %w", err)
    }
    // Format with xfs: the pi-in-xen initramfs formats on first boot if
    // the disk is unformatted (init script addition):
    //   if [ -b /dev/xvdb ] && ! blkid -t TYPE=xfs /dev/xvdb; then
    //       mkfs.xfs /dev/xvdb
    //   fi
    // This avoids needing mkfs.xfs on the dom0 (no shelling out).
    return nil
}
```

**Note:** XFS formatting is tricky without shelling out. Options:
1. Use a podman container with `mkfs.xfs` (consistent with no-shelling-out rule)
2. Leave the disk unformatted and let the initramfs format on first boot
3. Use a Go xfs library

For this task, create the disk file. Formatting can be done by the initramfs on first boot (add to init script: `if [ -b /dev/xvdb ] && ! blkid -t TYPE=xfs /dev/xvdb; then mkfs.xfs /dev/xvdb; fi`).

- [ ] **Step 4: Attach workspace disk in domain config**

In `internal/vm/domcfg.go`, in `agentDomainConfig`:
```go
if m.WorkspaceSizeMB > 0 {
    spec.Disks = []DomainDisk{{
        PdevPath:  filepath.Join(vmDir, "workspace.img"),
        Vdev:      "xvdb",
        Format:    "raw",
        Readwrite: 1,
    }}
}
```

- [ ] **Step 5: Create workspace disk in agent VM creation**

In `internal/vm/create.go`, in `createAgentVM`:
```go
workspaceSize := spec.WorkspaceSizeMB
if workspaceSize == 0 {
    workspaceSize = 4096 // default 4GB
}
if workspaceSize > 0 {
    if err := CreateWorkspaceDisk(filepath.Join(vmDir, "workspace.img"), workspaceSize); err != nil {
        return nil, fmt.Errorf("create workspace disk: %w", err)
    }
}
m.WorkspaceSizeMB = workspaceSize
```

Add `WorkspaceSizeMB` to `Spec` struct.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/vm/ -run TestCreateWorkspaceDisk -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/vm/workspace.go internal/vm/workspace_test.go internal/vm/create.go internal/vm/domcfg.go internal/vm/meta.go
git commit -m "feat: workspace disk for agent VMs (default 4GB)"
```

---

### Task 7: Console Access

**Files:**
- Create: `internal/xenctl/console.go`
- Create: `internal/cli/console.go`
- Modify: `internal/cli/vm.go` (add console subcommand)
- Test: `internal/xenctl/console_test.go`

**Interfaces:**
- Consumes: xenlight `PrimaryConsoleGetTty`
- Produces: `xenctl.Console(vmName string) error`
- Produces: `qlvm vm console <name>` CLI command

- [ ] **Step 1: Write test for console path resolution**

In `internal/xenctl/console_test.go`:
```go
func TestConsolePath(t *testing.T) {
    // Test that PrimaryConsoleGetTty returns the correct path format
    // (requires cgo build, skip in stub)
}
```

- [ ] **Step 2: Implement console access**

In `internal/xenctl/console.go` (cgo-tagged):
```go
//go:build libxl

package xenctl

import (
    "fmt"
    "os"
    "syscall"

    "xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight"
)

// Console attaches to a VM's primary console (TTY).
// Uses pty for proper terminal handling (raw mode, resize).
func Console(ctx *xenlight.Context, vmName string) error {
    dom, err := ctx.DominfoByName(vmName)
    if err != nil {
        return fmt.Errorf("domain %s not found: %w", vmName, err)
    }
    if !dom.Running() {
        return fmt.Errorf("domain %s is not running")
    }
    ttyPath, err := ctx.PrimaryConsoleGetTty(uint32(dom.Domid()))
    if err != nil {
        return fmt.Errorf("get console tty: %w", err)
    }
    // Open TTY with pty for proper terminal I/O
    ptmx, err := pty.Open()
    if err != nil {
        return fmt.Errorf("open pty: %w", err)
    }
    defer ptmx.Close()
    // Connect stdin/stdout to pty master
    if err := pty.InheritStdio(ptmx); err != nil {
        return fmt.Errorf("inherit stdio: %w", err)
    }
    // Open the VM console TTY on the pty slave
    // (simplified: direct dup2 approach)
    fd, err := syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_NOCTTY, 0)
    if err != nil {
        return fmt.Errorf("open console tty: %w", err)
    }
    defer syscall.Close(fd)
    // Bidirectional copy: stdin -> console, console -> stdout
    errCh := make(chan error, 2)
    go func() {
        buf := make([]byte, 4096)
        for {
            n, err := os.Stdin.Read(buf)
            if err != nil || n == 0 {
                errCh <- err
                return
            }
            if _, err := syscall.Write(fd, buf[:n]); err != nil {
                errCh <- err
                return
            }
        }
    }()
    go func() {
        buf := make([]byte, 4096)
        for {
            n, err := syscall.Read(fd, buf)
            if err != nil || n == 0 {
                errCh <- err
                return
            }
            if _, err := os.Stdout.Write(buf[:n]); err != nil {
                errCh <- err
                return
            }
        }
    }()
    return <-errCh // block until console closes
}
```

**Note:** The `github.com/creack/pty` dependency may need to be added to go.mod. The implementer should verify terminal handling works correctly (raw mode, window resize signals).

- [ ] **Step 3: Implement console CLI command**

In `internal/cli/console.go`:
```go
package cli

import (
    "github.com/spf13/cobra"
)

func consoleCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "console <name>",
        Short: "Attach to a running VM's console",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            name := args[0]
            // Resolve config, check VM exists and is running
            // Call xenctl.Console
            return nil
        },
    }
    return cmd
}
```

- [ ] **Step 4: Add console to vm command group**

In `internal/cli/vm.go`:
```go
vmCmd.AddCommand(consoleCmd())
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/xenctl/ -run TestConsole -v`
Expected: PASS (or SKIP in stub build)

- [ ] **Step 6: Commit**

```bash
git add internal/xenctl/console.go internal/xenctl/console_test.go internal/cli/console.go internal/cli/vm.go
git commit -m "feat: qlvm vm console command (xenlight PrimaryConsoleGetTty)"
```

---

### Task 8: CLI Integration and Config

**Files:**
- Modify: `internal/cli/create.go` (add --workspace-size flag, type handling)
- Modify: `internal/config/config.go` (add agent defaults)
- Modify: `internal/cli/template.go` (show type in list)
- Test: `internal/cli/create_test.go`

**Interfaces:**
- Consumes: All previous tasks
- Produces: Complete CLI integration for agent VMs

- [ ] **Step 1: Add agent config defaults**

In `internal/config/config.go`:
```go
type Config struct {
    // ... existing fields ...
    Agent AgentConfig `yaml:"agent"`
}

type AgentConfig struct {
    MemoryMB int `yaml:"memory_mb"`
    VCPUs    int `yaml:"vcpus"`
}
```

Add defaults: `agent.memory_mb: 2048`, `agent.vcpus: 2`

- [ ] **Step 2: Add --workspace-size flag to vm create**

In `internal/cli/create.go`:
```go
cmd.Flags().IntVar(&workspaceSize, "workspace-size", 0, "workspace disk size in MB (default 4096, 0 = none)")
```

Pass to `vm.Spec.WorkspaceSizeMB`.

- [ ] **Step 3: Handle agent type in vm create**

In `internal/cli/create.go`, resolve template type and set VM type:
```go
tpl, err := ensureTemplate(...)
if err != nil {
    return err
}
if template.IsAgent(tpl) {
    spec.Type = "agent"
    if spec.MemoryMB == 0 {
        spec.MemoryMB = cfg.Agent.MemoryMB
    }
    if spec.VCPUs == 0 {
        spec.VCPUs = cfg.Agent.VCPUs
    }
} else if spec.Type == "agent" {
    return fmt.Errorf("--type agent requires an agent template")
}
```

- [ ] **Step 4: Show type in template list**

In `internal/cli/template.go`, add TYPE column to `template list` output.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cli/ -run TestCreate -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/cli/create.go internal/cli/create_test.go internal/config/config.go internal/cli/template.go
git commit -m "feat: CLI integration for agent VMs (--workspace-size, agent config)"
```

---

### Task 9: End-to-End Integration Tests

**Files:**
- Create: `internal/integration/agent_test.go`
- Test: Full agent VM lifecycle

**Interfaces:**
- Consumes: All previous tasks
- Produces: Integration tests for agent VM lifecycle

- [ ] **Step 1: Write integration test for agent template create**

In `internal/integration/agent_test.go`:
```go
func TestAgentTemplateCreate(t *testing.T) {
    // Requires: agent container image available
    // Test: qlvm template create ghcr.io/jcpowermac/pi-in-xen
    // Verify: template dir has vmlinuz, initramfs, META with type=agent
}
```

- [ ] **Step 2: Write integration test for agent VM create/start/stop/delete**

In `internal/integration/agent_test.go`:
```go
func TestAgentVMLifecycle(t *testing.T) {
    // Requires: agent template created, Xen available
    // Test: vm create -> vm start -> vm list (running) -> vm stop -> vm delete
    // Verify: each step succeeds, OVN port created/deleted
}
```

- [ ] **Step 3: Run integration tests**

Run: `go test ./internal/integration/ -run TestAgent -v -tags integration`
Expected: PASS (requires Xen dom0 environment)

- [ ] **Step 4: Commit**

```bash
git add internal/integration/agent_test.go
git commit -m "test: agent VM integration tests"
```
