# qlvm

Qubes-like VM isolation on dom0: a native Go toolset that manages isolated,
subnet-scoped VM domains with per-domain gateways, a single OVN-backed SNAT
router to the physical LAN, and a declarative dom0 egress firewall —
configured through a single TOML file.

## Building

Use `make build` / `make test` / `make lint`; they pass the podman-bindings
build tags that exclude the btrfs/zfs storage drivers and gpgme (qlvm only
talks to podman over its REST socket). Plain `go build ./...` works only on a
dom0 with the `btrfs-progs-devel`, `libzfs-devel`, and `gpgme-devel` headers
installed.
