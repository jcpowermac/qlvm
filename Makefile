# BIN: output dir for the built binaries. container-build overrides it to a
# host-mounted writable dir (the dom0 root is ostree-immutable).
BIN ?= bin

# The podman pkg/bindings dependency pulls go.podman.io/storage graph drivers
# and the gpgme signature mechanism, which need cgo headers (btrfs, zfs,
# gpgme) not installed on a bare dom0. qlvm only talks to podman over its
# REST socket, so those drivers and gpgme are excluded at compile time.
# On a machine with the dev headers installed, plain `go build ./...`
# works without these tags.
# libxl is auto-detected: with the Xen dev packages installed the real
# xenlight binding compiles in (internal/xenctl/xenctl_libxl.go); without
# them the stub keeps the build cgo-free and lifecycle commands fail with a
# rebuild hint at runtime. Fedora ships pkg-config module `libxl`
# (libxl-devel); Debian/Ubuntu ship `xenlight` (libxen-dev) — either one
# provides the headers (libxl.h) and link targets (libxenlight) the cgo
# build needs.
LIBXL := $(shell (pkg-config --exists libxl 2>/dev/null || pkg-config --exists xenlight 2>/dev/null) && echo 1)
GO_TAGS := $(strip exclude_graphdriver_btrfs exclude_graphdriver_zfs containers_image_openpgp $(if $(LIBXL),libxl,))



build:
	@test -n "$(LIBXL)" || \
	echo "note: libxl not found (pkg-config --exists libxl); lifecycle commands will report 'built without Xen support' until you install libxl-devel and rebuild" >&2
	go build -tags '$(GO_TAGS)' -o $(BIN)/qlvm ./cmd/qlvm
	go build -tags '$(GO_TAGS)' -o $(BIN)/qlvm-vif ./cmd/qlvm-vif

test:
	go test -tags '$(GO_TAGS)' ./...

# Real-plane integration tests: they run install against the live
# OVN/OVS/firewalld/NetworkManager planes and Xen on this dom0.
test-integration:
	go test -v -tags '$(GO_TAGS) integration' ./internal/itest/

# Container build: compiles with the real xenlight cgo binding inside a
# Fedora container (a plain rpm root, so dnf works where the ostree dom0
# forbids it). The container's xen-devel must be at or below the dom0's
# Xen runtime version — the produced binary loads the dom0's
# libxenlight.so.* at runtime. OUT is a writable host dir the binaries are
# written to (e.g. ~/bin, on PATH). The host needs only podman + a go-aware
# network egress (module + toolchain downloads happen in the container).
# Build image, built once and cached (rebuild only when the Dockerfile or
# the pinned Xen version changes).
BUILDER ?= localhost/qlvm-builder:local

container-builder:
	podman build -t $(BUILDER) -f Dockerfile.builder .

# /usr/local is writable on the ostree dom0 (it is /var/usrlocal) and on
# PATH; running the target as root (sudo make container-build) is required
# to write there.
OUT ?= /usr/local/bin
# /gc = persistent Go cache (toolchain + module downloads happen once).
# yajl is runtime-bundled: the binary carries an $ORIGIN rpath and the
# build copies libyajl.so.2 next to it, so the dom0 needs no new packages.
# The binaries are built in the container's own filesystem and podman-cp'd
# out: writing through a :z host bind mount would relabel them
# container_file_t, a confined type that cannot read dom0 files (domain
# build fails with -3).
container-build: container-builder
	@mkdir -p $(HOME)/.cache/qlvm-build $(OUT)
	podman rm -f qlvm-bld 2>/dev/null || true
	podman create --name qlvm-bld \
		-v $(CURDIR):/src:ro \
		-v $(HOME)/.cache/qlvm-build:/gc:z \
		-e GOPATH=/gc -e GOCACHE=/gc/cache -e GOMODCACHE=/gc/pkg/mod \
		-e 'CGO_LDFLAGS=-Wl,-rpath,$$ORIGIN' \
		$(BUILDER) \
		/bin/sh -c 'cd /src && mkdir -p /out && GOTOOLCHAIN=auto GOFLAGS=-buildvcs=false make build BIN=/out \
		&& cp -L /usr/lib64/libyajl.so.2 /out/'
	podman start qlvm-bld
	@podman wait qlvm-bld >/dev/null
	@code=$$(podman inspect --format '{{.State.ExitCode}}' qlvm-bld); \
		if [ "$$code" != "0" ]; then \
			podman logs qlvm-bld; podman rm -f qlvm-bld; \
			echo "container build failed (exit $$code)" >&2; exit 1; \
		fi
	podman cp qlvm-bld:/out/. $(OUT)/
	podman rm -f qlvm-bld
	@chcon -t bin_t $(OUT)/qlvm $(OUT)/qlvm-vif 2>/dev/null || true

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --build-tags '$(GO_TAGS)'

.PHONY: build test test-integration lint
