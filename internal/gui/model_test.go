package gui

import (
	"testing"
	"time"
)

func TestNewAppModelDefaults(t *testing.T) {
	m := NewAppModel(2*time.Second, 30*time.Second)

	if m.VMs == nil {
		t.Fatal("VMs map is nil")
	}
	if m.DomainStates == nil {
		t.Fatal("DomainStates map is nil")
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

	vms := map[string]*struct{}{} // placeholder, actual test uses vm.Meta
	// Just verify the setter works without panicking
	m.SetVMs(nil)
}
