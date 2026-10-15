package gui

import (
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/vm"
)

func TestVMListViewFilteredVMs(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)

	model.SetVMs(map[string]*vm.Meta{
		"web":      {Name: "web", Type: "app", MemoryMB: 1024, VCPUs: 1},
		"database": {Name: "database", Type: "app", MemoryMB: 2048, VCPUs: 2},
		"agent1":   {Name: "agent1", Type: "agent", MemoryMB: 512, VCPUs: 1},
	})
	model.SetDomainStates(map[string]string{
		"web": "running",
	})

	view := NewVMListView(model)

	// No filter: all 3 VMs.
	entries := view.filteredVMs()
	if len(entries) != 3 {
		t.Fatalf("expected 3 VMs, got %d", len(entries))
	}

	// Sorted by name.
	if entries[0].Name != "agent1" || entries[1].Name != "database" || entries[2].Name != "web" {
		t.Errorf("unexpected sort order: %v", entries)
	}

	// Filter: only "web".
	model.SetFilter("web")
	entries = view.filteredVMs()
	if len(entries) != 1 {
		t.Fatalf("expected 1 VM after filter, got %d", len(entries))
	}
	if entries[0].Name != "web" {
		t.Errorf("expected 'web', got %q", entries[0].Name)
	}

	// State: web is running, others stopped.
	model.SetFilter("")
	entries = view.filteredVMs()
	if entries[2].State != "running" {
		t.Errorf("expected web to be running, got %q", entries[2].State)
	}
	if entries[0].State != "stopped" {
		t.Errorf("expected agent1 to be stopped, got %q", entries[0].State)
	}
}

func TestVMListViewSelectVM(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)

	model.SetVMs(map[string]*vm.Meta{
		"web": {Name: "web", Type: "app", MemoryMB: 1024, VCPUs: 1},
	})
	model.SetDomainStates(map[string]string{})

	view := NewVMListView(model)

	// Simulate selecting the first item.
	view.List.OnSelected(0)

	if model.SelectedVMName() != "web" {
		t.Errorf("expected selected VM 'web', got %q", model.SelectedVMName())
	}
}
