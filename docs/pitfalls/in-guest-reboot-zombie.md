# In-guest Reboot → Xen Zombie `---sr-` (reproduced 2026-09-30)

- **In-guest `reboot` is unsupported — use `qlvm vm restart`.** A plain
  in-guest `systemctl reboot` (no upgrade involved) hangs deterministically:
  the guest console ring ends with a 5 s
  `xenbus_frontend_dev_shutdown: device/vif/0 timeout closing device`, then
  `reboot: machine restart`, then NOTHING; the domain sits in `---sr-` in
  `xl list` (vCPU0 `---` halted, CPU time frozen) — unreachable, ACPI
  unresponsive, gone only via `xl destroy`. Reproduced twice back-to-back
  (Xen 4.21, guest kernel 7.2.7-200.fc44, template 72e8ffbb…): reboot fired
  ~20:54:53 → `---sr-` by 20:55:23, and ~21:03:22 → `---sr-` by 21:03:30,
  both zombied 5+ min (22 samples in run 2). This is the same terminal
  state as the "foo" incident; whether the in-guest upgrade changes
  anything on this path is untested.
- **Why no policy can save it: the dom0 has no libxl event handler.**
  qlvm creates domains via libxl cgo in a process that exits after create;
  `ps aux` shows no libxl/xl daemon for the live domain and there is no
  `/etc/libxl.conf` (libxl logging off — `journalctl -t libxl` is empty,
  zero libxl lines around create/stop/reboot/destroy). The guest's ACPI/HVM
  reboot request appears to go unhandled in the hypervisor — no dom0 process
  exists to consume it, and the hypervisor's internal state is not directly
  observable — so even the default `on_reboot=destroy` never runs. Setting
  `on_reboot=` in `internal/xenctl/xenctl_cgo.go` is a NO-OP until a dom0
  process handles libxl events for the domain's lifetime. `xl dmesg`
  (hypervisor ring) was
  empty (0 bytes). Netconsole was judged uninformative: the hang is after
  the guest kernel stops executing, so no in-guest channel reports past
  `machine restart` (the hvc0 ring already captures the kernel's last word).
- Recovery: `sudo xl destroy <name>` + `qlvm vm start <name>` — and
  `qlvm vm restart` automates this (graceful ACPI stop, linger poll ~60 s,
  force-kill on linger, start): live-verified against a real `---sr-`
  zombie on 2026-09-30 (rc=0 in ~61 s; Task 4b smoke). Loop-forensics on the
  zombie's disk are safe once the domain is gone from `xl list`.
- **The image's auto-upgrade timer makes the next reboot unattended.**
  `bootc-fetch-apply-updates.timer` ships ENABLED in the image layer
  (`/usr/lib/systemd/system/default.target.wants/`); ~1–3 h after every boot
  it runs `bootc upgrade --apply --quiet` (stages a deployment; does not
  self-reboot), so any later reboot of a long-lived VM is unattended and
  post-upgrade — and, per the note above, will zombie the domain. Image
  problem for the os-bolt sibling repo; not patchable in qlvm.
- Related state anomalies observed on the same VMs (not chased): domains
  show `-b----` (vCPU `-b-`) for parts of the life while the guest is
  provably live (`qlvm vm list` mislabels such a VM `stopped`); `qlvm vm
  stop` returned rc=0 while the domain lingered `---s--` 6+ min after a
  clean in-guest poweroff; the scratch VM ran 1 vCPU despite meta
  `vcpus = 2` (libxl config `max_vcpus:2, avail_vcpus:[0]`).
- Full evidence (console rings, timestamps, OVN/OVS, journal): the Task 3
  report in `.superpowers/sdd/2026-09-30-foo-reboot-and-uuid-fixes/`
  (gitignored; local only).
