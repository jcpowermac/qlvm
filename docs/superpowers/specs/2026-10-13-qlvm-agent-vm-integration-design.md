# qlvm Agent VM Integration Design

**Date:** 2026-10-13
**Topic:** Extend qlvm for container-based agent VMs (pi-in-xen)

## Intent

Make `qlvm template` and `qlvm vm` work identically for agent-based VMs as they do for bootc/ostree VMs. Agent VMs are Xen PVH domains that boot from a kernel + initramfs extracted from a container image (not a full ostree disk). The initramfs contains the entire rootfs (Alpine + nodejs + pi agent), and an optional workspace disk provides persistence for code and pi sessions.

**Why now:** The pi-in-xen container project is complete and produces a working PVH initramfs image at `ghcr.io/jcpowermac/pi-in-xen`. Integrating it into qlvm gives agent VMs the same lifecycle management, networking, and CLI surface as other VMs.

## Goals

- `qlvm template create ghcr.io/jcpowermac/pi-in-xen` works the same way as for bootc images
- `qlvm vm create <name> --template ghcr.io/jcpowermac/pi-in-xen` works the same way as for bootc templates
- `qlvm vm start/stop/restart/list/delete/console` work identically for agent VMs
- Agent VMs get the same OVN networking as other VMs
- Optional persistent workspace disk for code and pi sessions
- No changes to existing ostree VM behavior

## Non-Goals

- Waypipe/GUI support for agent VMs (pi is a TUI agent, console access is sufficient)
- SSH access into agent VMs (no sshd in the minimal initramfs)
- In-guest package management or upgrades
- Multi-vCPU support beyond what the current architecture provides
- Template sharing or registry integration beyond the existing podman pull

## Anti-Goals

- Breaking existing ostree VM workflows
- Adding complexity to the CLI surface (commands stay the same)
- Requiring manual container extraction steps

## Constraints

- **No shelling out** to management CLIs — use native Go control planes (podman REST for container extraction, xenlight for domain management, libovsdb for networking)
- **Container build skew** — same rule as ostree templates: the builder image's tools must be compatible with the dom0 runtime
- **Clean repo** — no vendor names, no real IPs
- **The host is the dom0** — ostree/atomic, no ad-hoc package installs
- **Agent container is read-only transport** — the container is NOT run; it's extracted to get kernel/initramfs

## How It Gets Made

### Template Baking (Agent vs Ostree)

**Detection:** Container LABEL `io.qlvm.type` determines template type:
- `pvh-initramfs` → agent template
- absent → ostree template (existing behavior)

**Agent template baking:**
1. Pull container via podman REST (same as ostree)
2. Create container from image (same as ostree)
3. Extract kernel from `/usr/lib/modules/<version>/vmlinuz` → save as `vmlinuz` in template dir
4. Extract initramfs from `/usr/lib/modules/<version>/initramfs.img` → save as `initramfs` in template dir
5. Read kernel version from `/etc/kernel-version` → save in META
6. No image-builder, no raw disk, no ostree baking
7. Template dir contains: `vmlinuz`, `initramfs`, `META`

**Agent template META:**
```yaml
dir: /var/lib/qvm/templates/<digest>
image: ghcr.io/jcpowermac/pi-in-xen:latest
digest: sha256:...
kernel_ver: 7.2.8-xen1
type: agent  # NEW field
root_dev: ""       # empty for agent
root_flags: ""     # empty for agent
ostree_path: ""    # empty for agent
```

### VM Creation (Agent vs Ostree)

**Template resolution:** Same as existing — `--template` flag resolves by exact name or unique prefix. The template META's `type` field determines the creation path.

**Agent VM creation:**
1. OVN port creation (same as ostree)
2. Per-VM kernel/initramfs copy from template dir to VM dir (same pattern as ostree's `EnsureKernel`)
3. Optional workspace disk: if `--workspace-size` specified, create a formatted disk image
4. Domain config: PVH with kernel/initramfs, different boot params:
   - `console=hvc0`
   - `ip=<ip>/24 gw=<gateway>` (network via kernel cmdline, not networkd)
   - `root=` omitted (initramfs is root)
   - `ostree=` omitted
5. Meta.yaml: same structure, type = "agent"

**Agent VM domain config (DomainSpec):**
```go
&DomainSpec{
    Type: "PVH",
    Name: m.Name,
    UUID: m.UUID,
    Kernel: filepath.Join(vmDir, "vmlinuz"),
    Ramdisk: filepath.Join(vmDir, "initramfs"),
    Extra: []string{
        "console=hvc0",
        "ip=" + m.IP + "/24",
        "gw=" + dom.Gateway,
    },
    MaxVcpus:    m.VCPUs,
    TargetMemkb: m.MemoryMB * 1024,
    Disks:       []DomainDisk{}, // empty unless --workspace-size
    Nics: []DomainNic{{Mac: m.MAC, Script: "vif-ovn", Nictype: "vif"}},
    P9S:         []DomainP9{},   // agent VMs don't use 9p mounts
}
```

**Workspace disk (on by default, 4GB):**
- `--workspace-size <MB>` flag on `vm create` (default: 4096, 0 = no workspace disk)
- Creates a raw disk image, formats with xfs
- Attached as `xvdb` in domain config
- **Prerequisite**: pi-in-xen init script needs a 2-line addition to mount `/dev/xvdb` at `/workspace`:
  ```sh
  if [ -b /dev/xvdb ]; then
      mount /dev/xvdb /workspace 2>/dev/null || true
  fi
  ```
  This change belongs in the pi-in-xen repo. qlvm creates and attaches the disk; the init script mounts it. Until the init script is updated, the disk is attached but not mounted (harmless, /workspace remains ephemeral).

### Network Configuration

**Ostree VMs:** systemd-networkd baked into disk, matches by MAC, static IP/gateway/DNS

**Agent VMs:** Simple static IP via kernel cmdline
- `ip=<ip>` passed in kernel Extra (no prefix)
- Init script parses, adds to eth0, hardcodes gateway as `.1` (matches current qlvm gateway convention)
- Same OVN LSP port creation
- MAC set via vif-ovn script (same as ostree)
- No networkd bake needed
- No DHCP (matches current qlvm static IP model)

**Gateway constraint:** The init script hardcodes gateway as `<subnet>.1`. The current qlvm domain config uses `.1` as gateway, so this works without changes. If a different gateway is needed in the future, the init script must be updated to parse `gw=` from the cmdline.

### VM Types

**Current:** `app` and `disposable` (affect default memory/CPU and SSH config creation)

**Add:** `agent` type
- Defaults: configurable in qlvm config (e.g., `agent.memory_mb: 2048`, `agent.vcpus: 2`)
- No SSH config creation (no sshd)
- No 9p mounts (agent VMs don't use them)

**CLI behavior:**
- `qlvm vm create <name> --template <agent-template>` → type auto-detected as "agent" from template
- `--type agent` flag available for explicit specification
- `--type app/disposable` with an agent template → error (type mismatch)

### `qlvm vm run` Alternative

**Current:** `qlvm vm run` SSHes into the VM and runs waypipe (requires sshd + desktop)

**Agent VMs:** No sshd, no desktop. The equivalent is console access:
- `qlvm vm console <name>` → `xl console <name>` (TTY access to the pi agent)
- `qlvm vm run` for agent VMs → error message suggesting `qlvm vm console`

**Note:** The pi agent runs as PID 1 in the initramfs and occupies the console. Console access gives direct TTY interaction with the agent.

### `qlvm vm sync-kernel`

**Current:** Re-extract kernel from the template's ostree deployment

**Agent VMs:** Re-extract kernel/initramfs from the container image
- Same podman pull + extract flow as template baking
- Overwrites `vmlinuz`/`initramfs` in the VM dir
- VM must be stopped before running

### Configuration

Add to `/etc/qvm/qlvm.yaml`:
```yaml
agent:
  memory_mb: 2048
  vcpus: 2
```

These are defaults for `--type agent` VMs. Can be overridden with `--memory`/`--cpus` flags.

### Init Script Prerequisites (pi-in-xen repo)

Two small changes needed in the pi-in-xen init script for full agent VM support:

1. **Workspace disk mounting** (for persistent /workspace):
   ```sh
   # After network config, before exec pi:
   if [ -b /dev/xvdb ]; then
       mount /dev/xvdb /workspace 2>/dev/null || true
   fi
   ```

2. **Gateway parsing** (optional, for non-.1 gateways):
   Change the hardcoded `.1` gateway to parse `gw=` from cmdline:
   ```sh
   GW=$(echo "$CMDLINE" | grep -o 'gw=[^ ]*' | cut -d= -f2)
   if [ -z "$GW" ]; then
       GW=$(echo "$IP" | awk -F'[./]' '{print $1"."$2"."$3".1"}')
   fi
   ip route add default via "$GW"
   ```

These changes belong in the pi-in-xen repo. qlvm works with the current init script (ephemeral /workspace, .1 gateway) and gains full functionality once the init script is updated.

## What's Left to the Builder

- Console access implementation via `PrimaryConsoleGetTty` (xenlight native, no shelling out)
- Container extraction via `CopyToArchive` (podman REST)
- Workspace disk formatting (xfs, using existing or new utilities)
- Error messages for type mismatches
- Test coverage strategy for agent-specific paths
- `--mount` (9p) for agent VMs: NO (workspace disk is the pattern)
- VM dir structure: `vmlinuz`, `initramfs`, `meta.yaml`, `workspace.img` (if workspace enabled)

## Builder Check Notes

- The pi-in-xen container LABEL `io.qlvm.type` must be present for detection to work
- The initramfs must contain everything needed (no external dependencies)
- Network config via `ip=` kernel param assumes a simple /24 subnet with gateway at .1 (matches current qlvm domain config)
- Workspace disk is on by default (4GB); the pi-in-xen init script needs a 2-line mount addition for it to work
- Console access uses `PrimaryConsoleGetTty` (xenlight native)
- Container extraction uses `CopyToArchive` (podman REST)
- `--workspace-size 0` opts out of the workspace disk (ephemeral /workspace)
