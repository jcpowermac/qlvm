# Build image for `make container-build`. A plain Fedora container root is a
# normal rpm system (dnf works where the ostree dom0 forbids it). Pin the
# xen runtime here to the dom0's: the produced binaries load the DOM0's
# libxenlight.so.* at runtime, so the build headers must be at or below the
# dom0's Xen version (dom0 currently: 4.21, quay.io/fedora/fedora:44).
# Rebuild when either side moves: make container-builder
FROM quay.io/fedora/fedora:44

# golang: the module cache (GOPATH, mounted at runtime) provides the exact
# toolchain via GOTOOLCHAIN=auto; this only seeds the first download.
RUN dnf install -y golang gcc pkg-config xen-devel yajl-devel && dnf clean all

WORKDIR /src
