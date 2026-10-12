# CI (GitHub Actions) Pitfalls

- **CI = ubuntu-latest: non-root, no SELinux, no libxl** (stub path; the
  Makefile's exclude tags already handle the podman cgo deps). Unit tests
  must pass there, not just on this SELinux-enforcing dom0.
- **Fake-FS contract**: a fake's mount target is a REAL temp dir, so any
  raw `os.*` call on a target path materializes for real and leaks into
  raw-OS side effects (chown/setxattr ran on CI and broke the bake tests
  2026-09-29). Route tree operations through the `ostree.FS` interface and
  keep raw side effects Lstat-guarded (surgery.go `chownForUser` /
  `setSELinuxContext`). Repro the CI failure locally: `sudo go test
  ./internal/ostree/`, or non-root via `podman run --rm -v $PWD:/src:z -w
  /src localhost/qlvm-builder:local sh -c 'go test -tags
  "exclude_graphdriver_btrfs exclude_graphdriver_zfs
  containers_image_openpgp" ./...'`.
