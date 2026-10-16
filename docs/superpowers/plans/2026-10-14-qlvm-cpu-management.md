# Plan: qlvm CPU Management

**Spec:** `docs/superpowers/specs/2026-10-14-qlvm-cpu-management-design.md`

## Context

Dom0 on a 14-core (6P+8E) / 65GB machine is unconstrained, starving guest VMs of resources. This plan adds:
1. Dom0 resource limits (memory + vCPUs) applied via boot params during `qlvm install`.
2. Guest CPU core tracking and placement (E-first auto, manual override).

## Dependencies

- **Task 1** (Dom0 config) and **Task 2** (coremap package) are independent and can start together.
- **Task 3** (CPU pinning in Xen) depends on Task 2 (needs coremap types).
- **Task 4** (VM creation integration) depends on Tasks 2 and 3.
- **Task 5** (install integration) depends on Task 1.
- **Task 6** (os repo change) depends on Task 1 (needs the params file format).
- **Task 7** (cores command) depends on Task 2.

## Tasks

### Task 1: Dom0 config in qlvm.yaml

**Goal:** Add `dom0` section to the config file structure.

**Files:**
- `internal/config/config.go`
- `internal/cli/install.go` (config validation)

**Changes:**
1. Add `Dom0` struct to `config.go`:
   ```go
   type Dom0 struct {
       MemoryMB int  `yaml:"memory_mb"` // default 32768
       VCPUs    int  `yaml:"vcpus"`     // default 2
       Pin      bool `yaml:"pin"`       // default true
   }
   ```
2. Add `Dom0 Dom0 \`yaml:"dom0"\`` to `Config` struct.
3. Add validation in `Validate()`: `vcpus > 0`, `memory_mb >= MinMemoryMB`, `pin` optional (defaults true).
4. Add defaults: if `Dom0` section is absent, use MemoryMB=32768, VCPUs=2, Pin=true.

**Testing:** Unit tests in `internal/config/config_test.go` (new file) covering:
- Valid config with dom0 section
- Config without dom0 section (defaults applied)
- Invalid vcpus (0 or negative)
- Invalid memory_mb (below minimum)

**Verification:** `go test ./internal/config/...`

---

### Task 2: Coremap package (CPU topology + tracking)

**Goal:** New package for CPU topology detection and core assignment tracking.

**Files:**
- `internal/coremap/coremap.go` (new)
- `internal/coremap/coremap_test.go` (new)

**Changes:**
1. Define types:
   ```go
   type Topology struct {
       TotalCores int   `yaml:"total_cores"`
       PCores     []int `yaml:"p_cores"`
       ECores     []int `yaml:"e_cores"`
   }
   
   type Assignment struct {
       VM    string `yaml:"vm"`
       Cores []int  `yaml:"cores"`
   }
   
   type Coremap struct {
       Topology    Topology     `yaml:"topology"`
       Dom0Cores   []int        `yaml:"dom0_cores"`
       Assignments []Assignment `yaml:"assignments"`
   }
   ```
2. `DetectTopology()` — reads `/proc/cpuinfo` for model, looks up P/E split in a model table. Fallback: assume first half of cores are P. Return `Topology`.
   - Model table: map of CPU model strings to `{p: int, e: int}`. Start with `"Ultra 5 245K": {p: 6, e: 8}`.
   - Read total cores from `/sys/devices/system/cpu/` (count cpu* dirs).
   - P cores = first N in Linux CPU numbering, E cores = rest.
3. `Load(path string)` — read `/var/lib/qlvm/coremap.yaml`.
4. `Save(path string)` — write coremap.yaml atomically (write to temp, rename).
5. `Rebuild(topology Topology, dom0VCPUs int, vmMetas map[string]*vm.Meta)` — reconstruct coremap from existing VM meta files + dom0 config. Handles stale entries.
6. `Allocate(cm *Coremap, vcpus int) ([]int, error)` — auto-placement: find contiguous block of E cores first, then P cores (excluding dom0 cores). Return the assigned cores.
7. `Free(cm *Coremap, vmName string)` — remove a VM's assignment.
8. `FreeCores(cm *Coremap) map[int]bool` — return set of unassigned, non-dom0 cores.

**Testing:** Unit tests covering:
- `DetectTopology()` with mocked `/proc/cpuinfo`
- `Allocate()` with various core availability scenarios
- `Free()` removes correct assignment
- `Rebuild()` handles stale entries
- Contiguous block finding

**Verification:** `go test ./internal/coremap/...`

---

### Task 3: CPU pinning in Xen domain creation

**Goal:** Add CPU affinity to domain creation via libxl.

**Files:**
- `internal/vm/domcfg.go`
- `internal/xenctl/xenctl_cgo.go`
- `internal/vm/domcfg_test.go` (existing, extend)

**Changes:**
1. Add `CPUs []int` field to `DomainSpec` in `domcfg.go`.
2. In `DomainConfig()` and `agentDomainConfig()`, set `CPUs` from `Meta.Cores` if present.
3. In `xenctl_cgo.go`:
   - Add `cpus` parameter to `qlvm_create_domain()` C function (array + count).
   - In the C code, if cpus are provided, set `d.c_info.cpus` (libxl CPU mask) or use `libxl_domain_setmaxvcpu` + vcpu pinning after creation.
   - **Preferred approach:** Use `libxl_domain_cputopology` or set the domain's CPU affinity via `libxl_set_vcpuaffinity` after domain creation but before unpause.
   - Actually, the cleanest approach: set `d.c_info.cpus` as a bitset before `libxl_domain_create_new`. The `libxl_domain_config` struct has a `cpus` field (flex_array of uint8) that sets the domain's allowed CPUs.
4. Map Go `[]int` to C flex_array bitset.

**Testing:** Extend existing domcfg tests to verify CPUs field is set correctly.

**Verification:** `go test ./internal/vm/... ./internal/xenctl/...` (cgo file compiles only with `-tags libxl`)

---

### Task 4: VM creation integration

**Goal:** Wire coremap into VM create/start flow.

**Files:**
- `internal/vm/meta.go`
- `internal/vm/create.go`
- `internal/cli/create.go`

**Changes:**
1. Add `Cores []int \`yaml:"cores"\`` to `Meta` struct in `meta.go`.
2. Add `Cores []int` to `Spec` in `create.go`.
3. In `Create()`:
   - If `spec.Cores` is non-empty, use those cores (validate they're available).
   - Otherwise, call `coremap.Allocate()` to auto-assign.
   - Save assigned cores to `Meta.Cores`.
4. In `Delete()` (in `xenctl/xenctl.go`), call `coremap.Free()` after successful deletion.
5. Add `--cores` flag to `vm create` command in `cli/create.go`:
   - Format: `--cores 6-7` or `--cores 6,7,8`.
   - Parse into `[]int`.

**Testing:** Extend existing create tests. New tests for:
- Auto-placement assigns E cores first
- Manual cores override auto-placement
- Invalid cores (already assigned, out of range) return error
- Delete frees cores

**Verification:** `go test ./internal/vm/... ./internal/cli/...`

---

### Task 5: Install integration (dom0 params)

**Goal:** `qlvm install` writes Xen boot params file.

**Files:**
- `internal/cli/install.go`
- `internal/setup/setup.go`

**Changes:**
1. Add `Dom0Params func() error` to `setup.Plan`.
2. In `install.go`'s `newInstallPlan()`, implement `Dom0Params`:
   - Read `cfg.Dom0`.
   - Format: `dom0_mem=<X>G,max:<X>G dom0_max_vcpus=<N>` + ` dom0_vcpus_pin` if `cfg.Dom0.Pin`.
   - Write to `/etc/qlvm/xen-boot-params.conf` (0644).
   - Create `/etc/qlvm/` dir if needed.
3. Add the step to `setup.Run()` after the "dom0" step (before storage).

**Testing:** Unit test in `internal/setup/setup_test.go` (extend) verifying:
- Params file is written with correct format
- Default values when dom0 section absent
- Pin=false omits dom0_vcpus_pin

**Verification:** `go test ./internal/setup/...`

---

### Task 6: os repo change (xen-dom0-refresh)

**Goal:** Modify `xen-dom0-refresh` to read and apply the params file.

**Files:**
- `../os/files/system/usr/libexec/xen-dom0-refresh`

**Changes:**
1. In section 4 (user.cfg generation), read `/etc/qlvm/xen-boot-params.conf` if it exists.
2. Append its contents to the `multiboot2` line:
   ```bash
   xen_params=""
   if [ -f /etc/qlvm/xen-boot-params.conf ]; then
       xen_params=$(cat /etc/qlvm/xen-boot-params.conf)
   fi
   # ...
   multiboot2 /xen-${ver}.gz ${xen_params}
   ```
3. This is a minimal change: if the file doesn't exist, behavior is unchanged.

**Verification:** This file is a bash script in the os repo, not Go. Verify by reading the modified script and checking the logic. No Go tests apply. The change will be tested on the live system after the ostree image is rebuilt.

**Note:** This task requires modifying the `../os` repo (separate git repo). The change is small and self-contained.

---

### Task 7: `qlvm cores` command

**Goal:** New CLI command to display core assignments.

**Files:**
- `internal/cli/cores.go` (new)

**Changes:**
1. New `coresCmd()` cobra command.
2. Load coremap.yaml.
3. Print table:
   ```
   Core  Type  Assigned to
   0     P     dom0
   1     P     dom0
   2     P     (free)
   3     P     work
   ...
   ```
4. If coremap doesn't exist, detect topology and show all cores as free (except dom0's).

**Testing:** Unit test for table formatting.

**Verification:** `go test ./internal/cli/...`

## Execution Notes

- **Build after every change:** `sudo make container-build` before testing live commands (per AGENTS.md).
- **Live testing order:**
  1. Task 5 + Task 6 together (dom0 params) — requires os repo rebuild + reboot.
  2. Tasks 2, 3, 4 (guest CPU management) — test with `qlvm vm create` and `xl vcpu-list`.
  3. Task 7 (cores command) — verify output matches `xl vcpu-list`.
- **Dom0 params require a reboot** to take effect. After `qlvm install` writes the params file, the next boot (triggered by `xen-dom0-refresh` regenerating user.cfg) will apply them.
- **The params file is read by xen-dom0-refresh on every boot**, so changes to the file take effect after reboot without manual grub editing.

## Risk Mitigation

- **Coremap corruption:** `Rebuild()` reconstructs from VM meta files + dom0 config. Call it on load if the file is missing or malformed.
- **Stale assignments:** `Rebuild()` removes VMs that don't exist on disk.
- **Out-of-band VM deletion:** Handled by `Rebuild()`.
- **Xen CPU pinning failure:** If libxl fails to set CPU affinity, the domain creation fails (no partial pinning). Clear error message.
- **Dom0 starvation:** Default is 2 vCPUs; can be increased in config. Monitor with `qlvm cores` and `uptime`.
