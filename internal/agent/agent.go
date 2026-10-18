// Package agent implements the privileged qlvm D-Bus daemon (qlvm-agent).
// The daemon runs as root under systemd, exposes io.qlvm.Agent on the
// system bus, and delegates to qlvm's internal packages. The GUI (running
// as the user) calls the daemon over D-Bus.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/jcpowermac/qlvm/internal/apps"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/mounts"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/provisioner"
	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// D-Bus constants.
const (
	ServiceName = "io.qlvm.Agent"
	ObjectPath  = "/io/qlvm/Agent"
	Iface       = "io.qlvm.Agent"
)

// VMInfo is a single row of the VM list, as returned by D-Bus.
type VMInfo struct {
	Name      string
	Type      string // app|disposable|agent
	State     string // running|stopped|dying|paused|blocked
	MemoryMB  uint64
	VCPUs     uint8
	Domain    string
	Image     string
	Digest    string
	Mounts    []string // "host:guest" pairs
	CreatedAt int64    // unix seconds
}

// TemplateInfo is a single row of the template list.
type TemplateInfo struct {
	Dir       string
	Image     string
	Digest    string
	Kernel    string
	SizeBytes uint64
	VMCount   int
	Complete  bool
}

// DomainInfo is one row of the Xen domain list.
type DomainInfo struct {
	Name  string
	ID    uint32
	MemMB uint64
	VCPUs uint8
	State string
}

// CreateVMOptions are the parameters for CreateVM.
type CreateVMOptions struct {
	Name     string
	Domain   string
	Template string
	Type     string
	MemoryMB uint64
	VCPUs    uint8
	Mounts   []string
	Cores    string
}

// ConfigInfo is the D-Bus-friendly representation of the qlvm configuration.
type ConfigInfo struct {
	Network    NetworkInfo
	Domains    []DomainConfigInfo
	VM         VMDefaultsInfo
	Disposable VMDefaultsInfo
	Agent      VMDefaultsInfo
	Firewall   FirewallInfo
	Dom0       Dom0ConfigInfo
}

// NetworkInfo is the D-Bus-friendly network configuration.
type NetworkInfo struct {
	NIC      string
	Gateway  string
	RouterIP string
	Dom0IP   string
	DNS      []string
}

// DomainConfigInfo is one isolation domain from the configuration.
type DomainConfigInfo struct {
	Name    string
	Subnet  string
	Gateway string
}

// VMDefaultsInfo is the D-Bus-friendly VM defaults.
type VMDefaultsInfo struct {
	User     string
	MemoryMB int
	VCPUs    int
}

// FirewallInfo is the D-Bus-friendly firewall configuration.
type FirewallInfo struct {
	Egress FirewallEgressInfo
}

// FirewallEgressInfo is the D-Bus-friendly egress policy.
type FirewallEgressInfo struct {
	AllowDNS      bool
	AllowHTTPS    bool
	AllowSSHToVMs bool
	AllowICMP     bool
	ExtraRules    []string
}

// Dom0ConfigInfo is the D-Bus-friendly dom0 resource limits.
type Dom0ConfigInfo struct {
	MemoryMB int
	VCPUs    int
	Pin      bool
	PinSet   bool
}

// CoremapInfo is the D-Bus-friendly representation of the CPU core map.
type CoremapInfo struct {
	TotalCores  int
	PCores      []int
	ECores      []int
	Dom0Cores   []int
	Assignments []CoremapAssignmentInfo
}

// CoremapAssignmentInfo is one VM's core allocation.
type CoremapAssignmentInfo struct {
	VM    string
	Cores []int
}

// Agent is the D-Bus object exported by the daemon. Methods are mapped to
// D-Bus by godbus reflection. Long-running methods emit Progress signals
// and return on completion (or error).
type Agent struct {
	// Control planes (wired by the daemon's main).
	xen         xenctl.Xen
	ovn         vm.OVNPorter
	ovs         ovs.VifPorter
	cfg         *config.Config
	coremapPath string
	root        string // qlvm state root (/var/lib/qlvm)

	// podmanFactory creates the Podman client for template operations.
	// Defaults to template.NewPodman; tests inject a fake.
	podmanFactory func(ctx context.Context, socket string) (template.Podman, error)

	// bakeFn bakes a template directory. Defaults to ostree.BakeTemplate;
	// tests inject a recording fake.
	bakeFn func(ctx context.Context, dir, fstype, sshAuthKeys string) error

	// provisionRunner executes ProvisionVM ops. Nil = use the default
	// SSH runner; tests inject a fake.
	provisionRunner provisioner.Runner

	// provisionDir is the layered provision content dir (base/ + per-vm/).
	// Empty = use provisionDefaultDir; tests inject a temp dir.
	provisionDir string

	// homeDir is the SSH home for provisioner resolution and SyncKernel
	// auth. Empty = use os.UserHomeDir; tests inject a temp dir.
	homeDir string

	// kernelHost is the remote end for SyncKernel. Nil = connect to the
	// VM's IP via sshConnectFunc (or sshx.Connect); tests inject a fake.
	kernelHost kernelSyncer

	// sshConnectFunc dials a VM for SyncKernel when kernelHost is nil.
	// Nil = use sshx.Connect; tests inject a fake.
	sshConnectFunc func(ctx context.Context, home, host, user string) (*ssh.Client, error)

	// appSyncFetch pulls one VM's desktop entries using the SSH identity
	// of home. Nil = defaultAppSyncFetch; tests inject a fake.
	appSyncFetch func(ctx context.Context, home, host string) (map[string]string, error)

	// Progress signal state (mutex-protected).
	mu       sync.Mutex
	progress map[string]*ProgressTracker

	// conn is the D-Bus connection (set by Run).
	conn *dbus.Conn

	// progressHook is a test hook called on every progress emission.
	// It is called with (id, message, done) where done indicates whether
	// this is a ProgressDone emission. Nil in production.
	progressHook func(id, message string, done bool)
}

// ProgressTracker tracks a long-running operation for signal emission.
type ProgressTracker struct {
	ID      string
	Message string
	Done    bool
}

// newOpID generates a unique operation ID for progress tracking.
// The prefix identifies the operation type; the UUID ensures uniqueness
// across concurrent operations of the same type.
func newOpID(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

// New creates an Agent with the given control planes. root is the qlvm
// state root (e.g. /var/lib/qlvm) where VM and template state is stored.
func New(x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath, root string) *Agent {
	return &Agent{
		xen:           x,
		ovn:           o,
		ovs:           v,
		cfg:           cfg,
		coremapPath:   coremapPath,
		root:          root,
		podmanFactory: template.NewPodman,
		bakeFn: func(ctx context.Context, dir, fstype, sshAuthKeys string) error {
			return ostree.BakeTemplate(ctx, ostree.NewFS(nil), dir, fstype, sshAuthKeys)
		},
		progress: make(map[string]*ProgressTracker),
	}
}

// Run registers the Agent on the system bus and blocks until the bus
// connection closes or ctx is cancelled. Returns the acquired bus name.
func (a *Agent) Run(ctx context.Context) (string, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return "", fmt.Errorf("system bus: %w", err)
	}
	a.conn = conn

	if err := conn.ExportAll(a, dbus.ObjectPath(ObjectPath), Iface); err != nil {
		_ = conn.Close()
		a.conn = nil
		return "", fmt.Errorf("export agent: %w", err)
	}

	if _, err := conn.RequestName(ServiceName, dbus.NameFlagDoNotQueue|dbus.NameFlagReplaceExisting); err != nil {
		_ = conn.ExportAll(nil, dbus.ObjectPath(ObjectPath), Iface)
		_ = conn.Close()
		a.conn = nil
		return "", fmt.Errorf("request name %s: %w", ServiceName, err)
	}

	log.Printf("qlvm-agent running on system bus as %s", ServiceName)

	// Block until ctx is cancelled or the connection closes. conn.Context()
	// is cancelled by godbus when the connection is closed or the peer
	// disconnects, so this is event-driven rather than polling.
	select {
	case <-ctx.Done():
	case <-conn.Context().Done():
	}

	_ = conn.ExportAll(nil, dbus.ObjectPath(ObjectPath), Iface)
	_ = conn.Close()
	a.conn = nil
	return ServiceName, nil
}

// --- Read methods ---

// ListVMs returns all VMs: running domains from Xen plus stopped VMs from
// meta.yaml. The result is sorted by name.
func (a *Agent) ListVMs() ([]VMInfo, error) {
	domains, err := a.xen.List()
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}

	// Build a map of running domains for fast lookup.
	running := make(map[string]xenctl.DomainInfo, len(domains))
	for _, d := range domains {
		running[d.Name] = d
	}

	// Load all VM metas from the state root.
	metas, err := a.loadAllMetas()
	if err != nil {
		return nil, fmt.Errorf("load VM metas: %w", err)
	}

	// Build the result: every domain (running) plus every meta not in domains (stopped).
	result := make([]VMInfo, 0, len(metas)+len(domains))
	seen := make(map[string]bool, len(metas)+len(domains))

	// Add all metas (running or stopped).
	for _, m := range metas {
		info := a.metaToVMInfo(m, running)
		result = append(result, info)
		seen[m.Name] = true
	}

	// Add any running domains that don't have a meta (e.g. VMs created outside qlvm).
	for _, d := range domains {
		if !seen[d.Name] {
			result = append(result, VMInfo{
				Name:     d.Name,
				State:    d.State,
				MemoryMB: d.MemMB,
				VCPUs:    d.VCPUs,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// loadAllMetas reads every <root>/vms/*/meta.yaml, skipping unreadable
// entries (a crashed create may leave a dir without meta).
func (a *Agent) loadAllMetas() ([]*vm.Meta, error) {
	vmsDir := filepath.Join(a.root, "vms")
	entries, err := os.ReadDir(vmsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var metas []*vm.Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := vm.LoadMeta(filepath.Join(vmsDir, e.Name()))
		if err != nil {
			continue // skip dirs without a loadable meta
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// metaToVMInfo converts a VM meta to VMInfo, using the running domain map
// to fill in live state (memory, vcpus, state) when the domain is up.
func (a *Agent) metaToVMInfo(m *vm.Meta, running map[string]xenctl.DomainInfo) VMInfo {
	info := VMInfo{
		Name:      m.Name,
		Type:      m.Type,
		State:     "stopped",
		MemoryMB:  uint64(m.MemoryMB), //nolint:gosec,G115
		VCPUs:     uint8(m.VCPUs),     //nolint:gosec,G115
		Domain:    m.Domain,
		Image:     m.Image,
		Digest:    m.Digest,
		CreatedAt: m.Created.Unix(),
	}

	// Fill mounts as "host:guest" pairs.
	for _, mount := range m.Mounts {
		info.Mounts = append(info.Mounts, mount.Host+":"+mount.Guest)
	}

	// If the domain is running, override with live Xen state.
	if d, ok := running[m.Name]; ok {
		info.State = d.State
		info.MemoryMB = d.MemMB
		info.VCPUs = d.VCPUs
	}

	return info
}

// ListTemplates returns all templates in <root>/templates. A missing
// templates dir returns an empty list.
func (a *Agent) ListTemplates() ([]TemplateInfo, error) {
	refs := a.vmTemplateRefs()
	templatesDir := filepath.Join(a.root, "templates")

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read templates dir: %w", err)
	}

	var result []TemplateInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info := a.templateDirToInfo(e.Name(), refs)
		result = append(result, info)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Dir < result[j].Dir })
	return result, nil
}

// vmTemplateRefs maps template dir path -> sorted VM names whose meta.yaml
// references that template dir.
func (a *Agent) vmTemplateRefs() map[string][]string {
	refs := make(map[string][]string)
	vmsDir := filepath.Join(a.root, "vms")
	entries, err := os.ReadDir(vmsDir)
	if err != nil {
		return refs
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := vm.LoadMeta(filepath.Join(vmsDir, e.Name()))
		if err != nil {
			continue
		}
		// Compute the template dir from the VM's image/digest.
		dir := template.DirOfRef(a.root, m.Image, template.NormalizeDigest(m.Digest))
		refs[dir] = append(refs[dir], m.Name)
	}
	for _, names := range refs {
		sort.Strings(names)
	}
	return refs
}

// templateDirToInfo builds a TemplateInfo for a single template directory.
func (a *Agent) templateDirToInfo(name string, refs map[string][]string) TemplateInfo {
	dir := filepath.Join(a.root, "templates", name)
	info := TemplateInfo{
		Dir:     name,
		VMCount: len(refs[dir]),
	}

	// Load META if present.
	if t, err := template.LoadMeta(dir); err == nil {
		info.Complete = true
		info.Image = t.Image
		info.Digest = t.Digest
		info.Kernel = t.KernelVer
	}

	// Get raw image size if present.
	if fi, err := os.Stat(filepath.Join(dir, "template.raw")); err == nil {
		info.SizeBytes = uint64(fi.Size()) //nolint:gosec,G115
	}

	return info
}

// ListDomains returns all Xen domains from the Xen API.
func (a *Agent) ListDomains() ([]DomainInfo, error) {
	domains, err := a.xen.List()
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	result := make([]DomainInfo, len(domains))
	for i, d := range domains {
		result[i] = DomainInfo{
			Name:  d.Name,
			ID:    d.ID,
			MemMB: d.MemMB,
			VCPUs: d.VCPUs,
			State: d.State,
		}
	}
	return result, nil
}

// GetVM returns a single VM by name, merging meta.yaml with live Xen state.
func (a *Agent) GetVM(name string) (*VMInfo, error) {
	if err := validateVMName(name); err != nil {
		return nil, err
	}
	domainDir := filepath.Join(a.root, "vms", name)
	m, err := vm.LoadMeta(domainDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("VM %q not found", name)
		}
		return nil, fmt.Errorf("load VM %q: %w", name, err)
	}

	// Check if the domain is running.
	domains, err := a.xen.List()
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	running := make(map[string]xenctl.DomainInfo, len(domains))
	for _, d := range domains {
		running[d.Name] = d
	}

	info := a.metaToVMInfo(m, running)
	return &info, nil
}

// GetTemplate returns a single template by directory name.
func (a *Agent) GetTemplate(dir string) (*TemplateInfo, error) {
	if dir == "" || strings.Contains(dir, "/") || strings.Contains(dir, "\\") || strings.Contains(dir, "..") {
		return nil, fmt.Errorf("invalid template directory %q", dir)
	}
	templatesDir := filepath.Join(a.root, "templates")
	if _, err := os.Stat(filepath.Join(templatesDir, dir)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("template %q not found", dir)
		}
		return nil, fmt.Errorf("stat template %q: %w", dir, err)
	}

	refs := a.vmTemplateRefs()
	info := a.templateDirToInfo(dir, refs)
	return &info, nil
}

// GetConfig returns the loaded qlvm configuration.
func (a *Agent) GetConfig() (*ConfigInfo, error) {
	if a.cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	result := &ConfigInfo{
		Network: NetworkInfo{
			NIC:      a.cfg.Network.NIC,
			Gateway:  a.cfg.Network.Gateway,
			RouterIP: a.cfg.Network.RouterIP,
			Dom0IP:   a.cfg.Network.Dom0IP,
			DNS:      a.cfg.Network.DNS,
		},
		Domains:    a.domainConfigsToInfo(a.cfg.Domains),
		VM:         a.vmDefaultsToInfo(a.cfg.VM),
		Disposable: a.vmDefaultsToInfo(a.cfg.Disposable),
		Agent:      a.vmDefaultsToInfo(a.cfg.Agent),
		Firewall: FirewallInfo{
			Egress: FirewallEgressInfo{
				AllowDNS:      a.cfg.Firewall.Egress.AllowDNS,
				AllowHTTPS:    a.cfg.Firewall.Egress.AllowHTTPS,
				AllowSSHToVMs: a.cfg.Firewall.Egress.AllowSSHToVMs,
				AllowICMP:     a.cfg.Firewall.Egress.AllowICMP,
				ExtraRules:    a.cfg.Firewall.Egress.ExtraRules,
			},
		},
		Dom0: Dom0ConfigInfo{
			MemoryMB: a.cfg.Dom0.MemoryMB,
			VCPUs:    a.cfg.Dom0.VCPUs,
			Pin:      false,
			PinSet:   false,
		},
	}
	if a.cfg.Dom0.Pin != nil {
		result.Dom0.Pin = *a.cfg.Dom0.Pin
		result.Dom0.PinSet = true
	}
	return result, nil
}

// domainConfigsToInfo converts config domains to D-Bus-friendly info.
func (a *Agent) domainConfigsToInfo(domains []config.Domain) []DomainConfigInfo {
	result := make([]DomainConfigInfo, len(domains))
	for i, d := range domains {
		result[i] = DomainConfigInfo{
			Name:    d.Name,
			Subnet:  d.Subnet,
			Gateway: d.Gateway,
		}
	}
	return result
}

// vmDefaultsToInfo converts VM defaults to D-Bus-friendly info.
func (a *Agent) vmDefaultsToInfo(d config.VMDefaults) VMDefaultsInfo {
	return VMDefaultsInfo{
		User:     d.User,
		MemoryMB: d.MemoryMB,
		VCPUs:    d.VCPUs,
	}
}

// GetCoremap returns the current CPU core map. Returns an error if the
// coremap file doesn't exist or can't be parsed.
func (a *Agent) GetCoremap() (*CoremapInfo, error) {
	cm, err := coremap.Load(a.coremapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("coremap not found at %s", a.coremapPath)
		}
		return nil, fmt.Errorf("load coremap: %w", err)
	}

	assignments := make([]CoremapAssignmentInfo, len(cm.Assignments))
	for i, a := range cm.Assignments {
		assignments[i] = CoremapAssignmentInfo{
			VM:    a.VM,
			Cores: a.Cores,
		}
	}

	return &CoremapInfo{
		TotalCores:  cm.Topology.TotalCores,
		PCores:      cm.Topology.PCores,
		ECores:      cm.Topology.ECores,
		Dom0Cores:   cm.Dom0Cores,
		Assignments: assignments,
	}, nil
}

// --- VM lifecycle helpers ---

// stopPollInterval/stopMaxWait govern stopForced's post-shutdown poll loop.
var (
	stopPollInterval = 1500 * time.Millisecond
	stopMaxWait      = 60 * time.Second
)

// validateVMName rejects names that would be unsafe for Xen domain names
// or filesystem paths: empty, containing path separators or traversal
// sequences, or containing characters outside the allowed set.
// validNameChar reports whether r is allowed in a VM or template name.
func validNameChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
}

func validateVMName(name string) error {
	if name == "" {
		return errors.New("VM name must not be empty")
	}
	if len(name) > 128 {
		return errors.New("VM name must be at most 128 characters")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return errors.New("VM name must not contain path separators")
	}
	if name == "." || name == ".." || strings.Contains(name, "/..") || strings.Contains(name, "../") {
		return errors.New("VM name must not contain path traversal sequences")
	}
	for _, r := range name {
		if validNameChar(r) {
			continue
		}
		return fmt.Errorf("VM name must contain only alphanumeric characters, hyphens, underscores, or dots; got %q", name)
	}
	return nil
}

// vmDir returns the state directory for a VM under the qlvm root.
func (a *Agent) vmDir(name string) string {
	return filepath.Join(a.root, "vms", name)
}

// resolveTemplate maps a template reference — an exact template dir name,
// or a unique prefix of one — to the dir under <root>/templates.
func (a *Agent) resolveTemplate(name string) (string, error) {
	tplRoot := filepath.Join(a.root, "templates")
	entries, err := os.ReadDir(tplRoot)
	var have []string
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no templates under %s; bake one first", tplRoot)
	}
	if err != nil {
		return "", err
	}
	var match []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		have = append(have, e.Name())
		if name != "" && (e.Name() == name || strings.HasPrefix(e.Name(), name)) {
			match = append(match, e.Name())
		}
	}
	switch len(match) {
	case 0:
		return "", fmt.Errorf("no template dir matching %q; existing: %s", name, strings.Join(have, ", "))
	case 1:
		return filepath.Join(tplRoot, match[0]), nil
	default:
		return "", fmt.Errorf("template ref %q is ambiguous (matches: %s)", name, strings.Join(match, ", "))
	}
}

// loadRefTemplate resolves a template reference to a complete template dir
// and returns its META. Ostree templates require META + template.raw; agent
// templates require META + vmlinuz + initramfs.
func (a *Agent) loadRefTemplate(name string) (*template.Template, error) {
	dir, err := a.resolveTemplate(name)
	if err != nil {
		return nil, err
	}
	tpl, err := template.LoadMeta(dir)
	if err != nil {
		return nil, fmt.Errorf("template %s has no loadable META: %w; bake a complete one first", filepath.Base(dir), err)
	}
	// Agent templates carry vmlinuz/initramfs, not template.raw.
	if template.IsAgent(tpl) {
		for _, f := range []string{"vmlinuz", "initramfs"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				return nil, fmt.Errorf("template %s is incomplete (no %s); bake it first", filepath.Base(dir), f)
			}
		}
		return tpl, nil
	}
	if _, err := os.Stat(filepath.Join(dir, "template.raw")); err != nil {
		return nil, fmt.Errorf("template %s is incomplete (no template.raw); bake it first", filepath.Base(dir))
	}
	return tpl, nil
}

// parseCoresFlag parses a cores string into a slice of core IDs.
// Accepts comma-separated values and/or ranges (e.g. "6,7" or "6-8").
func parseCoresFlag(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var cores []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			startStr, endStr, ok := strings.Cut(part, "-")
			if !ok {
				return nil, fmt.Errorf("invalid cores %q: bad range %q", s, part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(startStr))
			if err != nil {
				return nil, fmt.Errorf("invalid cores %q: bad start %q", s, startStr)
			}
			end, err := strconv.Atoi(strings.TrimSpace(endStr))
			if err != nil {
				return nil, fmt.Errorf("invalid cores %q: bad end %q", s, endStr)
			}
			if start > end {
				return nil, fmt.Errorf("invalid cores %q: range start %d > end %d", s, start, end)
			}
			for c := start; c <= end; c++ {
				cores = append(cores, c)
			}
		} else {
			c, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid cores %q: bad core ID %q", s, part)
			}
			cores = append(cores, c)
		}
	}
	if len(cores) == 0 {
		return nil, fmt.Errorf("invalid cores %q: no cores specified", s)
	}
	return cores, nil
}

// parseMounts parses a list of "host:guest" mount strings into vm.Mounts.
func parseMounts(mounts []string) ([]vm.Mount, error) {
	var ms []vm.Mount
	for _, f := range mounts {
		host, guest, ok := strings.Cut(f, ":")
		if !ok || host == "" || guest == "" {
			return nil, fmt.Errorf("--mount %q: want host:guest", f)
		}
		ms = append(ms, vm.Mount{Host: host, Guest: guest})
	}
	return ms, nil
}

// --- VM lifecycle methods ---

// CreateVM prepares a new VM from a template. Returns the VM name.
func (a *Agent) CreateVM(opts *CreateVMOptions) (string, error) {
	if err := validateVMName(opts.Name); err != nil {
		return "", fmt.Errorf("create %s: %w", opts.Name, err)
	}
	if opts.Template == "" {
		return "", fmt.Errorf("create %s: template is required", opts.Name)
	}

	opID := newOpID("create")
	defer a.emitProgressDone(opID)
	a.emitProgress(opID, "starting")

	tpl, err := a.loadRefTemplate(opts.Template)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", opts.Name, err)
	}

	// Auto-detect agent type from template.
	typ := opts.Type
	if template.IsAgent(tpl) {
		if typ == "app" || typ == "disposable" {
			return "", fmt.Errorf("create %s: --type %s is incompatible with agent template %q (agent templates require --type agent)", opts.Name, typ, opts.Template)
		}
		typ = "agent"
	}

	a.emitProgress(opID, "parsing options")
	ms, err := parseMounts(opts.Mounts)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", opts.Name, err)
	}
	cores, err := parseCoresFlag(opts.Cores)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", opts.Name, err)
	}

	domain := opts.Domain
	if domain == "" && len(a.cfg.Domains) > 0 {
		domain = a.cfg.Domains[0].Name
	}

	memory := int(opts.MemoryMB) //nolint:gosec,G115
	vcpus := int(opts.VCPUs)     //nolint:gosec,G115

	a.emitProgress(opID, "creating VM")
	meta, err := vm.Create(context.Background(), vm.CreateDeps{
		OVN:         a.ovn,
		Tpl:         tpl,
		FS:          ostree.NewFS(nil),
		Reflink:     mounts.Reflink,
		Root:        a.root,
		FSType:      "xfs",
		CoremapPath: a.coremapPath,
		Dom0VCPUs:   a.cfg.Dom0.VCPUs,
	}, a.cfg, vm.Spec{
		Name:     opts.Name,
		Domain:   domain,
		Type:     typ,
		Image:    tpl.Image,
		MemoryMB: memory,
		VCPUs:    vcpus,
		Mounts:   ms,
		Cores:    cores,
	})
	if err != nil {
		return "", fmt.Errorf("create %s: %w", opts.Name, err)
	}

	a.emitProgress(opID, "created")
	return meta.Name, nil
}

// DeleteVM removes a VM (domain, OVN/OVS ports, state dir, ssh block).
func (a *Agent) DeleteVM(name string) error {
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}

	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("delete %s: home dir: %w", name, err)
	}
	if err := xenctl.Delete(ctx, a.xen, a.ovn, a.ovs, home, a.vmDir(name), name, a.coremapPath); err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}

// StartVM boots a prepared VM.
func (a *Agent) StartVM(name string) error {
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	dir := a.vmDir(name)
	m, err := vm.LoadMeta(dir)
	if err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	tpl, err := template.LoadByRef(a.root, m.Image, m.Digest)
	if err != nil {
		return fmt.Errorf("start %s: template: %w", name, err)
	}
	if err := xenctl.Start(context.Background(), a.xen, a.ovs, m, dir, tpl); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}

// StopVM gracefully stops a running VM.
func (a *Agent) StopVM(name string) error {
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	if err := a.stopForced(name); err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	return nil
}

// KillVM force-kills a running VM.
func (a *Agent) KillVM(name string) error {
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("kill %s: %w", name, err)
	}
	if err := a.xen.Destroy(name); err != nil {
		return fmt.Errorf("kill %s: %w", name, err)
	}
	return nil
}

// RestartVM restarts a VM (graceful stop, force-kill if it lingers, then start).
func (a *Agent) RestartVM(name string) error {
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	if err := a.stopForced(name); err != nil {
		return fmt.Errorf("restart %s: stop: %w", name, err)
	}
	if err := a.StartVM(name); err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	return nil
}

// stopForced stops a domain gracefully and force-kills if it refuses ACPI
// or lingers after a successful shutdown (zombie domains never answer
// ACPI, and Shutdown only queues the event — it never waits).
func (a *Agent) stopForced(name string) error {
	if err := a.xen.Shutdown(name); err != nil {
		if derr := a.xen.Destroy(name); derr != nil {
			return fmt.Errorf("stop %s: %w; destroy fallback: %w", name, err, derr)
		}
		return nil
	}
	// Shutdown is fire-and-forget: the ACPI event is queued and libxl
	// returns immediately, so success proves nothing. Poll until the
	// domain is actually gone; a domain that lingers (the in-guest-reboot
	// zombie, ---sr-, never answers ACPI) gets force-killed — Destroy is
	// the verified recovery.
	deadline := time.Now().Add(stopMaxWait)
	for {
		running, err := a.xen.Running(name)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !time.Now().Add(stopPollInterval).Before(deadline) {
			break
		}
		time.Sleep(stopPollInterval)
	}
	return a.xen.Destroy(name)
}

// --- Template methods ---

// podmanSocket is the podman REST socket (podman.socket unit).
const podmanSocket = "unix:///run/podman/podman.sock"

// fstype is the filesystem for both the template bake and the VM disk.
const fstype = "xfs"

// CreateTemplate bakes a new template from a container image. It pulls the
// image, reaps stale builder containers, and bakes the template via
// template.Ensure with the ostree bake callback. Returns the template
// directory name (basename). Long-running: emits Progress signals at each
// phase.
func (a *Agent) CreateTemplate(image string, force bool) (string, error) {
	if image == "" {
		return "", fmt.Errorf("image is required")
	}

	ctx := context.Background()
	opID := newOpID("template-create")
	defer a.emitProgressDone(opID)
	a.emitProgress(opID, "starting")

	pod, err := a.podmanFactory(ctx, podmanSocket)
	if err != nil {
		return "", fmt.Errorf("podman: %w", err)
	}

	digest, err := pod.Pull(ctx, image)
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", image, err)
	}

	dirName := template.DirOfRef(a.root, image, template.NormalizeDigest(digest))
	dirName = filepath.Base(dirName)

	// Check if template already exists.
	exists := false
	if _, err := os.Stat(filepath.Join(a.root, "templates", dirName)); err == nil {
		exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat template %s: %w", dirName, err)
	}

	if exists && !force {
		return "", fmt.Errorf("template %s already exists; re-bake with force", dirName)
	}

	// If forcing a re-bake, check that no VM references the template.
	if exists {
		refs := a.vmTemplateRefs()
		fullDir := filepath.Join(a.root, "templates", dirName)
		if vms := refs[fullDir]; len(vms) > 0 {
			return "", fmt.Errorf("template %s is referenced by VMs %s; delete those VMs first", dirName, strings.Join(vms, ", "))
		}
	}

	a.emitProgress(opID, "reaping stale resources")
	// Reap stale loop devices, leftover bake scratch mounts, and exited
	// image-builder containers (best effort — a killed prior bake can leave
	// any of these behind and they break the next bake).
	staleLoops(ctx, a.root, io.Discard)
	leftoverBakeMounts(io.Discard)
	if err := template.ReapBuilderContainers(ctx, pod, io.Discard); err != nil {
		// Warning only — the bake proceeds.
		_ = err
	}

	// Remove the existing template if re-baking.
	if exists {
		if err := os.RemoveAll(filepath.Join(a.root, "templates", dirName)); err != nil {
			return "", fmt.Errorf("remove existing template %s: %w", dirName, err)
		}
	}

	a.emitProgress(opID, "baking template")
	keys, err := sshAuthKeys()
	if err != nil {
		return "", err
	}

	_, err = template.Ensure(ctx, pod, template.EnsureOpts{
		Root:            a.root,
		Ref:             image,
		Log:             io.Discard,
		PrePulledDigest: digest,
		Bake: func(dir string) error {
			return a.bakeFn(ctx, dir, fstype, keys)
		},
	})
	if err != nil {
		return "", fmt.Errorf("bake template %s: %w", dirName, err)
	}

	a.emitProgress(opID, "done")
	return dirName, nil
}

// DeleteTemplate removes a template directory by name. Refuses if any VM
// references the template.
func (a *Agent) DeleteTemplate(dir string) error {
	if dir == "" || strings.Contains(dir, "/") || strings.Contains(dir, "\\") || strings.Contains(dir, "..") {
		return fmt.Errorf("invalid template directory %q", dir)
	}

	fullDir := filepath.Join(a.root, "templates", dir)
	if _, err := os.Stat(fullDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("template %q not found", dir)
		}
		return fmt.Errorf("stat template %q: %w", dir, err)
	}

	// Check if any VM references this template.
	refs := a.vmTemplateRefs()
	if vms := refs[fullDir]; len(vms) > 0 {
		return fmt.Errorf("template %s is referenced by VMs %s; delete those VMs first", dir, strings.Join(vms, ", "))
	}

	if err := os.RemoveAll(fullDir); err != nil {
		return fmt.Errorf("remove template %s: %w", dir, err)
	}
	return nil
}

// loopLineRe matches `losetup -a` lines. The backing path may carry a
// " (deleted)" suffix; group 2 captures the path without it.
var loopLineRe = regexp.MustCompile(`^(/\S+)(?: \([^)]*\))?:.*\(([^()]+)(?: \(deleted\))?\)\s*$`)

// parseLoopLines returns the loop devices in `losetup -a` output whose
// backing file is under root and no longer exists (stale loops).
func parseLoopLines(output, root string) []string {
	prefix := root + string(filepath.Separator)
	var out []string
	for _, line := range strings.Split(output, "\n") {
		m := loopLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := strings.TrimSpace(m[2])
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			out = append(out, m[1])
		}
	}
	return out
}

// staleLoops detaches loop devices under root whose backing file was deleted
// (a killed create leaves them attached, and they break template re-attach).
func staleLoops(ctx context.Context, root string, out io.Writer) {
	b, err := exec.CommandContext(ctx, "losetup", "-a").Output() // #nosec G204 -- fixed binary, fixed args
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: losetup: %v\n", err)
		return
	}
	for _, dev := range parseLoopLines(string(b), root) {
		if err := exec.CommandContext(ctx, "losetup", "-d", dev).Run(); err != nil { // #nosec G204 -- dev is a parsed /dev/loopN name
			_, _ = fmt.Fprintf(out, "warning: losetup -d %s: %v\n", dev, err)
		} else {
			_, _ = fmt.Fprintf(out, "detached stale loop %s\n", dev)
		}
	}
}

// leftoverBakeMounts unmounts and removes <tmp>/qlvm-ostree-* scratch dirs a
// killed bake leaves mounted (ostree.mountTarget).
func leftoverBakeMounts(out io.Writer) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ostree.MountPrefix) {
			continue
		}
		dir := filepath.Join(os.TempDir(), e.Name())
		if err := unix.Unmount(dir, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
			_, _ = fmt.Fprintf(out, "warning: unmount %s: %v\n", dir, err)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			_, _ = fmt.Fprintf(out, "warning: remove %s: %v\n", dir, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "removed stale bake mount %s\n", dir)
	}
}

// wheelGroupMembersFn and userLookupFn are the /etc-group and /etc/passwd
// seams behind appSyncHomes; tests stub them.
var (
	wheelGroupMembersFn = wheelGroupMembers
	userLookupFn        = userHomeDir
)

func userHomeDir(name string) (string, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// wheelGroupMembers returns the supplementary members of the wheel group by
// parsing /etc/group. os/user.Group has no member list field, so we parse
// the file directly (format: name:password:GID:member1,member2,...).
func wheelGroupMembers() []string {
	b, err := os.ReadFile("/etc/group") // #nosec G304 -- fixed system file
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, ":", 4)
		if len(fields) != 4 {
			continue
		}
		if fields[0] != "wheel" {
			continue
		}
		if fields[3] == "" {
			return nil
		}
		return strings.Split(fields[3], ",")
	}
	return nil
}

// sshAuthKeys collects the id_*.pub identities of root, SUDO_USER (if set),
// and all users in the wheel group (desktop users). The daemon runs as root
// under systemd where SUDO_USER is unset, so wheel membership is the
// heuristic for finding desktop users whose keys are needed for qlvm vm run
// SSH connections. Baked into every VM's authorized_keys; empty result is a
// hard error.
func sshAuthKeys() (string, error) {
	var homes []string
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			homes = append(homes, u.HomeDir)
		}
	}
	// Collect homes of all users in the wheel group (desktop users).
	for _, mem := range wheelGroupMembers() {
		if u, err := user.Lookup(mem); err == nil && u.HomeDir != "" {
			homes = append(homes, u.HomeDir)
		}
	}
	// Deduplicate homes.
	seenHomes := map[string]bool{}
	var uniqueHomes []string
	for _, h := range homes {
		if !seenHomes[h] {
			seenHomes[h] = true
			uniqueHomes = append(uniqueHomes, h)
		}
	}
	seen := map[string]bool{}
	var keys []string
	for _, h := range uniqueHomes {
		for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
			b, err := os.ReadFile(filepath.Join(h, ".ssh", n)) // #nosec G304 G703 -- homes from $HOME/passwd, fixed file names
			if err != nil {
				continue
			}
			k := strings.TrimSpace(string(b))
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("no ssh identity: run `ssh-keygen -t ed25519` as the desktop user before baking a template (qlvm vm run sshes into VMs with it)")
	}
	return strings.Join(keys, "\n") + "\n", nil
}

// --- Auxiliary methods ---

// provisionDefaultDir is where dom0 looks for the layered provision
// content (base/ + per-vm/), next to the qlvm config tree.
const provisionDefaultDir = "/etc/qlvm/provision"

// ProvisionVM syncs layered dotfiles into a running VM's home over sftp.
// Dom0 provisioning is external; this provisions the guest.
func (a *Agent) ProvisionVM(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := validateVMName(name); err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	vmDir := a.vmDir(name)
	if _, err := vm.LoadMeta(vmDir); err != nil {
		return fmt.Errorf("provision %s: %w", name, err)
	}

	home := a.homeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
	}

	opID := newOpID("provision")
	defer a.emitProgressDone(opID)
	a.emitProgress(opID, "starting")
	a.emitProgress(opID, "Syncing dotfiles into "+name)

	var r provisioner.Runner
	if a.provisionRunner != nil {
		r = a.provisionRunner
	} else {
		r = provisioner.NewSSHRunner(home)
	}
	dir := provisionDefaultDir
	if a.provisionDir != "" {
		dir = a.provisionDir
	}
	if err := provisioner.Provision(ctx, r, home, name, dir); err != nil {
		return fmt.Errorf("provision %s: %w", name, err)
	}
	return nil
}

// kernelSyncer is the ssh seam tests fake: a remote command plus file
// fetches against the running VM.
type kernelSyncer interface {
	Run(ctx context.Context, cmd string) (string, error)
	FetchFile(ctx context.Context, path string) (io.ReadCloser, error)
}

// sshKernelSyncer adapts a live *ssh.Client to kernelSyncer.
type sshKernelSyncer struct{ c *ssh.Client }

func (s sshKernelSyncer) Run(ctx context.Context, cmd string) (string, error) {
	return sshx.Run(ctx, s.c, cmd)
}

func (s sshKernelSyncer) FetchFile(ctx context.Context, path string) (io.ReadCloser, error) {
	return sshx.FetchFile(ctx, s.c, path)
}

// SyncKernel copies the VM's current kernel + initramfs from its /boot
// into the VM's state dir so a restart picks up the new kernel.
func (a *Agent) SyncKernel(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := validateVMName(name); err != nil {
		return fmt.Errorf("sync-kernel: %w", err)
	}
	vmDir := a.vmDir(name)
	m, err := vm.LoadMeta(vmDir)
	if err != nil {
		return fmt.Errorf("sync-kernel %s: %w", name, err)
	}

	home := a.homeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return err
		}
	}

	opID := newOpID("sync-kernel")
	defer a.emitProgressDone(opID)
	a.emitProgress(opID, "starting")

	var host kernelSyncer
	if a.kernelHost != nil {
		host = a.kernelHost
	} else {
		// Connect directly to the VM's IP: disposable VMs have no ssh-config
		// alias, and a running VM may have a fresh host key.
		var connectFunc func(ctx context.Context, home, host, user string) (*ssh.Client, error)
		if a.sshConnectFunc != nil {
			connectFunc = a.sshConnectFunc
		} else {
			connectFunc = sshx.Connect
		}
		c, err := connectFunc(ctx, home, m.IP, vm.SSHUser)
		if err != nil {
			return fmt.Errorf("sync-kernel %s: %w", name, err)
		}
		defer func() { _ = c.Close() }()
		host = sshKernelSyncer{c: c}
	}

	a.emitProgress(opID, "Querying kernel version on "+name)
	a.emitProgress(opID, "Fetching kernel files from "+name)
	ver, err := syncKernelFiles(ctx, host, vmDir)
	if err != nil {
		return fmt.Errorf("sync-kernel %s: %w", name, err)
	}

	a.emitProgress(opID, "Synced kernel "+ver+" to "+vmDir)
	return nil
}

// syncKernelFiles copies the VM's current kernel and initramfs from its
// /boot into dir under both their exact fetched names and the unversioned
// aliases (vmlinuz/initramfs) that the VM's domain config boots.
// Everything is fetched before anything is written: a partial copy (new
// vmlinuz beside the old initramfs) would leave the VM unbootable.
func syncKernelFiles(ctx context.Context, host kernelSyncer, dir string) (string, error) {
	raw, err := host.Run(ctx, "rpm -q kernel-core --last | head -1")
	if err != nil {
		return "", fmt.Errorf("query vm kernel: %w", err)
	}
	ver, err := sshx.KernelVersion(raw)
	if err != nil {
		return "", err
	}
	files := []struct{ remote, local, alias string }{
		{"/boot/vmlinuz-" + ver, "vmlinuz-" + ver, "vmlinuz"},
		{"/boot/initramfs-" + ver + ".img", "initramfs-" + ver + ".img", "initramfs"},
	}
	var bodies [][]byte
	for _, f := range files {
		rc, err := host.FetchFile(ctx, f.remote)
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, err)
		}
		body, rerr := io.ReadAll(rc)
		cerr := rc.Close()
		if rerr != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, rerr)
		}
		if cerr != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, cerr)
		}
		bodies = append(bodies, body)
	}
	for i, f := range files {
		for _, name := range []string{f.local, f.alias} {
			if err := os.WriteFile(filepath.Join(dir, name), bodies[i], 0o600); err != nil {
				return "", err
			}
		}
	}
	return ver, nil
}

// --- Progress ---

// emitProgress sends a Progress signal for operation id with the given message.
// Unexported so it is not visible as a callable D-Bus method.
func (a *Agent) emitProgress(id, message string) {
	a.mu.Lock()
	if t, ok := a.progress[id]; ok {
		t.Message = message
	} else {
		a.progress[id] = &ProgressTracker{ID: id, Message: message}
	}
	a.mu.Unlock()

	if a.progressHook != nil {
		a.progressHook(id, message, false)
	}

	if a.conn != nil {
		_ = a.conn.Emit(dbus.ObjectPath(ObjectPath), Iface+".Progress", id, message)
	}
}

// emitProgressDone sends a ProgressDone signal for operation id.
// Unexported so it is not visible as a callable D-Bus method.
func (a *Agent) emitProgressDone(id string) {
	a.mu.Lock()
	if t, ok := a.progress[id]; ok {
		t.Done = true
	}
	delete(a.progress, id) // free memory after completion
	a.mu.Unlock()

	if a.progressHook != nil {
		a.progressHook(id, "", true)
	}

	if a.conn != nil {
		_ = a.conn.Emit(dbus.ObjectPath(ObjectPath), Iface+".ProgressDone", id)
	}
}

// --- App methods (desktop cache + per-VM whitelist) ---

// AppInfo is one cached app row for the GUI's Apps tab: the full cached set
// plus per-app whitelist state (absent whitelist file = all visible).
type AppInfo struct {
	Name        string
	Icon        string
	Whitelisted bool
}

// appCacheDir is the per-VM desktop cache under the state root.
func (a *Agent) appCacheDir() string {
	return filepath.Join(a.root, "desktop-cache")
}

// appSyncHomes returns the SSH home candidates for SyncApps, in order: the
// daemon's own home (root under systemd), then each wheel-group member's
// home in sorted order. The first candidate that connects wins.
func (a *Agent) appSyncHomes() []string {
	var homes []string
	home := a.homeDir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home != "" {
		homes = append(homes, home)
	}
	members := wheelGroupMembersFn()
	sort.Strings(members)
	seen := map[string]bool{home: true}
	for _, mem := range members {
		h, err := userLookupFn(mem)
		if err != nil || h == "" || seen[h] {
			continue
		}
		seen[h] = true
		homes = append(homes, h)
	}
	return homes
}

// defaultAppSyncFetch pulls one VM's concatenated desktop stream over SSH
// with the identity of home and splits it into per-app entries.
func defaultAppSyncFetch(ctx context.Context, home, host string) (map[string]string, error) {
	c, err := sshx.Connect(ctx, home, host, vm.SSHUser)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	rc, err := sshx.FetchFile(ctx, c, apps.DesktopGlob)
	if err != nil {
		return nil, err
	}
	data, rerr := io.ReadAll(rc)
	cerr := rc.Close()
	if rerr != nil {
		return nil, rerr
	}
	if cerr != nil {
		return nil, cerr
	}
	return apps.SplitDesktops(data)
}

// ListApps returns the VM's full cached app set with per-app whitelist
// state. No cache dir (never synced) yields an empty list, not an error.
func (a *Agent) ListApps(name string) (out []AppInfo, err error) {
	defer func() {
		if out == nil {
			out = []AppInfo{}
		}
	}()
	all, err := apps.CachedApps(a.appCacheDir(), name)
	if err != nil {
		return nil, err
	}
	wl, err := apps.ReadWhitelist(a.appCacheDir(), name)
	if err != nil {
		return nil, err
	}
	active := wl != nil
	allowed := make(map[string]bool, len(wl))
	for _, n := range wl {
		allowed[strings.ToLower(n)] = true
	}
	for _, e := range all {
		out = append(out, AppInfo{
			Name:        e.Name,
			Icon:        e.Icon,
			Whitelisted: !active || allowed[strings.ToLower(e.Base)],
		})
	}
	return out, nil
}

// SetAppWhitelist replaces the VM's whitelist (sorted, deduped basenames);
// an empty list deletes the file, which is the "all apps visible" state.
func (a *Agent) SetAppWhitelist(name string, entries []string) error {
	return apps.WriteWhitelist(a.appCacheDir(), name, entries)
}

// SyncApps refreshes the VM's desktop cache from the guest, trying the SSH
// identity of each home in appSyncHomes in order (root home, then wheel
// members sorted) and using the first that connects. The existing
// per-VM whitelist file survives the swap.
func (a *Agent) SyncApps(name string) (int, error) {
	m, err := vm.LoadMeta(a.vmDir(name))
	if err != nil {
		return 0, fmt.Errorf("sync apps %s: %w", name, err)
	}
	fetch := a.appSyncFetch
	if fetch == nil {
		fetch = defaultAppSyncFetch
	}
	ctx := context.Background()
	var tried []string
	var lastErr error
	for _, home := range a.appSyncHomes() {
		tried = append(tried, home)
		entries, ferr := fetch(ctx, home, m.IP)
		if ferr == nil {
			n, werr := apps.SyncCache(a.appCacheDir(), name, entries)
			if werr != nil {
				return 0, fmt.Errorf("sync apps %s: %w", name, werr)
			}
			return n, nil
		}
		lastErr = ferr
	}
	if lastErr == nil {
		lastErr = errors.New("no home candidates")
	}
	return 0, fmt.Errorf("sync apps %s: no working ssh identity (tried %s): %w", name, strings.Join(tried, ", "), lastErr)
}
