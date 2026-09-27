//go:build libxl

// This file is the real xenlight-backed Xen implementation. It compiles
// only with -tags libxl (libxl-devel required); on this host it is written
// but NOT compile-verified — the default build uses xenctl_stub.go.
package xenctl

import (
	"encoding/hex"
	"fmt"
	"strings"

	"xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// libxlXen implements Xen on one xenlight.Context per process (Close
// releases it; the CLI defers it).
type libxlXen struct {
	*xenlight.Context
}

// New opens the libxl context.
func New() (Xen, error) {
	c, err := xenlight.NewContext()
	if err != nil {
		return nil, fmt.Errorf("libxl context: %w", err)
	}
	return &libxlXen{c}, nil
}

func (x *libxlXen) Close() error { return x.Context.Close() }

func (x *libxlXen) CreateDomain(spec *vm.DomainSpec) error {
	cfg, err := toDomainConfig(spec)
	if err != nil {
		return err
	}
	if _, err := x.DomainCreateNew(cfg); err != nil {
		return fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return nil
}

func (x *libxlXen) Destroy(name string) error {
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if err := x.DomainDestroy(d); err != nil {
		return fmt.Errorf("destroy %s: %w", name, err)
	}
	return nil
}

func (x *libxlXen) Shutdown(name string) error {
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if err := x.DomainShutdown(d); err != nil {
		return fmt.Errorf("shutdown %s: %w", name, err)
	}
	return nil
}

func (x *libxlXen) List() ([]DomainInfo, error) {
	doms := x.ListDomain()
	out := make([]DomainInfo, 0, len(doms))
	for _, d := range doms {
		if d.Domid == 0 {
			continue // dom0 is not a VM
		}
		out = append(out, DomainInfo{
			Name:  x.DomidToName(d.Domid),
			ID:    uint32(d.Domid),
			MemMB: d.CurrentMemkb / 1024,
			VCPUs: uint8(d.VcpuOnline),
			State: stateOf(d),
		})
	}
	return out, nil
}

func (x *libxlXen) Running(name string) (bool, error) {
	if _, err := x.NameToDomid(name); err != nil {
		// An absent domain is not running; a broken context will
		// surface in every other call with its own error.
		return false, nil
	}
	return true, nil
}

func stateOf(d xenlight.Dominfo) string {
	switch {
	case d.Dying:
		return "dying"
	case !d.Running:
		return "stopped"
	case d.Paused:
		return "paused"
	case d.Blocked:
		return "blocked"
	default:
		return "running"
	}
}

// toDomainConfig maps the pure DomainSpec onto the xenlight domain config
// (string spellings come from task 9's DomainSpec, per its comment).
func toDomainConfig(spec *vm.DomainSpec) (*xenlight.DomainConfig, error) {
	cfg, err := xenlight.NewDomainConfig()
	if err != nil {
		return nil, err
	}
	uuid, err := parseUUID(spec.UUID)
	if err != nil {
		return nil, err
	}
	cfg.CInfo.Type = xenlight.DomainTypePvh
	cfg.CInfo.Name = spec.Name
	cfg.CInfo.Uuid = uuid
	cfg.BInfo.MaxVcpus = spec.MaxVcpus
	cfg.BInfo.TargetMemkb = uint64(spec.TargetMemkb)
	cfg.BInfo.Kernel = spec.Kernel
	cfg.BInfo.Ramdisk = spec.Ramdisk
	cfg.BInfo.Cmdline = strings.Join(spec.Extra, " ")
	for _, d := range spec.Disks {
		cfg.Disks = append(cfg.Disks, xenlight.DeviceDisk{
			PdevPath:  d.PdevPath,
			Vdev:      d.Vdev,
			Format:    xenlight.DiskFormatRaw,
			Readwrite: d.Readwrite,
		})
	}
	for _, n := range spec.Nics {
		mac, err := parseMAC(n.Mac)
		if err != nil {
			return nil, err
		}
		cfg.Nics = append(cfg.Nics, xenlight.DeviceNic{
			Mac:     mac,
			Script:  n.Script,
			Nictype: xenlight.NicTypeVif,
		})
	}
	for _, p := range spec.P9S {
		cfg.P9S = append(cfg.P9S, xenlight.DeviceP9{
			Tag:           p.Tag,
			Path:          p.Path,
			SecurityModel: p.SecurityModel,
			Type:          xenlight.P9TypeXen9Pfsd,
		})
	}
	return cfg, nil
}

// parseUUID parses a RFC 4122 string into xenlight.Uuid (no exported
// parser in the generated API; the type is [16]byte).
func parseUUID(s string) (xenlight.Uuid, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return xenlight.Uuid{}, fmt.Errorf("uuid %q: %v", s, err)
	}
	var u xenlight.Uuid
	copy(u[:], b)
	return u, nil
}

// parseMAC parses a colon-separated MAC into xenlight.Mac ([6]byte).
func parseMAC(s string) (xenlight.Mac, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, ":", ""))
	if err != nil || len(b) != 6 {
		return xenlight.Mac{}, fmt.Errorf("mac %q: %v", s, err)
	}
	var m xenlight.Mac
	copy(m[:], b)
	return m, nil
}
