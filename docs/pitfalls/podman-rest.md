# Podman (pkg/bindings REST) Pitfalls

- `podman.socket` must be running: `sudo systemctl enable --now podman.socket`
  (legacy dom0s do not enable it by default; `qlvm template create` needs it).
- **Never build a zero-value `specgen.SpecGenerator` literal**: its
  `HealthLogDestination` stays `""`, and libpod's create path
  (`pkg/specgen/generate/container_create.go`) *unconditionally* runs it
  through `GetValidHealthCheckDestination` → `os.Stat("")` → create fails
  with "HealthCheck Log '' destination error: stat : no such file or
  directory". Use `specgen.NewSpecGenerator(image, false)` and set fields
  on it — the constructor fills the health-log defaults.
- **pkg/bindings `containers.Logs` sends to the caller's channels
  synchronously and never closes them.** Never `for range` the channels —
  it hangs forever once the (fast-exiting) builder is done (this hung
  `create` 23 min after a successful bake). Drain from goroutines and
  `close()` the channels yourself after the `Logs` call returns. A
  non-following fetch after `Wait` reliably returns the full log.
- **An aborted tool call does NOT kill a `sudo qlvm ...` child**: it is
  reparented to init as root and keeps running (kill needs `sudo kill`).
  Check `pgrep -af qlvm` after aborts; clean orphaned containers with
  `podman rm`.
- **image-builder CLI drift**: `ghcr.io/osbuild/image-builder-cli:latest`
  moved the raw build to `build raw <flags>` (top-level `--bootc-*` flags
  are gone) and the entrypoint `chcon`s its cache dir → the container
  must run `--privileged` under SELinux. Raw lands in `$OUTPUT/raw-x86_64.raw`
  (top level; older versions used `image/`), with `--with-buildlog
  --with-manifest` alongside.
