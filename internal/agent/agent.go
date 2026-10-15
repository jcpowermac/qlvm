// Package agent implements the privileged qlvm D-Bus daemon (qlvm-agent).
// The daemon runs as root under systemd, exposes io.qlvm.Agent on the
// system bus, and delegates to qlvm's internal packages. The GUI (running
// as the user) calls the daemon over D-Bus.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/ovs"
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
	Pin      *bool
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
	root        string // qlvm state root (/var/lib/qvm)

	// Progress signal state (mutex-protected).
	mu       sync.Mutex
	progress map[string]*ProgressTracker

	// conn is the D-Bus connection (set by Run).
	conn *dbus.Conn
}

// ProgressTracker tracks a long-running operation for signal emission.
type ProgressTracker struct {
	ID      string
	Message string
	Done    bool
}

// New creates an Agent with the given control planes. root is the qlvm
// state root (e.g. /var/lib/qvm) where VM and template state is stored.
func New(x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath, root string) *Agent {
	return &Agent{
		xen:         x,
		ovn:         o,
		ovs:         v,
		cfg:         cfg,
		coremapPath: coremapPath,
		root:        root,
		progress:    make(map[string]*ProgressTracker),
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

	if err := conn.Export(a, dbus.ObjectPath(ObjectPath), Iface); err != nil {
		_ = conn.Close()
		a.conn = nil
		return "", fmt.Errorf("export agent: %w", err)
	}

	if _, err := conn.RequestName(ServiceName, dbus.NameFlagDoNotQueue|dbus.NameFlagReplaceExisting); err != nil {
		_ = conn.Export(nil, dbus.ObjectPath(ObjectPath), Iface)
		_ = conn.Close()
		a.conn = nil
		return "", fmt.Errorf("request name %s: %w", ServiceName, err)
	}

	// Block until ctx is cancelled or the connection closes. conn.Context()
	// is cancelled by godbus when the connection is closed or the peer
	// disconnects, so this is event-driven rather than polling.
	select {
	case <-ctx.Done():
	case <-conn.Context().Done():
	}

	_ = conn.Export(nil, dbus.ObjectPath(ObjectPath), Iface)
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
				Name:    d.Name,
				State:   d.State,
				MemoryMB: d.MemMB,
				VCPUs:   d.VCPUs,
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
		MemoryMB:  uint64(m.MemoryMB),
		VCPUs:     uint8(m.VCPUs),
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
		info.SizeBytes = uint64(fi.Size())
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
	return &ConfigInfo{
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
			Pin:      a.cfg.Dom0.Pin,
		},
	}, nil
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

// --- VM lifecycle methods ---

// CreateVM prepares a new VM from a template. Returns the VM name.
func (a *Agent) CreateVM(opts *CreateVMOptions) (string, error) {
	return "", fmt.Errorf("CreateVM: not implemented (Task 5)")
}

// DeleteVM removes a VM (domain, OVN/OVS ports, state dir, ssh block).
func (a *Agent) DeleteVM(name string) error {
	return fmt.Errorf("DeleteVM: not implemented (Task 5)")
}

// StartVM boots a prepared VM.
func (a *Agent) StartVM(name string) error {
	return fmt.Errorf("StartVM: not implemented (Task 5)")
}

// StopVM gracefully stops a running VM.
func (a *Agent) StopVM(name string) error {
	return fmt.Errorf("StopVM: not implemented (Task 5)")
}

// KillVM force-kills a running VM.
func (a *Agent) KillVM(name string) error {
	return fmt.Errorf("KillVM: not implemented (Task 5)")
}

// RestartVM restarts a VM (graceful stop, force-kill if it lingers, then start).
func (a *Agent) RestartVM(name string) error {
	return fmt.Errorf("RestartVM: not implemented (Task 5)")
}

// --- Template methods ---

// CreateTemplate bakes a new template from a container image. Returns the template directory.
func (a *Agent) CreateTemplate(image string, force bool) (string, error) {
	return "", fmt.Errorf("CreateTemplate: not implemented (Task 6)")
}

// DeleteTemplate removes a template directory.
func (a *Agent) DeleteTemplate(dir string) error {
	return fmt.Errorf("DeleteTemplate: not implemented (Task 6)")
}

// --- Auxiliary methods ---

// ProvisionVM provisions a running VM (installs waypipe, configures ssh).
func (a *Agent) ProvisionVM(name string) error {
	return fmt.Errorf("ProvisionVM: not implemented (Task 7)")
}

// SyncKernel updates a VM's kernel from the template.
func (a *Agent) SyncKernel(name string) error {
	return fmt.Errorf("SyncKernel: not implemented (Task 8)")
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
	a.mu.Unlock()

	if a.conn != nil {
		_ = a.conn.Emit(dbus.ObjectPath(ObjectPath), Iface+".ProgressDone", id)
	}
}
