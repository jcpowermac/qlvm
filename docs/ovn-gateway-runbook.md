# OVN Gateway Connectivity — Runbook

Diagnosing and fixing "VM unreachable / gateway not responding" on the
OVN 26.03 stack (Fedora, `ovs 26.03.x`). Written after a full debug
session where the chassis-claim below had broken on a single-chassis dom0.

Example network space used throughout: LAN `192.168.1.0/24`
(dom0 at `192.168.1.104`, OVN router IP `192.168.1.200`), VM subnet
`10.100.1.0/24` (VM at `10.100.1.10`, gateway `10.100.1.1`).

## Topology recap

```
VM (10.100.1.10) --[vifN.0]--> br-int (OVN) --[ext-localnet]--> br-ex (192.168.1.0/24)
                                        \--[gw-work 10.100.1.1]--> work switch
dom0 (192.168.1.104) routes 10.100.0.0/16 via 192.168.1.200 (router_ip, gw-external LRP)
```

The gateway IP (192.168.1.200) is answered by OVN on the `external`
datapath, but **only while the chassis-redirect port `cr-gw-external` is
claimed by a live chassis**. If the claim is broken, the provider ARP
gets no reply and every route through the router dies. This is the
single most common breakage mode of this stack.

## Symptoms

- `ping 10.100.x.x` from dom0: 100% loss (or `No route to host`).
- `arping -I enp1s0 192.168.1.200`: no response; `ip neigh` shows FAILED.
- VM side is fine: it has its IP, sends ARP for its own gateway (10.100.1.1),
  packets arrive at OVN (`ovs-ofctl dump-flows br-int | grep "in_port=<vif>"`
  shows n_packets growing), but table 40 (output to VM MAC) has n_packets=0.
- `/var/log/ovn/ovn-controller.log` loops, every recompute:
  ```
  Claiming lport cr-gw-external for this chassis.
  Setting lport cr-gw-external up in Southbound
  Releasing lport cr-gw-external from this chassis (sb_readonly=0)
  Setting lport cr-gw-external down in Southbound
  ```
  Claim-then-release = "I am in the HA group but not the active member".

## How the claim works (OVN 26.03 — read this before touching anything)

Key facts verified against `v26.03.x` source (`controller/binding.c`,
`controller/ha-chassis.c`, `northd/northd.c`):

1. **The controller ignores the legacy SB `Port_Binding.gateway_chassis`
   column entirely.** The cr-port claim path is `consider_cr_lport()` →
   `consider_ha_lport()` → `ha_chassis_group_is_active()`. Claim happens
   only via the **HA_Chassis** tables.

2. `lrp-set-gateway-chassis gw-external <X> <prio>` (NB) is a *legacy*
   feature: northd translates it into an `HA_Chassis_Group` (named after
   the LRP, `gw-external`) plus one `HA_Chassis` row per chassis, and links
   it to the `gw-external`/`cr-gw-external` Port_Bindings.

3. **`<X>` must be the Chassis NAME column — the stable OVS chassis_id
   (a UUID), NOT the hostname.** northd resolves it via
   `chassis_lookup_by_name(sbrec_chassis_by_name, ...)` which indexes
   `Chassis.name`. A wrong value makes the lookup return NULL, the
   `HA_Chassis.chassis` ref stays empty, and the claim fails silently.

4. `ha_chassis_group_is_active()` for a group whose members' `chassis`
   refs are all empty returns false → nobody claims → gateway dead.
   If exactly one member has its `chassis` ref set to the local chassis,
   the local chassis is active and claims.

5. **The incremental northd engine does not re-copy GWC → HA
   group when only the `Gateway_Chassis` row changes.** It must see the
   LRP record itself change. Re-running `lrp-set-gateway-chassis` (which
   replaces the GWC row and mutates the LRP) is the reliable trigger.

6. Chassis churn on `ovn-controller` restart is normal and harmless: the
   Chassis row is recreated with the same stable `name` (chassis_id), so
   HA refs that point at the name keep resolving. Don't chase it.

## Diagnosis

```sh
# 1. Is the cr port claimed?
sudo ovn-sbctl list Port_Binding | grep -A26 'logical_port        : cr-gw-external' \
  | grep -E 'ha_chassis_group|chassis |up'
#   up: true  +  chassis: <live uuid>  =>  claimed (healthy)
#   up: false + chassis: []            =>  not claimed (this bug)

# 2. Does the HA group have a live member?
sudo ovn-sbctl list Chassis | grep -E '_uuid|name|hostname'   # live uuid + NAME column
sudo ovn-sbctl list HA_Chassis | grep -E '_uuid|chassis|external_ids'
#   healthy: one row with chassis: [<live uuid>] and
#            chassis-name == the Chassis NAME column value
#   broken:  chassis: [] (empty ref) — the whole disease

# 3. Does the NB side agree?
sudo ovn-nbctl lrp-list gateway | grep -B2 -A4 'name : gw-external'   # gateway_chassis ref
sudo ovn-nbctl list Gateway_Chassis | grep -E 'chassis_name|name'
#   healthy: chassis_name == Chassis NAME column (a UUID), not the hostname

# 4. Controller decision log (the smoking gun)
sudo tail -f /var/log/ovn/ovn-controller.log | grep cr-gw-external
```

## The fix

```sh
# Get the stable chassis name (Name column, NOT hostname, NOT the row uuid)
CH=$(sudo ovn-sbctl list Chassis | grep '^name' | awk '{print $3}' | tr -d '"')

# Re-run with the LRP mutation (forces northd's legacy copy)
sudo ovn-nbctl lrp-set-gateway-chassis gw-external "$CH" 0

sleep 10
# Verify: HA row now has chassis: [<live uuid>], and the controller log
# shows "Claiming lport cr-gw-external" WITHOUT a following "Releasing".
```

If HA rows for stale chassis-names (e.g. a hostname you typed in
earlier) are cluttering the group, they're harmless but can be removed:

```sh
sudo ovn-sbctl list HA_Chassis           # find the stale rows' uuids
sudo ovn-sbctl destroy HA_Chassis <uuid> <uuid>
```

If nothing works, restart northd then the controller (order matters —
northd first so the SB is repopulated before the controller evaluates):

```sh
sudo systemctl restart ovn-northd
sudo systemctl restart ovn-controller
```

## Verification

```sh
sudo arping -I enp1s0 -c2 192.168.1.200      # provider ARP (may show 0 — see note)
ip neigh show 192.168.1.200                  # should be REACHABLE/STALE with lladdr
sudo ping -c4 10.100.1.10                    # the real test
ssh user@10.100.1.10 uptime                  # full path through the router
```

Note: `arping` from the physical NIC often reports 0 responses even when
the gateway works (the reply originates from OVN-generated MACs and the
raw-socket probe can be unreliable across the localnet patch). The ping
is the authoritative test.

## How qlvm avoids this class of bug

- The reconciler (`internal/ovn`) creates the gateway router *before*
  adding `lrp-add gateway gw-<domain>` ports, and re-runs are
  idempotent — a partial first pass does not leave dangling switch ports.
- The chassis name is read from the `Chassis.name` column (the stable
  OVS chassis_id) rather than scraped from a hostname, and
  `SetGatewayChassis` always closes the create/update of the HA chassis,
  the HA group, and the router-port `ha_chassis_group` ref in one
  transaction, so the claim never sees a half-wired group.
- After a manual misconfiguration of the kind above, re-running
  `qlvm install` rewrites the gateway-chassis rows with the correct
  chassis name; the manual fix above is the recovery path when you need
  to inspect the state directly.

## Things that are NOT the problem (ruled out during the debug)

- **Firewall**: firewalld FORWARD policy is ACCEPT; stopping it changes nothing.
- **OVN binding/tunnel for the VM port**: the VM bound fine to the live
  chassis; `vifN.0` UP/LOWER_UP, correct `iface-id`, packets hit table 0.
- **The `vif-ovn` hotplug script**: correct; sets `iface-id`, skips `-emu`.
- **Chassis UUID churn**: cosmetic; the `name` column is stable.
- **`ovn-trace`**: the 26.03 build only accepts a single-predicate microflow
  here; not useful for multi-clause pipeline simulation.

## If the controller wedges

Symptom: `ps -o stat,time -C ovn-controller` shows all threads `S<l` with
~0 CPU time and the log only has `memory_trim ... inactivity` lines.
Fix: `sudo systemctl restart ovn-controller` (then re-check the cr port
claim; the HA refs are name-based so they survive the restart).
