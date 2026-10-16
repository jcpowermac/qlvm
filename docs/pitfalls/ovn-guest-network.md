# OVN / Guest Network Pitfalls

- **How it works (live-verified 2026-09-28).** Guest side: no DHCP, no
  NetworkManager — systemd-networkd applies the baked `10-bolt.network`
  (`[Match] MACAddress=` + static address/gateway/DNS) to the netfront
  interface (`enX0`). Dom0 side: each VM gets an OVN LSP named after the VM
  on the configured switch (`work`), plus an OVS port `vif<domid>.0`
  (type=system, `external-ids:iface-id=<vm-name>` — that is how
  ovn-controller ties the physical port to the LSP). Dom0 is NOT on the VM
  subnet: traffic dom0→VM routes over the uplink subnet via the OVN
  gateway router (`10.100.0.0/16 via <gw> dev br-ex`, static route on dom0
  from the legacy setup), so ping replies carry ttl=63. Legacy bash
  reference for the dom0 OVN/OVS/static plumbing: the legacy bash VM
  project (sibling tree, `qlvm-install`, `qlvm-harden-dom0`,
  `qlvm-setup-egress`, `vif-ovn` scripts).
- **Verify in 3 commands**: `ping -c3 <vm-ip>` (ttl=63 = via OVN router);
  `sudo ovn-sbctl find Port_Binding` → the VM's port has `up: true`, a
  `chassis`, and `mac`/`port_security` = the assigned MAC (the OVN "MAC
  binding"); `sudo ovs-vsctl show` → `vif<domid>.0` under br-int.
- The dom0 vif netdev's kernel-default MAC (`fe:ff:ff:ff:ff:ff`) does NOT
  need setting — OVS/ovn match on the guest frame MAC, verified working
  without touching it. No legacy-parity step missing there.
- **In-guest debugging without console input**: console typing is flaky;
  instead loop-attach the VM disk p4 (root) while the VM is STOPPED, drop a
  one-shot `dump.service` into the deployment etc overlay
  (`ostree/deploy/default/deploy/<commit>.0/etc/systemd/system/` + a
  `multi-user.target.wants/` symlink) that writes what you need to `/var`,
  boot, kill, remount, read. The journal is persistent — read
  `/var/log/journal` the same way.
- **A duplicate MAC across LSPs silently breaks routing.** If any stale LSP
  shares a MAC with a live VM's LSP (leftover from a previous test: the
  `bolt-test` LSP with the smoke VM's MAC/IP), ovn-controller's LSP-egress
  flow (`dl_dst=<mac>` → port index in table 40) can resolve to the stale
  port's index while the logical-port-egress flow (table 46) expects the
  live port's — packets hit the `priority=0 drop` in table 46. Diagnosis:
  `ovs-ofctl dump-flows br-int` tables 40 vs 46; fix: delete the stale LSP
  (`ovn-nbctl lsp-del <uuid>`). The whole "guest network OPEN" phase of the
  live smoke was this, not anything in the guest.
- OVN ctl drift (Fedora 44 OVN): `lsp-get`/`lsp-list`-style subcommands and
  the SB `Logical_Switch_Port` table are **gone**. Use
  `--columns=... find <Table> col=val`; the SB port table is `Port_Binding`
  with column `logical_port` (no `name`).
- Guest (bootc) layout for forensics: live `/etc` =
  `ostree/deploy/default/deploy/<commit>.0/etc`; the writable `/var` is
  `ostree/deploy/default/var` (sysroot-level — NOT partition root and NOT
  the deployment's `var/`); journald is persistent there, so stop the VM,
  loop-attach p4 (root), and read `/var/log/journal` + whatever a one-shot
  dump unit wrote. Guest netfront interface is `enX0` (renamed from eth0);
  networkd matches by MAC so the name is irrelevant.
- **Loop hygiene before `qlvm vm create`**: a loop device left attached to a
  *deleted* disk.img keeps stale partition nodes and can make the template
  attach fail ("has no ostree root (and /boot) partition pair ... EINVAL").
  `sudo losetup -a`, detach strays, then create.
- **Guest xfs log vs dom0 xfs (kernel skew, cost a VM 2026-10-02):** the
  guest kernel (7.2.x) writes an xfs log the older dom0 xfs driver cannot
  replay — after an unclean guest stop, any dom0 mount of the root
  partition fails with `log mount/recovery failed: error -22` (both ro and
  rw), and the *guest itself* then boots into emergency mode (its own log
  is mid-replay). `xfs_repair` without flags refuses ("valuable metadata
  changes in a log"). Fix: domain gone from `xl list`, then
  `sudo xfs_repair -L /dev/loopXp4` (resets the log), then mount rw and
  re-bake anything the lost log tail held (drop-ins, tokens). A clean
  ACPI stop (`qlvm vm stop`) replays fine — only unclean stops hit this.
  Loop-mount forensics reads are otherwise reliable; "empty reads" from a
  stale mount path were a separate (lost mount) issue, not xfs.
- **OVN 26.03 silently drops the gateway's SNAT unless the router has
  `options:chassis` (found + fixed live 2026-10-08; the guest's LAN
  egress/DNS was dead on the 26.03.2 package from Oct 5).** In 26.03
  northd decides `is_gw_router` from `Logical_Router.options:chassis`
  being non-empty (`northd.c`: `od->is_gw_router =
  !!smap_get(&od->nbr->options, "chassis")`) — the older localnet-peer
  heuristic is gone. Without the option the NAT builder early-returns and
  compiles zero SNAT flows; the failure is SILENT (no log line) and the
  symptom is exactly "guest → LAN dead, dom0 → guest fine": every guest
  egress packet leaves the LAN with its private source address and no
  reply can route back. Diagnosis: `ovn-nbctl get logical_router gateway
  options` empty while `ovn-sbctl dump-flows gateway` shows no
  `lr_out_snat`/`ct_snat` row. The router's `router_ip` SNAT
  (`10.100.0.0/16 → 172.31.12.200`) is the designed guest-egress path and
  works again once northd compiles it. **Fixed forward (not pinned):**
  the option carries the live chassis name — exactly what ovn-kubernetes
  sets (`pkg/ovn/gateway.go`) on the same OVN build — and the qlvm ovn
  reconciler now does it: `NewLive` opens the SB socket
  (`unix:/var/run/ovn/ovnsb_db.sock`, `QVM_OVNSB_ENDPOINT` override) and
  reads the chassis name from the SB `Chassis` table (bounded wait for
  ovn-controller to register), fresh installs carry `Options` on the
  router row, and `Apply`'s attach pass mutates `options:chassis` in place
  on the existing row when it drifted — so a re-run of `qlvm install`
  self-heals a router that predates the option (or an OVN upgrade that
  changes the rule again). Gotchas learned the hard way: libovsdb
  `Where(m).Mutate(m, …)` derives the match from the model's non-zero
  fields, so the new value may live only on the mutation model, not the
  condition model; and `count=1`/no-error replies prove NOTHING — the
  client cache lags the monitor, so verify the effect with `ovn-nbctl get`
  (server-side) and the SB flows. If egress regresses: check the option
  first, then the SB `lr_out_snat` row, then `ovn-trace gateway 'ip, ip4
  == 10.100.x.x/1.1, outport="gw-external"'` for the `ct_snat` action
  (26.03 trace syntax: positional datapath, `&&` separators in the
  microflow). NB: table 79's `MAC_CACHE_USE` rows on localnet routers are
  harmless usage counters (resubmit semantics), not a drop — an earlier
  read of them as the cause was wrong. The dom0 also still joins the work LSW directly (STILL MANUAL, not in the
  qlvm reconcilers: `qvm-dom0` OVS port on br-int + LSP `dom0`
  `10.100.1.2` + per-subnet /24 kernel routes) so that guest→dom0 replies
  (e.g. the waypipe tcp/vsock data port dials) land on an address the dom0
  owns, since the router SNATs guest egress to `router_ip`; that part is
  unchanged and separate from the SNAT fix.
- **Guest account is `user`, not the dom0's username.** The template
  bakes the dom0's pubkey into `/var/home/user/.ssh/authorized_keys`
  (see the SSH section below); ssh as `user@<vm-ip>`. SSHing as the
  dom0's user "works" to connect but lands nowhere — there is no such
  account in the guest.
