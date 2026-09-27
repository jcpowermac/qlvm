BIN := bin

# The podman pkg/bindings dependency pulls go.podman.io/storage graph drivers
# and the gpgme signature mechanism, which need cgo headers (btrfs, zfs,
# gpgme) not installed on a bare dom0. qlvm only talks to podman over its
# REST socket, so those drivers and gpgme are excluded at compile time.
# On a machine with the dev headers installed, plain `go build ./...`
# works without these tags.
# libxl is auto-detected: with libxl-devel installed the real xenlight
# binding compiles in (internal/xenctl/xenctl_libxl.go); without it the
# stub keeps the build cgo-free and lifecycle commands fail with a rebuild
# hint at runtime.
LIBXL := $(shell pkg-config --exists libxl 2>/dev/null && echo 1)
GO_TAGS := $(strip exclude_graphdriver_btrfs exclude_graphdriver_zfs containers_image_openpgp $(if $(LIBXL),libxl,))



build:
	@test -n "$(LIBXL)" || \
	echo "note: libxl not found (pkg-config --exists libxl); lifecycle commands will report 'built without Xen support' until you install libxl-devel and rebuild" >&2
	go build -tags '$(GO_TAGS)' -o $(BIN)/qlvm ./cmd/qlvm

test:
	go test -tags '$(GO_TAGS)' ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --build-tags '$(GO_TAGS)'

.PHONY: build test lint
