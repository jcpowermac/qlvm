# Fix: foo zombie-after-reboot + duplicate per-VM filesystem UUIDs

## Context

Live incident (2026-09-30, dom0): disposable VM `foo` guest-initiated a clean
`systemd` reboot at 03:54 (`reboot.target` in the guest journal). The second
boot printed the kernel banner on the console and then went **silent for
15+ h**: vCPUs blocked, domain stuck in `---sr-`, no dom0/libxl activity, no
post-reboot journal. It was unrecoverable except by `xl destroy`.

Forensics of the (still intact) `disk.img` also confirmed a second,
independent bug: `vm create` reflink-copies `template.raw` and never
regenerates filesystem UUIDs, so every VM from a template has the **same XFS
UUIDs** (p3 boot, p4 root) as the template and as each other. dom0 dmesg:
`Filesystem has duplicate UUID e15bb83b-...` on `loop1p3`. This breaks any
concurrent dom0-side mount of a VM disk alongside its template (e.g. a
`template create` bake while a VM from that template is being loop-mounted).

The keepalive half (idle ssh over the OVN path blackholing) is already fixed
and pushed (`b0e0c32`).

## What we know vs. don't

- **Known (evidence-backed):** duplicate XFS UUIDs across template + VMs;
  the in-guest reboot left a zombie domain; recovery = destroy + start.
- **Unknown:** why the second boot hung (leading hypothesis: Xen 4.21 PVH
  in-place reboot/reset path — the guest's `reboot` never produced another
  console line; a `device/vif/0 timeout closing device` appeared during the
  shutdown phase). Also unknown: what triggered the in-guest reboot
  (no user shell history; no ostree/bootc update entries in the journal).
  Task 3 reproduces before fixing — no fix ships without a repro.

## Global constraints (from AGENTS.md)

- No shelling out to management CLIs (xl, ovn-nbctl, ovs-vsctl,
  firewall-cmd, nmcli, podman). `xfs_admin` is a filesystem utility with no
  Go binding and is NOT on that list — it is the one sanctioned exec in this
  plan (like the `waypipe` exec helper).
- Unit tests run with the Makefile tags:
  `go test -tags 'exclude_graphdriver_btrfs exclude_graphdriver_zfs containers_image_openpgp' ./...`
- Clean repo: no real IPs, no vendor/product names in committed code/tests.
- The `xenlight` cgo file (`internal/xenctl/xenctl_cgo.go`, build tag
  `libxl`) is written but not compile-verified on this host; live behavior
  is verified on the dom0 via `sudo make container-build` + live smoke.

## Task 1 — Regenerate per-VM XFS UUIDs at create (confirmed bug)

**Files:**
- `internal/ostree/uuid.go` (new)
- `internal/ostree/uuid_test.go` (new)
- `internal/vm/create.go` (one call after the reflink)
- `internal/vm/create_test.go` (assert the new step's position)

**Design.** New `ostree.UniqueXFS`:

```go
// xfsAdminFn is the seam tests replace. xfs_admin is a filesystem
// utility (not a management CLI) with no Go binding.
var xfsAdminFn = func(dev string) error {
    return exec.Command("xfs_admin", "-U", "generate", dev).Run()
}

// UniqueXFS gives a reflinked template copy unique XFS filesystem UUIDs
// (boot + root partitions) so dom0 can loop-mount a VM disk and its
// template simultaneously. Non-XFS fstypes are a no-op.
func UniqueXFS(ctx context.Context, fs FS, disk, fstype string) error {
    if fstype != "xfs" {
        return nil
    }
    loop, err := fs.LoopAttach(disk)
    if err != nil {
        return err
    }
    defer func() { _ = fs.LoopDetach(loop) }()
    root, boot, rootPart, bootPart, err := findParts(ctx, fs, loop, true, true, fstype, "uuid")
    if err != nil {
        return err
    }
    for _, pm := range []partMount{root, boot} { // unmount before xfs_admin
        pm.cleanup()
    }
    for _, part := range []string{rootPart, bootPart} {
        if part == "" {
            continue
        }
        if err := xfsAdminFn("/dev/" + part); err != nil {
            return fmt.Errorf("xfs uuid %s: %w", part, err)
        }
    }
    return nil
}
```

(Adapt `partMount` usage to its actual fields — `target` + `cleanup func()`
— when writing; `findParts` is at `internal/ostree/surgery.go:203`.)

Wiring in `Create` (`internal/vm/create.go`, after the `d.Reflink` call,
before `BakeNetworkd`):

```go
if err := ostree.UniqueXFS(ctx, d.FS, disk, d.FSType); err != nil {
    return fail(fmt.Errorf("unique xfs uuids: %w", err))
}
```

`fail()` already deletes the OVN port + half-built dir, so a mid-step error
leaves no residue. Writing the superblock COWs reflinked extents — the
template is untouched (same mechanism `BakeNetworkd` already relies on).

**Tests** (`uuid_test.go`, TDD): fake `FS` recording `LoopAttach`/
`LoopDetach`/`Partitions` (reuse the fake pattern from `surgery_test.go`);
record `xfsAdminFn` calls.
1. `fstype="xfs"` → `xfsAdminFn` called exactly for `/dev/<bootPart>` and
   `/dev/<rootPart>` (order: boot then root), loop attached+detached.
2. `fstype="ext4"` → no `xfsAdminFn` calls, no loop attach.
3. `xfsAdminFn` error → error returned, loop detached.

`create_test.go`: the Create ordering test asserts `UniqueXFS` runs after
reflink and before the networkd bake (via the existing FS fake — make the
fake's `LoopAttach` fail when called before the "reflink" marker, or record
call order).

**Verify:** `go build ./...` + the tagged `go test ./...` from Global
constraints. Live (after container-build, Task 4): create a scratch VM, then
`xfs_info` the template p4 and the VM p4 (loop-attach + kpartx, RO) — UUIDs
differ; `xfs_info` two VMs from the same template — they differ from each
other.

## Task 2 — `qlvm vm restart <name>` (the recovery path we used by hand)

**Files:** `internal/cli/lifecycle.go`, `internal/cli/lifecycle_test.go`

The foo zombie was recovered by destroy + start. Make that one command.
Graceful first, force on refusal (a zombie never answers ACPI):

```go
func restartCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "restart <name>",
        Short: "Restart a VM (graceful stop, force-kill if it refuses, then start)",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            name := args[0]
            x, err := xenctl.New()
            if err != nil {
                return err
            }
            defer func() { _ = x.Close() }()
            if err := x.Shutdown(name); err != nil {
                if derr := x.Destroy(name); derr != nil {
                    return fmt.Errorf("restart %s: stop %v; kill %w", name, err, derr)
                }
            }
            if err := startVM(cmd.Context(), name); err != nil {
                return fmt.Errorf("restart %s: %w", name, err)
            }
            _, _ = fmt.Fprintf(cmd.OutOrStdout(), "restarted %s\n", name)
            return nil
        },
    }
}
```

Register in `init()` next to `killCmd`. `startVM` (already a shared body)
does the OVS port cleanup + boot.

**Tests** (`lifecycle_test.go`): extract the decision into
`restartPolicy(shutdownErr error) string` is overkill — instead test the
composed behavior with a small seam: `var shutdownFn/destroyFn/startFn` is
also overkill. Keep it honest: the only logic is "stop; on error kill;
start". Factor that into

```go
// stopForced stops a domain gracefully, force-killing if the graceful
// stop fails (zombie domains never answer ACPI).
func stopForced(x xenctl.Xen, name string) error {
    if err := x.Shutdown(name); err == nil {
        return nil
    }
    return x.Destroy(name)
}
```

and unit-test `stopForced` with a `fakeXen`-style recorder (the pattern in
`internal/xenctl/xenctl_test.go`): (a) Shutdown ok → no Destroy; (b)
Shutdown err → Destroy called; (c) both err → wrapped error.

**Verify:** tagged `go test ./internal/cli/`.

## Task 3 — The reboot hang: reproduce, diagnose, then fix (investigation task)

**No fix code until a repro exists.** Live task on the dom0 (after Task 4's
container-build so the current tree is running):

1. **Repro.** `sudo qlvm vm create repro --template <ref>`; wait for
   `ping -c3 <ip>` (ttl=63). In the guest:
   `sudo qlvm vm run ...` no — direct: as the desktop user
   `ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@<ip> 'sudo systemctl reboot'`.
   Watch `sudo xl list` for up to 5 min. Record: does the domain come back
   `r-----`? Does `xl console repro` show a second kernel banner?
2. **While the scratch VM is up, answer the trigger question:** in-guest
   `systemctl list-timers --all | grep -Ei 'ostree|update|bootc'` and
   `ls /etc/systemd/system/*.wants | grep -Ei 'ostree|bootc'`. If the image
   auto-updates/reboots itself, that is an **os-bolt image problem (sibling
   repo)** — report it there; do not patch qlvm for it.
3. **Branch:**
   - **Does NOT reproduce** (domain comes back in <~2 min): foo's hang was
     not the steady-state reboot path. Record the finding + the `---sr-`
     anomaly in AGENTS.md "Live-smoke pitfalls"; ship nothing else. The
     `vm restart` (Task 2) stays as the recovery aid.
   - **Reproduces:** capture immediately: `sudo xl dmesg`, dom0 journal
     around the reboot (`journalctl --since ... | grep -Ei 'libxl|hypervisor'`),
     `sudo xl list -l repro`, `xl console repro` ring, and the vif-ovn
     script's behavior (`/etc/xen/scripts/vif-ovn` + OVSDB activity). Then
     pick the **smallest** fix that makes a guest reboot end in a known
     state (running, or cleanly stopped — never a zombie). Candidates, in
     order of laziness:
     1. **Policy, no hypervisor change:** set an explicit libxl reboot
        policy in the C domain config (`internal/xenctl/xenctl_cgo.go`,
        currently sets none → libxl default). A guest reboot must not
        depend on the Xen in-place PVH reset working; `on_reboot=destroy`
        (domain stops; user runs `qlvm vm start`) or `on_reboot=restart`
        (libxl recreates the domain — the boot path that always works for
        us) both avoid a zombie. Decide which from the repro evidence
        (if libxl receives the reboot event at all — check dom0 journal for
        a libxl `Domain ... reboot` line).
     2. **If libxl never sees the reboot** (reset happens in-hypervisor):
        document "in-guest `reboot` is unsupported; use `qlvm vm restart`"
        in README + AGENTS.md (zero code), and file it upstream (Xen) with
        the captured evidence.
   - **Silent hang (repro hangs with zero console output, like foo):**
     before guessing, add kernel-level capture to the scratch VM only:
     netconsole to the dom0 (`echo ... > /proc/sys/...`/module params,
     temporary, not baked) — do NOT bake netconsole into templates in this
     plan.

**Deliverable:** a commit with the chosen fix + live re-verification
(`reboot` → known end state within 2 min), or an AGENTS.md/README note
documenting the limitation with the captured evidence. Either way, one
final `reboot` live check on a scratch VM and then `qlvm vm delete repro`.

## Task 4 — Rebuild + live smoke (gate for Tasks 1–3)

1. `sudo make container-build` (any code change requires the cgo rebuild
   before live commands; `bin/qlvm` stub cannot talk to Xen).
2. Task 1 live check (UUIDs differ: template vs VM vs VM2).
3. Task 2 live check: `sudo qlvm vm restart <scratch>` → stopped then
   running; ping ttl=63; `ovs-vsctl show` vif port present.
4. Task 3 repro session (same scratch VM).
5. Cleanup: delete scratch VMs, `sudo losetup -a` (empty), no
   `/tmp/qlvm-ostree-*` mounts, no orphan image-builder containers.

## Review focus (5 ways this could fail)

1. **Zombie VM + user runs `vm restart`:** graceful stop times out waiting
   on ACPI → must fall through to Destroy, and the subsequent start must
   succeed (this is exactly the foo recovery that worked by hand).
2. **`vm create` on a dom0 without xfsprogs:** `xfs_admin` missing → Create
   must fail loudly through `fail()` (port deleted, dir removed), not
   produce a VM with duplicate UUIDs silently.
3. **Non-xfs template (ext4 via `--bootc-default-fs`):** `UniqueXFS` is a
   no-op — no loop attach, no error; create proceeds as before.
4. **`UniqueXFS` dies mid-run** (xfs_admin fails on the boot partition):
   the disk keeps the template's boot-partition UUID and a new root UUID —
   the create failure path removes the dir, so no half-uniquified disk
   survives; a retry reflinks fresh.
5. **Task 3's "policy" fix is unreachable:** if the PVH reboot never
   reaches libxl (in-hypervisor reset), setting `on_reboot` changes
   nothing — the repro's dom0 journal must show libxl seeing the reboot
   event before that fix is chosen; otherwise the limitation is documented
   instead (branch 3 of Task 3).
