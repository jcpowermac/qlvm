package vm

import (
	"fmt"
	"path/filepath"
	"strconv"

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
	// CPUs is the list of physical CPU cores this domain is pinned to.
	// Empty = no pinning (Xen scheduler decides).
	CPUs []int
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

// P9Tag is the unique 9p tag (libxl mrtag) of a VM's i-th --mount
// (0-based in Meta.Mounts order): duplicate mrtags make shares
// indistinguishable to the guest, so one mount per VM name is not enough.
func P9Tag(vmName string, i int) string {
	return vmName + "-" + strconv.Itoa(i)
}

// agentHomeGuest is the guest mount point for an agent VM's pi home (its
// $HOME). It matches the ostree VM convention (9p shares mount under /var).
const agentHomeGuest = "/var/data"

// agentHomeHost is the isolated per-VM dom0 share dir that backs an agent
// VM's pi home. It lives under /var/lib/qlvm/9p/<name>/ (never the dom0
// user's ~/.pi) so each agent has its own, host-inspectable, persistent
// config/secrets/workspace.
func agentHomeHost(name string) string {
	return filepath.Join("/var/lib/qlvm/9p", name, "data")
}

// p9sFor renders the libxl 9pfs entries for a VM's --mount list, shared by
// the ostree and agent domain-config paths so a --mount host:guest becomes a
// real share (agentDomainConfig previously hardcoded P9S empty).
func p9sFor(name string, mounts []Mount) []DomainP9 {
	p9s := make([]DomainP9, 0, len(mounts))
	for i, mt := range mounts {
		p9s = append(p9s, DomainP9{
			Tag:           P9Tag(name, i),
			Guest:         mt.Guest,
			Path:          mt.Host,
			SecurityModel: "none",
			Type:          "xen9pfsd",
		})
	}
	return p9s
}

// DomainConfig renders the libxl domain configuration for a prepared VM.
// vmDir is the VM state dir holding disk.img (ruling 2026-09-26: added as a
// parameter rather than a Meta field).
func DomainConfig(vmDir string, m *Meta, tpl *template.Template) *DomainSpec {
	if template.IsAgent(tpl) {
		return agentDomainConfig(vmDir, m)
	}
	extra := []string{"root=" + tpl.RootDev}
	if tpl.RootFlags != "" {
		extra = append(extra, tpl.RootFlags)
	}
	extra = append(extra,
		"ostree="+tpl.OstreePath,
		"systemd.default-target=multi-user.target",
		"console=hvc0",
	)
	// The guest mount point is not a libxl/kernel field: each share is
	// mounted by a systemd .mount unit baked into the VM disk at create
	// time (ostree.BakeMounts), matching What= to the tag below.
	p9s := p9sFor(m.Name, m.Mounts)
	return &DomainSpec{
		Type: "PVH",
		Name: m.Name,
		UUID: m.UUID,
		// The boot kernel is per-VM state in vmDir (seeded from the
		// template dir at start by EnsureKernel; refreshed in place by
		// sync-kernel), so one VM's upgrade never re-points a sibling's boot.
		Kernel:      filepath.Join(vmDir, "vmlinuz"),
		Ramdisk:     filepath.Join(vmDir, "initramfs"),
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
		CPUs: m.Cores,
	}
}

// agentDomainConfig renders the libxl domain configuration for an agent VM:
// kernel+initramfs boot (initramfs is root), static IP via kernel cmdline,
// no ostree params, no root disk.
func agentDomainConfig(vmDir string, m *Meta) *DomainSpec {
	// The agent's pi home is an always-present, isolated 9p share (index 0):
	// the dom0 side is a per-VM dir under /var/lib/qlvm/9p (never the dom0
	// user's ~/.pi), mounted rw at /var/data in the guest (pi's $HOME). Any
	// --mount shares are additional (index 1+).
	homeTag := P9Tag(m.Name, 0)
	p9s := []DomainP9{{
		Tag:           homeTag,
		Guest:         agentHomeGuest,
		Path:          agentHomeHost(m.Name),
		SecurityModel: "none",
		Type:          "xen9pfsd",
	}}
	cmdline := []string{fmt.Sprintf("qlvm.mount=%s=%s", homeTag, agentHomeGuest)}
	for i, mt := range m.Mounts {
		t := P9Tag(m.Name, i+1)
		g := mt.Guest
		if !filepath.IsAbs(g) {
			g = "/" + g
		}
		p9s = append(p9s, DomainP9{
			Tag: t, Guest: g, Path: mt.Host, SecurityModel: "none", Type: "xen9pfsd",
		})
		cmdline = append(cmdline, fmt.Sprintf("qlvm.mount=%s=%s", t, g))
	}

	spec := &DomainSpec{
		Type: "PVH",
		Name: m.Name,
		UUID: m.UUID,
		Kernel:  filepath.Join(vmDir, "vmlinuz"),
		Ramdisk: filepath.Join(vmDir, "initramfs"),
		Extra: []string{
			"console=hvc0",
			"ip=" + m.IP,
		},
		MaxVcpus:    m.VCPUs,
		TargetMemkb: m.MemoryMB * 1024,
		Disks:       []DomainDisk{},
		Nics: []DomainNic{{Mac: m.MAC, Script: "vif-ovn", Nictype: "vif"}},
		P9S:         p9s, // pi home (index 0) + any --mount shares (index 1+)
		CPUs:        m.Cores,
	}
	// Tell the busybox init which 9p tags to mount where (no systemd to bake a
	// unit; init reads these and mounts each with a retry loop). The ostree
	// path does not need this — its baked .service carries the tag + path.
	spec.Extra = append(spec.Extra, cmdline...)
	if m.WorkspaceSizeMB > 0 {
		spec.Disks = []DomainDisk{{
			PdevPath:  filepath.Join(vmDir, "workspace.img"),
			Vdev:      "xvdb",
			Format:    "raw",
			Readwrite: 1,
		}}
	}
	return spec
}
