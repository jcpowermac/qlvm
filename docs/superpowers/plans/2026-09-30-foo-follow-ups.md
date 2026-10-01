# qlvm follow-ups (post foo-branch, 2026-09-30)

Handoff for a new session. The foo branch (`feat/foo-reboot-uuid-fixes`, 8 commits) is
merged to main and pushed. Everything below is a known, evidenced follow-up — none blocks
daily use, but #1 and #6 are the user-facing ones.

## Environment quick-start (dom0)

- Repo: /var/home/jcallen/Development/qlvm (branch main). Worktrees under .worktrees/.
- Real binary: `sudo make container-build` → /usr/local/bin/qlvm (cgo libxl build in the
  fedora:44 container; the repo bin/qlvm is a no-libxl stub that cannot talk to Xen).
  ALWAYS rebuild after code changes before running live commands.
- Unit tests: `go test -tags 'exclude_graphdriver_btrfs exclude_graphdriver_zfs containers_image_openpgp' ./...`
  (host go is fine; the cgo file is excluded by build tag).
- Template ref: `jcpowermac-os-bolt-sha256:72e8ffbbcedab9a561cfab9aee3c6412bd8c8d4d5a406019f843dc5b3e46fd3e`
  (dir under /var/lib/qvm/templates/). Guest user `user` (ssh pubkey, NO sudo in-guest);
  `qlvm vm create` does NOT start (spec §6.2.4) — `qlvm vm start <name>` explicitly.
- Guest in-guest forensics without sudo: the one-shot systemd unit pattern (loop-attach p4
  while stopped, unit into the deployment etc overlay, self-removing) — see AGENTS.md
  "Live-smoke pitfalls".
- Loop hygiene before `vm create`: `sudo losetup -a`, detach strays ((deleted) suffix),
  unmount stray /tmp/qlvm-ostree-* mounts. util-linux 2.41.5 rejects `losetup -P`.
- dom0 egress is a DROP allowlist; only the local llama.cpp endpoint is reachable.
  Git pushes work over HTTPS (gh credential helper); SSH is blocked.

## What the foo branch did (context)

1. Per-VM **root** XFS UUID at create (`ostree.UniqueXFS`, root-only: the image's
   /etc/fstab pins the /boot XFS UUID — regenerating it drops the VM into emergency
   mode; live-proven).
2. `qlvm vm restart`: graceful ACPI stop → polls `Running` ~60s → `Destroy` on linger →
   start. Live-verified against a real `---sr-` zombie (rc=0 in 61.2s, fresh kernel
   banner + ssh).
3. `findParts(wantBoot=false)` now releases the /boot probe mount (H4: create leaked a
   loop + rw /boot mount; with shared /boot UUIDs the second create hard-failed).
4. Docs: in-guest `systemctl reboot` zombifies the domain `---sr-` (repro 2/2; no libxl
   event handler on the dom0 → `on_reboot` policy is a no-op; upstream Xen filing
   pending). README + AGENTS.md "In-guest reboot → Xen zombie" section.

Full run history: the SDD ledger is local-only (gitignored) —
`.worktrees/foo-reboot-uuid-fixes/.superpowers/sdd/2026-09-30-foo-reboot-and-uuid-fixes/`
(ledger.md, task reports with raw journal/console evidence). The worktree is removed
after this merge; the evidence worth keeping is summarized here and in the committed plan
`docs/superpowers/plans/2026-09-30-foo-reboot-and-uuid-fixes.md`.

## Follow-ups (ranked)

### 1. A2 — `vm stop` lies about completion (user-facing)
`qlvm vm stop` = fire-and-forget libxl DomainShutdown: returns rc=0 the instant the ACPI
event is sent, while the domain can linger `---s--` for minutes (observed 6+ min). A user
who stops then starts gets "domain already running". Fix: make stopCmd wait for domain
disappearance — `stopForced` (internal/cli/lifecycle.go) is the ready-made primitive
(poll `Running` vs `stopMaxWait`, seams already there). TDD with the existing fakeXen
recorder. Live-verify: stop a healthy VM (fast path) and confirm rc=0 only after
`xl list` is clean.

### 2. A3 — VMs boot with 1 online vCPU despite meta `vcpus = 2`
Root-caused: internal/xenctl/xenctl_cgo.go:~63 sets `d.b_info.max_vcpus` but never the
initial-online vCPU count → Xen comes up with 1 online. Fix: set the initial count
(e.g. `max_vcpus` online at create, or a libxl `cpus_allowed`/initial field — check the
binding's DomainConfig surface). Live-verify: `xl list` VCPUs column shows 2 on a fresh VM.

### 3. A1 — `vm list` mislabels `-b----` domains as `stopped`
Domains sat `-b----` (vCPU `-b-`) with provably live guests (OVN counters showed their
ping replies) for parts of their life; `qlvm vm list` reported `stopped`. State mapping
is in internal/xenctl (stateOf). Also: this dom0 has NO libxl journal logs at all, so
lifecycle events are unobservable — consider whether libxl's log target should be
configured (ties to #5's stderr noise).

### 4. M-smoke-1 — benign `pvcontrol_cb` libxl stderr on zombie restart
`vm restart` on a zombie prints a scary libxl `pvcontrol_cb ... -9` line (both libxl
contexts log XTL_ERROR to stderr — xenctl_cgo.go xtl_createlogger_stdiostream). Cosmetic;
a log-level or stderr-redirect tweak in the xenctl context setup.

### 5. Upstream Xen filing (controller/user action, no code)
Guest `systemctl reboot` → PVH in-place reset hangs in-hypervisor; no dom0 handler,
`xl dmesg` empty, zombie `---sr-` unrecoverable except destroy. Evidence: task-3 report
§13/§15 (local, see above) — console ring, vcpu-list, OVN flow dump, ps/journal proof of
no handler. File against Xen 4.21 (dom0) with that evidence.

### 6. os-bolt image flag (sibling repo, user action)
`bootc-fetch-apply-updates.timer` ships ENABLED in the image layer: ~1–3h after every
boot it runs `bootc upgrade --apply --quiet` (stages a deployment, does not self-reboot).
Consequence: any later reboot of a long-lived qlvm VM is unattended AND post-upgrade →
zombie. Ask the os-bolt maintainers to disable the timer (or gate it) in the image.

### 7. Spec drift: design spec §6.2.4 says `root=UUID=…`, code ships `root=PARTUUID=…`
Pre-existing (not introduced by the foo branch). Update the spec to match the code
(PARTUUID is the boot-safe choice — GPT PARTUUIDs are stable across reflinks).

### 8. findParts error text (report-only)
A concurrent same-template create overlapping UniqueXFS's probe window can hit the
shared /boot UUID and fail with the misleading "no ostree root (and /boot) partition
pair … EINVAL" text (the real XFS duplicate-UUID error is embedded; cleanup is complete).
Improve the error text when next touching findParts.

### 9. Open Minors (carried from the task reviews)
- M1.1: no vm-package test for "xfs_admin fails mid-run → Create removes port + dir"
  (indirectly covered via the fail() helper; add a stub-exit-1 variant in
  create_test.go).
- M2.1: stopForced both-fail drops the Shutdown error (returns Destroy's only); one-line
  `fmt.Errorf("stop: %v; kill: %w", …)` if ever desired.
- M3.1: the linger poll's strict `Before` exits ≈58.5s, not 60s (immaterial).
- M-docs-2: AGENTS.md "now performs exactly that" slightly strong (polish).

## Live state at handoff (2026-09-30 ~23:50)

- Dom0 clean: `xl list` = Domain-0 only; `losetup -a` empty; no /tmp/qlvm-ostree-*
  mounts; no leftover OVN/OVS ports. No VMs exist (foo, repro, smoke, smoke2, ha1/ha2 all
  deleted; foo's disk was destroyed during forensics).
- /usr/local/bin/qlvm = branch build (21cae57 generation, 22:04) — behaviorally identical
  to post-merge main (later commits docs-only). Rebuild after any code work anyway.
- The SDD worktree (.worktrees/foo-reboot-uuid-fixes) is removed with this merge;
  .worktrees/impl (feat/qlvm-impl, old) still exists.
