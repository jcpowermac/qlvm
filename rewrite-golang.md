
Re-write the bash scripts to golang. If possible this should be a single command with subcommands and options, as required.

Here are a list of available API/pkgs there might be more please search

https://github.com/xen-project/xen/tree/master/tools/golang/xenlight
https://github.com/ovn-kubernetes/libovsdb
https://github.com/osbuild/image-builder
https://github.com/podman-container-tools/podman/tree/main/libpod
https://pkg.go.dev/golang.org/x/crypto/ssh
- config-management backend (dropped during implementation; provisioning runs over SSH — see Task 12 ruling in docs/superpowers/plans/2026-09-26-qlvm-implementation.md)

If you have to use cgo that is fine. 

Going foward I would like to use only Container-based OS installs and image-builder for agent and persistent virtual machines.

Golang best practices should be followed, use https://github.com/golangci/golangci-lint
TDD should be followed. 

