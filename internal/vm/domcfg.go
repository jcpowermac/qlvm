package vm

import (
	"path/filepath"

	"github.com/jcpowermac/qlvm/internal/template"
)

// DomainSpec is the pure, serializable form of the libxl domain
// configuration (supervisor ruling 2026-09-26: brief wanted
// *xenlight.DomainConfig, but the cgo-free default build cannot import
// xenlight). Task 10's libxl-tagged file maps DomainSpec ->
// *xenlight.DomainConfig and boots it; the string values match the
// xenlight enum spellings ("PVH", "raw", "vif", "none", "xen9pfsd").
type DomainSpec struct {
	Type        string
	Name        string
	UUID        string
	Kernel      string
	Ramdisk     string
	Extra       []string
	MaxVcpus    int
	TargetMemkb int
	Disks       []DomainDisk
	Nics        []DomainNic
	P9S         []DomainP9
}

// DomainDisk is one libxl-style disk entry in a DomainSpec.
type DomainDisk struct {
	PdevPath  string
	Vdev      string
	Format    string
	Readwrite int
}

// DomainNic is one libxl-style NIC entry in a DomainSpec.
type DomainNic struct {
	Mac     string
	Script  string
	Nictype string
}

// DomainP9 is one libxl-style 9pfs entry in a DomainSpec.
type DomainP9 struct {
	Tag           string
	Guest         string
	Path          string
	SecurityModel string
	Type          string
}

// DomainConfig renders the libxl domain configuration for a prepared VM.
// vmDir is the VM state dir holding disk.img (ruling 2026-09-26: added as a
// parameter rather than a Meta field).
func DomainConfig(vmDir string, m *Meta, tpl *template.Template) *DomainSpec {
	extra := []string{"root=" + tpl.RootDev}
	if tpl.RootFlags != "" {
		extra = append(extra, tpl.RootFlags)
	}
	extra = append(extra,
		"ostree="+tpl.OstreePath,
		"systemd.default-target=multi-user.target",
		"console=hvc0",
	)
	p9s := make([]DomainP9, 0, len(m.Mounts))
	for _, mt := range m.Mounts {
		p9s = append(p9s, DomainP9{
			Tag:           m.Name,
			Guest:         mt.Guest,
			Path:          mt.Host,
			SecurityModel: "none",
			Type:          "xen9pfsd",
		})
	}
	return &DomainSpec{
		Type:        "PVH",
		Name:        m.Name,
		UUID:        m.UUID,
		Kernel:      filepath.Join(tpl.Dir, "vmlinuz"),
		Ramdisk:     filepath.Join(tpl.Dir, "initramfs"),
		Extra:       extra,
		MaxVcpus:    m.VCPUs,
		TargetMemkb: m.MemoryMB * 1024,
		Disks: []DomainDisk{{
			PdevPath:  filepath.Join(vmDir, "disk.img"),
			Vdev:      "xvda",
			Format:    "raw",
			Readwrite: 1,
		}},
		Nics: []DomainNic{{Mac: m.MAC, Script: "vif-ovn", Nictype: "vif"}},
		P9S:  p9s,
	}
}
