// Package agent implements the privileged qlvm D-Bus daemon (qlvm-agent).
// The daemon runs as root under systemd, exposes io.qlvm.Agent on the
// system bus, and delegates to qlvm's internal packages. The GUI (running
// as the user) calls the daemon over D-Bus.
package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovs"
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

// New creates an Agent with the given control planes.
func New(x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath string) *Agent {
	return &Agent{
		xen:         x,
		ovn:         o,
		ovs:         v,
		cfg:         cfg,
		coremapPath: coremapPath,
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

// ListVMs returns all VMs (running from Xen, stopped from meta.yaml).
func (a *Agent) ListVMs() ([]VMInfo, error) {
	return nil, fmt.Errorf("ListVMs: not implemented (Task 4)")
}

// ListTemplates returns all templates.
func (a *Agent) ListTemplates() ([]TemplateInfo, error) {
	return nil, fmt.Errorf("ListTemplates: not implemented (Task 4)")
}

// ListDomains returns all Xen domains.
func (a *Agent) ListDomains() ([]DomainInfo, error) {
	return nil, fmt.Errorf("ListDomains: not implemented (Task 4)")
}

// GetVM returns a single VM by name.
func (a *Agent) GetVM(name string) (*VMInfo, error) {
	return nil, fmt.Errorf("GetVM: not implemented (Task 4)")
}

// GetTemplate returns a single template by directory.
func (a *Agent) GetTemplate(dir string) (*TemplateInfo, error) {
	return nil, fmt.Errorf("GetTemplate: not implemented (Task 4)")
}

// GetConfig returns the current configuration.
func (a *Agent) GetConfig() (*ConfigInfo, error) {
	return nil, fmt.Errorf("GetConfig: not implemented (Task 4)")
}

// GetCoremap returns the current CPU core map.
func (a *Agent) GetCoremap() (*CoremapInfo, error) {
	return nil, fmt.Errorf("GetCoremap: not implemented (Task 4)")
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
