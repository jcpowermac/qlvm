package vm

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/template"
)

// OVNPorter is the switch-port surface Create needs; *ovn.Reconciler satisfies it.
type OVNPorter interface {
	AddLSPort(ctx context.Context, sw, name, mac, ip string) error
	DelLSPort(ctx context.Context, name string) error
}

// CreateDeps is the injectable surface of Create (supervisor rulings
// 2026-09-26): Root is the qlvm state root (vmDir = Root/vms/<name>); FSType
// is image-builder's --bootc-default-fs, supplied by the wiring layer.
type CreateDeps struct {
	OVN     OVNPorter
	Tpl     *template.Template
	FS      ostree.FS
	Mounter ostree.Mounter
	Reflink func(dst, src string) error
	Root    string
	FSType  string
}

// Spec is the user-requested VM; zero MemoryMB/VCPUs take defaults from cfg
// per Type (app -> cfg.VM, disposable -> cfg.Disposable).
type Spec struct {
	Name     string
	Domain   string
	Type     string
	Image    string
	MemoryMB int
	VCPUs    int
	Mounts   []Mount
}

// sshConfigFn is the seam Create calls for the app-type side effect, so tests
// can observe its position in the create ordering.
var sshConfigFn = AddSSHConfig

// Create prepares a VM (spec §6): OVN port -> reflink disk -> per-VM XFS
// UUIDs -> per-VM networkd bake -> per-mount 9p .mount units -> meta.toml ->
// ssh-config (app only). Any failure after the port is added deletes the
// port and the half-built VM dir, leaving no orphan port.
func Create(ctx context.Context, d CreateDeps, cfg *config.Config, spec Spec) (*Meta, error) {
	dom, ok := cfg.Domain(spec.Domain)
	if !ok {
		return nil, fmt.Errorf("domain %q not in config", spec.Domain)
	}
	memory, vcpus := spec.MemoryMB, spec.VCPUs
	switch spec.Type {
	case "app":
		if memory == 0 {
			memory = cfg.VM.MemoryMB
		}
		if vcpus == 0 {
			vcpus = cfg.VM.VCPUs
		}
	case "disposable":
		if memory == 0 {
			memory = cfg.Disposable.MemoryMB
		}
		if vcpus == 0 {
			vcpus = cfg.Disposable.VCPUs
		}
	default:
		return nil, fmt.Errorf("type %q: must be app or disposable", spec.Type)
	}
	vmDir := filepath.Join(d.Root, "vms", spec.Name)
	if _, err := LoadMeta(vmDir); err == nil {
		return nil, fmt.Errorf("%s: VM already exists", spec.Name)
	}
	maxHostnum, err := maxDomainHostnum(d.Root, spec.Domain)
	if err != nil {
		return nil, err
	}
	ip, mac := HostIPMAC(dom, maxHostnum)

	fail := func(err error) (*Meta, error) {
		_ = d.OVN.DelLSPort(ctx, spec.Name)
		_ = os.RemoveAll(vmDir)
		return nil, err
	}
	// 0755 + world-readable meta.toml: `qlvm vm run` is a user-session command
	// and must LoadMeta without root (disk.img itself stays 0600).
	if err := os.MkdirAll(vmDir, 0o755); err != nil { //nolint:gosec // G301: user-session read is the design
		return nil, fmt.Errorf("create %s: %w", spec.Name, err)
	}
	if err := d.OVN.AddLSPort(ctx, spec.Domain, spec.Name, mac, ip); err != nil {
		return fail(fmt.Errorf("add OVN port %s: %w", spec.Name, err))
	}
	disk := filepath.Join(vmDir, "disk.img")
	if err := d.Reflink(disk, filepath.Join(d.Tpl.Dir, "template.raw")); err != nil {
		return fail(fmt.Errorf("reflink disk: %w", err))
	}
	// Per-VM XFS UUIDs before the networkd bake: the bake remounts the root
	// partition, and xfs_admin needs it unmounted (and must run on the fresh
	// reflink, not the template).
	if err := ostree.UniqueXFS(ctx, d.FS, disk, d.FSType); err != nil {
		return fail(fmt.Errorf("unique xfs uuids: %w", err))
	}
	dns := ""
	if len(cfg.Network.DNS) > 0 {
		dns = cfg.Network.DNS[0]
	}
	if err := ostree.BakeNetworkd(ctx, d.FS, disk, d.FSType, ip, dom.Gateway, mac, dns); err != nil {
		return fail(fmt.Errorf("networkd bake: %w", err))
	}
	if len(spec.Mounts) > 0 {
		shares := make([]ostree.SharedMount, 0, len(spec.Mounts))
		for i, mt := range spec.Mounts {
			where := mt.Guest
			if !strings.HasPrefix(where, "/") {
				where = "/" + where
			}
			shares = append(shares, ostree.SharedMount{Where: where, What: P9Tag(spec.Name, i)})
		}
		if err := ostree.BakeMounts(ctx, d.FS, disk, d.FSType, shares); err != nil {
			return fail(fmt.Errorf("mount units bake: %w", err))
		}
	}
	m := &Meta{
		Name:     spec.Name,
		Type:     spec.Type,
		Image:    spec.Image,
		Digest:   d.Tpl.Digest,
		Domain:   spec.Domain,
		IP:       ip,
		MAC:      mac,
		MemoryMB: memory,
		VCPUs:    vcpus,
		Mounts:   spec.Mounts,
		UUID:     newUUID(),
		Created:  time.Now(),
	}
	if err := m.Save(vmDir); err != nil {
		return fail(fmt.Errorf("save meta: %w", err))
	}
	if spec.Type == "app" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fail(fmt.Errorf("home dir: %w", err))
		}
		if err := sshConfigFn(home, m); err != nil {
			return fail(fmt.Errorf("ssh config: %w", err))
		}
	}
	return m, nil
}

// maxDomainHostnum returns the highest host number assigned to a domain's
// persisted VMs under Root/vms (0 when none) — the self-contained stand-in
// for "existing ports on the switch" (ruling 2026-09-26): a failed Create
// deletes its port, so metas and ports agree. A count would collide after a
// delete, so the max is used instead.
func maxDomainHostnum(root, domain string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(root, "vms"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	maxH := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := LoadMeta(filepath.Join(root, "vms", e.Name()))
		if err != nil {
			// A dir left by a crash between MkdirAll and meta save must not
			// poison later creates: unreadable meta = not counted.
			continue
		}
		if m.Domain != domain {
			continue
		}
		// IP is <subnet>.<hostnum>: the last octet is the host number.
		dot := strings.LastIndexByte(m.IP, '.')
		if dot < 0 {
			continue
		}
		n, err := strconv.Atoi(m.IP[dot+1:])
		if err != nil || n <= 0 {
			continue
		}
		if n > maxH {
			maxH = n
		}
	}
	return maxH, nil
}

// newUUID returns a random RFC 4122 v4 UUID string (persisted in Meta so the
// domain keeps a stable identity across start/stop).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
