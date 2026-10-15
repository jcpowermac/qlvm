package gui

import (
	"sync"
	"time"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// AppModel holds the shared state for the GUI. It's the single source of
// truth for the UI, updated by background operations and read by views.
type AppModel struct {
	mu sync.RWMutex

	// Config loaded from /etc/qvm/qlvm.yaml.
	Config *config.Config

	// VMs: name → metadata.
	VMs map[string]*vm.Meta

	// DomainStates: name → Xen domain state string (from libxl).
	DomainStates map[string]string

	// Templates: directory → template metadata.
	Templates map[string]*template.Template

	// SelectedVM: the VM currently selected in the list.
	SelectedVM string

	// SelectedTemplate: the template currently selected in the list.
	SelectedTemplate string

	// CurrentTab: "vms" or "templates".
	CurrentTab string

	// Filter: search text for the VM/template list.
	Filter string

	// Refresh interval for running VMs.
	RunningInterval time.Duration

	// Refresh interval for stopped VMs.
	StoppedInterval time.Duration

	// logFn is the callback for operation output.
	logFn func(string)
}

// NewAppModel creates an AppModel with default refresh intervals.
func NewAppModel(runningInterval, stoppedInterval time.Duration) *AppModel {
	return &AppModel{
		VMs:             make(map[string]*vm.Meta),
		DomainStates:    make(map[string]string),
		Templates:       make(map[string]*template.Template),
		CurrentTab:      "vms",
		RunningInterval: runningInterval,
		StoppedInterval: stoppedInterval,
	}
}

// SetConfig updates the config.
func (m *AppModel) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Config = cfg
}

// GetConfig returns the current config.
func (m *AppModel) GetConfig() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Config
}

// SetVMs replaces the VM map.
func (m *AppModel) SetVMs(vms map[string]*vm.Meta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.VMs = vms
}

// GetVMs returns a copy of the VM map.
func (m *AppModel) GetVMs() map[string]*vm.Meta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*vm.Meta, len(m.VMs))
	for k, v := range m.VMs {
		result[k] = v
	}
	return result
}

// SetDomainStates replaces the domain state map.
func (m *AppModel) SetDomainStates(states map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DomainStates = states
}

// GetDomainStates returns a copy of the domain state map.
func (m *AppModel) GetDomainStates() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.DomainStates))
	for k, v := range m.DomainStates {
		result[k] = v
	}
	return result
}

// SetTemplates replaces the template map.
func (m *AppModel) SetTemplates(tpls map[string]*template.Template) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Templates = tpls
}

// GetTemplates returns a copy of the template map.
func (m *AppModel) GetTemplates() map[string]*template.Template {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*template.Template, len(m.Templates))
	for k, v := range m.Templates {
		result[k] = v
	}
	return result
}

// SelectVM sets the selected VM.
func (m *AppModel) SelectVM(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SelectedVM = name
}

// SelectedVMName returns the selected VM name.
func (m *AppModel) SelectedVMName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.SelectedVM
}

// SelectTemplate sets the selected template.
func (m *AppModel) SelectTemplate(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SelectedTemplate = dir
}

// SelectedTemplateDir returns the selected template directory.
func (m *AppModel) SelectedTemplateDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.SelectedTemplate
}

// SetTab sets the current tab.
func (m *AppModel) SetTab(tab string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CurrentTab = tab
}

// CurrentTabName returns the current tab.
func (m *AppModel) CurrentTabName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.CurrentTab
}

// SetFilter sets the search filter.
func (m *AppModel) SetFilter(filter string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Filter = filter
}

// FilterText returns the current filter.
func (m *AppModel) FilterText() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Filter
}

// Log sends a message to the log callback.
func (m *AppModel) Log(msg string) {
	m.mu.RLock()
	logFn := m.logFn
	m.mu.RUnlock()
	if logFn != nil {
		logFn(msg)
	}
}

// SetLogCallback sets the log callback.
func (m *AppModel) SetLogCallback(fn func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logFn = fn
}
