# Waypipe Control Channel (`vm run --connect ssh|tcp|vsock`)

- **`--connect` picks the channel** (default `ssh`): ssh = `waypipe ssh`
  over the VM's sshd (works on every template, no relay needed); tcp =
  token control frame to the guest's qlvm-ctl relay + ephemeral TCP data
  port; vsock = waypipe `--vsock` (guest relay dials the dom0's vsock
  port). **vsock kernel-module path is dead; Xen Argo is the real
  alternative (investigated 2026-10-03).** There is no
  `CONFIG_XEN_VSOCKETS` in any F44 kernel: no XEN_VSOCKETS Kconfig symbol
  or driver exists anywhere in the 7.2.8 srpm tree or mainline v7.2, and
  F44 ships no 6.x kernels (oldest: 7.1.10) — the appended config line is
  silently dropped by olddefconfig. The only existing vsock-over-Xen
  kernel stacks are `xen-troops/meta-xt-vhost` (needs qemudm/QEMU) and
  **Xen Argo** (v4v lineage: Citrix 2010 → XenServer/OpenXT 2019 →
  upstreamed to Xen 2025/26, **Status: Tech Preview**, first in the
  4.22-dev tree). Argo needs NO QEMU and NO virtio: 4 hypercalls
  (register_ring/unregister_ring/sendv/notify), hypervisor-copied 16 MB
  rings; guest+dom0 run the OpenXT `linux-xen-argo` modules
  (`argo-linux` driver + `vsock-argo` AF_VSOCK transport, WIP/experimental,
  CID ≈ domid, last touched 2025-04), and waypipe --vsock works
  unmodified. BLOCKER: the dom0's Xen 4.21.2 has no Argo (no ARGO line in
  /boot/xen-4.21.2.config; git check of stable-4.21/master) — using Argo
  means building Xen master (CONFIG_ARGO=y) as a custom dom0 hypervisor
  (reboot, all VMs down, Tech Preview) + porting both OpenXT modules to
  the 7.2 kernels (guest .ko baked into templates). Until that lands, use
  the ssh (default) or tcp channels.
- **The relay is an ncat listener, not systemd socket activation.** A
  socket-activated service receives the connection on **fd 3** (via
  LISTEN_FDS), NOT stdin — the relay's `read -r tok` from stdin got
  /dev/null EOF and the frame was never consumed (CLOSE-WAIT pileup on
  4711, dom0 hung forever). `ncat -lk 4711 --sh-exec /etc/qlvm/qlvm-ctl`
  hands each connection to the script as fd 0. ncat `--sh-exec` takes
  exactly ONE argument, so it points at the executable script path, not a
  split command line. A failed relay now kills only its child; no
  start-limit wedge class exists.
- **Control frame is 5 lines**: `<token>\n<mode>\n<port>\n<dom0-ip>\n<exec>\n`
  (mode tcp|vsock). Dom0 and relay must match — a 4-line relay reads the
  mode line as the port and mis-runs; re-bake or loop-patch after changes.
- **waypipe CLI**: global options go BEFORE the mode word (`waypipe
  --xwls ssh ...`); `waypipe ssh --xwls` fails (the parser treats the
  first bare word after `ssh` as the destination).
- **tcp data channel gotchas (live-verified 2026-10-03, cost the whole
  session; the guest's waypipe server dials the bridge, which dials the
  dom0's data port — every link in that chain has a failure mode):**
  - **The bridge dialer's ncat timeout needs an explicit `s` unit.**
    `ncat -w 120 host port` *exits immediately* (rc=2) printing
    "the default unit for -w is seconds … QUITTING." — ncat treats a bare
    1–5000 value as ambiguous ms-vs-s and refuses to run. The relay's
    `ncat -lkU <sock> --sh-exec "ncat -w 120 $dom0 $port"` therefore
    never dialed, the unix pipe got EPIPE, and the guest waypipe server
    panicked (`server/mod.rs` Backend BrokenPipe) and tore down the app.
    Use `-w 120s`. Symptom of the bare form: the guest waypipe process
    exists for a moment then vanishes, no TCP ever reaches the dom0 data
    port, `ss` on the dom0 shows only the relay's 4711 listener.
  - **The frame's dom0-ip is the dom0's uplink IP, not the OVN router.**
    The bridge dials `<dom0-ip>:<port>`; the dom0 data listener is bound
    on the dom0's br-ex address (e.g. 172.31.12.111), NOT
    `router_ip` (172.31.12.200, the OVN L3 gateway, which does not
    forward to dom0 host ports). `--connect tcp` therefore requires the
    new `network.dom0_ip` config field (the old code passed
    `router_ip` and hung). Set it to the same IP `ip -4 addr show br-ex`
    shows.
  - **The dom0 firewall must admit guest→dom0 data-port dials, and the
    rich rule matches the router IP, not the VM subnet.** The uplink
    `public` zone on br-ex rejects everything except ssh/mdns/dhcpv6, so
    the guest's dial of the dom0's ephemeral data port (32768–60999) was
    EHOSTUNREACH. Worse, the OVN `gateway` router SNATs ALL VM traffic
    to `router_ip` (172.31.12.200), so a rule scoping
    `source address="10.100.0.0/16"` never matched — the dom0 saw
    source 172.31.12.200. `fw.Ensure` now also manages a
    `dom0-data-in` policy (target CONTINUE, ingress ANY, egress HOST) with
    rich rules for BOTH the supernet and router_ip; port range is the
    default `ip_local_port_range`. Symptom if missing: `ping` to the
    dom0 from the guest works, ssh works, but `ncat <dom0> <highport>`
    returns "No route to host" — while the dom0 itself can connect to
    its own IP:port (that path skips the OVN SNAT + zone).
  - **`bootc-fetch-apply-updates.timer` is masked at bake.** The
    fedora-bootc base image ships it enabled; ~1–3 h after boot it stages
    an in-guest upgrade and emits a "restart required" that is a trap for
    qlvm VMs (the boot path is qlvm-owned, so the upgrade can't take
    effect, and the suggested in-guest reboot deterministically zombies
    the domain in `---sr-`). `bakeHeadlessUnits` masks it; kernel
    refresh belongs to `qlvm vm sync-kernel`. A pre-existing VM keeps
    the timer unless its disk is loop-patched (mask symlink in the
    deployment's `/etc/systemd/system/`).
