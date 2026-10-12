# Xen / Build Pitfalls

- Two binary flavors: repo `bin/qlvm` = portable stub (no `libxl` tag);
  `/usr/local/bin/qlvm` = real cgo build via `sudo make container-build`
  (compiles in `quay.io/fedora/fedora:44`, bundles `libyajl.so.2` with
  `$ORIGIN` rpath, SELinux `:z` on bind mounts, Go cache
  `~/.cache/qlvm-build`).
- **Rebuild the container binary after ANY change before re-running live
  commands — the stub in `bin/` cannot talk to Xen.**
- **vif link-up is `ioctl(SIOCSIFFLAGS)`, never RTM_NEWLINK ifi_flags
  (regression cost a whole session, 2026-10-07).** `cmd/qlvm-vif`'s
  original `netlinkSetUp` put IFF_UP in `ifinfomsg.ifi_flags`; the
  kernel only honors that field when the device is CREATED and SILENTLY
  ACKS (errno 0) for an existing device — every restarted VM came up
  with the vif admin-down (dom0→VM ping/SSH dead, guest fine) until a
  hand `ip link set vifN.0 up`. A raw-netlink success ack proves NOTHING;
  verify the effect (sysfs `flags`/operstate). The fix: read-modify-write
  via SIOCSIFFLAGS (sysfs prints the word in **hex**; the ioctl replaces
  the whole word, so preserve BROADCAST/MULTICAST), polled until the
  IFF_UP flag is actually set (500 ms to the hotplug deadline; fail =
  libxl sees exit 1). Success must be the flag, not operstate — a
  fresh-boot vif has no carrier until the guest's netfront comes up,
  past the deadline. The vif OVS Interface also gets `admin_state=up` on
  create (ovs-vswitchd re-syncs from the kernel on its async bind).
  Also: the `admin_state` column on the OVS `Interface` model must be a
  `*string` (libovsdb marshals a zero-value `string` as `""` and the
  strict ovsdb-server rejects it as a type mismatch).
