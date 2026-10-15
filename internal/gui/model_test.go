package gui

import (
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func TestNewAppModelDefaults(t *testing.T) {
	m := NewAppModel(2*time.Second, 30*time.Second)

	if m.VMs == nil {
		t.Fatal("VMs map is nil")
	}
	if m.Templates == nil {
		t.Fatal("Templates map is nil")
	}
	if m.CurrentTab != "vms" {
		t.Errorf("expected default tab 'vms', got %q", m.CurrentTab)
	}
	if m.RunningInterval != 2*time.Second {
		t.Errorf("expected running interval 2s, got %v", m.RunningInterval)
	}
	if m.StoppedInterval != 30*time.Second {
		t.Errorf("expected stopped interval 30s, got %v", m.StoppedInterval)
	}
}

func TestModelSelectVM(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	m.SelectVM("test-vm")
	if m.SelectedVMName() != "test-vm" {
		t.Errorf("expected selected VM 'test-vm', got %q", m.SelectedVMName())
	}
}

func TestModelSelectTemplate(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	m.SelectTemplate("/var/lib/qvm/templates/test")
	if m.SelectedTemplateDir() != "/var/lib/qvm/templates/test" {
		t.Errorf("expected selected template dir, got %q", m.SelectedTemplateDir())
	}
}

func TestModelSetTab(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	m.SetTab("templates")
	if m.CurrentTabName() != "templates" {
		t.Errorf("expected tab 'templates', got %q", m.CurrentTabName())
	}
}

func TestModelSetFilter(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	m.SetFilter("test")
	if m.FilterText() != "test" {
		t.Errorf("expected filter 'test', got %q", m.FilterText())
	}
}

func TestModelSetVMs(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	vms := map[string]*agent.VMInfo{
		"test": {Name: "test", Type: "app", State: "running", MemoryMB: 1024, VCPUs: 1},
	}
	m.SetVMs(vms)

	got := m.GetVMs()
	if len(got) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(got))
	}
	if got["test"].Name != "test" {
		t.Errorf("expected VM name 'test', got %q", got["test"].Name)
	}
}

func TestModelSetTemplates(t *testing.T) {
	m := NewAppModel(time.Second, time.Second)

	tpls := map[string]*agent.TemplateInfo{
		"test": {Dir: "test", Image: "quay.io/fedora/fedora:44", Digest: "sha256:abc123"},
	}
	m.SetTemplates(tpls)

	got := m.GetTemplates()
	if len(got) != 1 {
		t.Fatalf("expected 1 template, got %d", len(got))
	}
	if got["test"].Image != "quay.io/fedora/fedora:44" {
		t.Errorf("expected template image, got %q", got["test"].Image)
	}
}
