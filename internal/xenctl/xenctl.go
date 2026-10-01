// Package xenctl is the VM lifecycle surface over Xen: Start/Delete
// orchestrate Xen plus OVN/OVS port state, and the Xen interface wraps the
// libxl binding (xenctl_libxl.go, build tag libxl; builds without libxl get
// the stub whose New reports how to rebuild).
package xenctl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// Xen is the libxl surface the lifecycle orchestration needs. CreateDomain
// takes the pure vm.DomainSpec; the libxl-tagged implementation maps it to
// *xenlight.DomainConfig (supervisor ruling: the default build is cgo-free).
type Xen interface {
	CreateDomain(spec *vm.DomainSpec) error
	Destroy(name string) error
	Shutdown(name string) error
	List() ([]DomainInfo, error)
	Running(name string) (bool, error)
	Close() error
}

// domainState maps the libxl domain flags to the qlvm vm list STATE
// column. Pure (no libxl import) so the mapping is unit-testable without
// the libxl build tag.
func domainState(dying, running, paused, blocked bool) string {
	switch {
	case dying:
		return "dying"
	case paused:
		return "paused"
	case running:
		return "running"
	case blocked:
		return "blocked"
	default:
		return "stopped"
	}
}

// DomainInfo is one row of the Xen domain list (qlvm vm list).
type DomainInfo struct {
	Name  string
	ID    uint32
	MemMB uint64
	VCPUs uint8
	State string
}

// Start boots a prepared VM: it refuses to start a running domain, seeds
// the per-VM kernel files from the template dir if missing, removes stale
// OVS vif ports left by a prior crashed start (duplicate iface-id would
// break OVN binding), then creates the domain. vmDir is the VM state dir
// holding meta.toml, disk.img and the per-VM kernel/initramfs.
func Start(ctx context.Context, x Xen, vp ovs.VifPorter, m *vm.Meta, vmDir string, tpl *template.Template) error {
	running, err := x.Running(m.Name)
	if err != nil {
		return fmt.Errorf("start %s: %w", m.Name, err)
	}
	if running {
		return fmt.Errorf("start %s: domain already running", m.Name)
	}
	stale, err := vp.StaleVifPorts(ctx, m.Name)
	if err != nil {
		return fmt.Errorf("start %s: list stale vif ports: %w", m.Name, err)
	}
	for _, dev := range stale {
		if err := vp.DelVifPort(ctx, dev); err != nil {
			return fmt.Errorf("start %s: remove stale port %s: %w", m.Name, dev, err)
		}
	}
	if err := vm.EnsureKernel(vmDir, tpl.Dir); err != nil {
		return fmt.Errorf("start %s: %w", m.Name, err)
	}
	if err := x.CreateDomain(vm.DomainConfig(vmDir, m, tpl)); err != nil {
		return fmt.Errorf("start %s: create domain: %w", m.Name, err)
	}
	return nil
}

// Delete removes a VM: destroy if running -> drop the OVN switch port ->
// clean stale vif ports -> remove the VM state dir -> drop the ssh config
// block (app type only; meta is read before the dir goes away).
func Delete(ctx context.Context, x Xen, op vm.OVNPorter, vp ovs.VifPorter, home, vmDir, name string) error {
	m, err := vm.LoadMeta(vmDir)
	if err != nil {
		return fmt.Errorf("delete %s: load meta: %w", name, err)
	}
	running, err := x.Running(name)
	if err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	if running {
		if err := x.Destroy(name); err != nil {
			return fmt.Errorf("delete %s: destroy domain: %w", name, err)
		}
	}
	// Retry idempotency: a delete interrupted after dropping the port
	// must still finish, so OVN "not found" is treated as done.
	// ponytail: string match; the OVNPorter interface has no not-found sentinel.
	if err := op.DelLSPort(ctx, name); err != nil && !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("delete %s: remove OVN port: %w", name, err)
	}
	stale, err := vp.StaleVifPorts(ctx, name)
	if err != nil {
		return fmt.Errorf("delete %s: list stale vif ports: %w", name, err)
	}
	for _, dev := range stale {
		if err := vp.DelVifPort(ctx, dev); err != nil {
			return fmt.Errorf("delete %s: remove stale port %s: %w", name, dev, err)
		}
	}
	if err := os.RemoveAll(vmDir); err != nil {
		return fmt.Errorf("delete %s: remove %s: %w", name, vmDir, err)
	}
	if m.Type == "app" {
		if err := vm.RemoveSSHConfig(home, name); err != nil {
			return fmt.Errorf("delete %s: ssh config: %w", name, err)
		}
	}
	return nil
}
