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
- **cgo `libxl_domain_create_new` from the agent: four crashes, four fixes**
  (2026-10-19; all verified live — a pinned VM now starts with
  `Affinity (Hard)` on its cores). The config is assembled in C
  (`qlvm_create_domain` in `internal/xenctl/xenctl_cgo.go`) with the
  public init functions, not the Go binding's toC (it leaves unions and
  optional pointers as raw malloc garbage).
  1. **cpumap**: `libxl_domain_config_init` leaves `b_info.cpumap`
     (map=NULL, size=0). Writing bits on it SIGSEGVs. Use
     `libxl_cpu_bitmap_alloc(ctx, &b_info.cpumap, 0)` (machine-sized) +
     `libxl_bitmap_set`. `libxl_cpumask_set` is internal (not in the
     public headers).
  2. **vcpu_hard_affinity double-free**: with a cpumap set,
     `libxl__domain_build_info_setdefault` converts it into per-vCPU
     `vcpu_hard_affinity` bitmaps allocated from the **AO GC arena**; the
     AO dies at the end of create_new, and
     `libxl_domain_config_dispose` then `free()`s the GC-arena pointers
     ("double free or corruption"). The conversion is skipped when
     `num_vcpu_hard_affinity != 0`, so pre-populate the array yourself:
     plain `calloc` the array, `libxl_cpu_bitmap_alloc` each bitmap,
     `libxl_bitmap_copy` the cpumap into each. (A bitmap allocated via
     `libxl_cpu_bitmap_alloc` is ALSO GC-arena — but the create path
     copies from it, it does not free it; the dispose frees only the
     array elements, which our mallocs satisfy. Revisit if libxl's
     generated dispose ever gains a `vcpu_hard_affinity` GC check.)
  3. **SIGCHLD ownership**: use `libxl_sigchld_owner_libxl` (self-pipe,
     reaps on the ev_dispatch thread). The default `owner_mainloop` + a
     Go-goroutine forwarder (`libxl_childproc_sigchld_occurred` from a
     `signal.Notify` goroutine) reaps from a different thread than
     ev_dispatch and use-after-freed when vif-ovn exited.
  4. **SIGCHLD assert**: `sigchld_installhandler_core` (libxl_fork.c)
     asserts the previous SIGCHLD disposition was SIG_DFL/SIG_IGN
     **without SA_SIGINFO**. The Go runtime's `os/signal.Ignore` is not
     enough: its `setsig` installs SIG_IGN with the trampoline's
     `SA_SIGINFO|SA_ONSTACK|SA_RESTORER|SA_RESTART` flags still set →
     SIGABRT on the first hotplug fork. So save the action and install a
     **bare** `sigaction` (flags=0, handler=SIG_IGN) around the create,
     restore after (`qlvm_chld_ignore`/`qlvm_chld_restore`), serialized
     by `createMu` (libxl's SIGCHLD install state is process-global).
  5. **unpause**: libxl 4.21 leaves a new domain's vCPUs paused when
     create_new returns; `xl create` unpauses explicitly. Without
     `libxl_domain_unpause` the guest never executes (vCPU time 0).
- **Coremap `Create` failure leak**: the coremap assignment is persisted
  BEFORE the disk work; `fail()` must `cm.Free(name)` + re-save or the
  VM's cores stay assigned forever (fixed 2026-10-19, test
  `TestCreateReflinkFailureFreesCoremapAssignment`).
  Also: the `admin_state` column on the OVS `Interface` model must be a
  `*string` (libovsdb marshals a zero-value `string` as `""` and the
  strict ovsdb-server rejects it as a type mismatch).
