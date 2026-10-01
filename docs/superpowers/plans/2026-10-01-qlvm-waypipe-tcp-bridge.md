# Plan: waypipe over a TCP bridge (no SSH in the projection path)

Date: 2026-10-01. Supersedes the `waypipe ssh` transport for `vm run` and
`apps` launch. `apps sync` (desktop fetch) keeps SSH.

## Why

`waypipe ssh` carries the projection over an interactive SSH session:
host-key churn on every VM generation, `ServerAlive*` tuning for the OVN
idle-TCP blackhole, and sshd as a dependency of the app path. vsock was the
first candidate (waypipe has native `--vsock`) but the stock Fedora kernel
has no `CONFIG_XEN_VSOCKETS` (no `xen-vsock.ko` in the guest's kernel tree),
and a custom kernel build in os-bolt was ruled out for this pass.

Decision: **plain TCP over the existing vif/ovn path**, bridged to
waypipe's unix protocol sockets by a baked guest relay (`ncat`) and a
dom0-side forwarder inside qlvm. Auth: per-VM token baked at create.

## Architecture (per `qlvm vm run <vm> app...`)

```
dom0                                        guest
──────                                      ─────
qlvm (Go):
  waypipe --socket /tmp/A.sock client  ←─── waypipe server (per app)
  TCP 0.0.0.0:PORT (ephemeral)          ncat -lkU /run/user/1000/B.sock
    accept → io.Copy ↔ unix A.sock        --sh-exec 'ncat DOM0IP PORT'
  control: dial ip:4711, writes          qvm-ctl.service (oneshot,
    <token>\n<PORT>\n<dom0IP>\n<exec>\n   socket-activated on tcp:4711)
```

- Guest: `qvm-ctl.socket` (systemd, `ListenStream=4711`) activates
  `qvm-ctl.service` per connection; `/etc/qvm/qvm-ctl` (sh) reads the 4-line
  frame, checks the token against `/etc/qvm/waypipe-token`, starts the
  ncat bridge, then runs `waypipe -o -n -s B.sock --display qvm server -- <exec>`.
- Dom0: `internal/projection` listens TCP, launches the `waypipe client`,
  forwards accepted TCP ↔ the client's unix socket, sends the control frame,
  waits for the client to exit (app exit), cleans up.
- Data port is ephemeral per run; dom0 zone is `target=ACCEPT` so no
  inbound firewall work. The DROP **egress** policy currently only allows
  22/tcp to the VM supernet → add an unconditional 4711/tcp rule to
  `EgressRules` (control port, token-authenticated, only reachable through
  the OVN supernet).

## Changes

### 1. os-bolt (sibling repo `../os/recipes/bolt.yml`)
Add `nmap-ncat` to the rpm install list. (No python3/ncat in the bootc
minimal base. This requires an image rebuild + `qlvm template create`
re-bake before live use.)

### 2. `internal/ostree/surgery.go` — `BakeControl`
Per-VM bake (runs at create, same mount path as `BakeNetworkd`):
- `/etc/qvm/qvm-ctl` (0755, sh script above; empty exec → `bash -l`)
- `/etc/qvm/waypipe-token` (0600, per-VM token)
- `/etc/systemd/system/qvm-ctl.socket` + `qvm-ctl.service`
  (`Type=oneshot`, `User=<user>`, `Requires/After=bolt-rundir.service`,
  `Environment=HOME=/var/home/<user> XDG_RUNTIME_DIR=/run/user/1000`,
  `ExecStart=/bin/sh /etc/qvm/qvm-ctl` — `sh <file>` so SELinux only needs
  *read* on etc_t, not execute)
- enable symlink `sockets.target.wants/qvm-ctl.socket`
Extract `deploymentEtc()` from `BakeNetworkd`'s inline path computation and
share it.

### 3. `internal/vm`
- `meta.go`: `Token string `toml:"token"`` (world-readable meta.toml —
  `vm run` reads it as the desktop user).
- `create.go`: generate 32-hex token (crypto/rand), pass to
  `ostree.BakeControl(ctx, d.FS, disk, d.FSType, SSHUser, token)`, store in
  Meta.

### 4. `internal/fw/fw.go`
`EgressRules`: unconditional
`rule family="ipv4" destination address="<supernet>" port port="4711"
protocol="tcp" accept` (waypipe control; token-authenticated on the guest).
No new config key → existing qlvm.toml files keep working.

### 5. `internal/projection` (new package)
```go
type Deps struct {
    Waypipe     func(args []string, stdin io.Reader, out, errW io.Writer) error
    DialControl func(ctx context.Context, addr string) (net.Conn, error)
    TempDir     string // waypipe socket dir, default os.TempDir()
}
func Run(ctx, d Deps, m *vm.Meta, dom0IP, exec string, out, errW io.Writer) error
func WaitControl(ctx, dialer, ip string, interval, timeout) error // apps gate
```
`Run`: TCP listen 0.0.0.0:0 → spawn waypipe client (`--socket <tmp>/
qlvm-waypipe-<vm>-<pid>.sock --unlink-socket client`) → poll for the socket
file (≤5 s; the guest must not dial before the client binds) → forward
goroutine (accept → dial unix socket → bidirectional io.Copy) → send control
frame → wait for waypipe exit → close listener.

### 6. `internal/cli`
- `run.go`: `vm run <vm> [app...]` → load meta + config (existing
  `--config` default `/etc/qvm/qlvm.toml`), `dom0IP = cfg.Network.RouterIP`,
  `exec = strings.Join(args[1:], " ")` (empty → guest defaults to `bash -l`),
  `projection.Run`. Keep the `WAYLAND_DISPLAY` guard and the `runWaypipe`
  seam (tests use it).
- `apps.go` launchApp: waiter → `projection.WaitControl` (replaces
  `sshx.WaitForSSH`); runner → `projection.Run`. `apps sync` unchanged (ssh).
- `vm run` requires the VM to be started already (unchanged behavior).

## Tests (TDD)

- `projection_test.go`: forward loop (real TCP listener 127.0.0.1:0 + unix
  listener, bidirectional bytes); `Run` with fake waypipe (accepts the unix
  conn, asserts data) and fake DialControl (asserts the exact 4-line frame,
  then simulates the guest dialing back the data port); token/port present.
- `surgery_test.go`: `BakeControl` on the fake FS — files, modes, unit
  content (user substituted), enable symlink, token content.
- `create_test.go`: Meta carries the token; BakeControl called (fake FS
  observes the writes).
- `fw` test: `EgressRules` includes the 4711 rule with the supernet.
- `run_test.go`/`apps_test.go`: updated seams.

## Live smoke (real dom0)

1. Rebuild os-bolt image with nmap-ncat; `qlvm template create` (timeout
   600+).
2. fw: re-run the firewall Ensure (which command — check wiring) + reload;
   verify 4711 rule in the dom0-egress policy.
3. `qlvm vm create <n>` → `vm start` → `qlvm vm run <n> firefox` (or
   `bash -l` first): app appears on the dom0 Wayland session; app exit
   returns `run`; repeat run works (oneshot cleanup).
4. `qlvm apps sync` + rofi launch path.
5. Negative: wrong token → guest refuses; stopped VM → clean error.
6. Kill mid-run: no orphan ncat/waypipe in guest, listener freed on dom0.

## Pitfalls to watch (known classes from this repo)

- SELinux: /etc/qvm files read by the unconfined_service_t service — the
  `sh <file>` ExecStart form avoids execute-permission; verify in smoke.
- waypipe server's first dial of B.sock can race the ncat listener — the
  script polls for the socket file; waypipe retries dials (ssh mode relies
  on the same).
- `bolt-rundir.service` must be up before the app needs XDG_RUNTIME_DIR —
  Requires/After in the unit.
- Existing templates (no ncat, no units) don't support the new path:
  `vm run` on them fails with a clear "re-bake template" error.
