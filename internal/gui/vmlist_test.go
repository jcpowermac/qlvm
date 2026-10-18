package gui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func TestVMTableFilteredVMs(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)

	model.SetVMs(map[string]*agent.VMInfo{
		"web":      {Name: "web", Type: "app", State: "running", MemoryMB: 1024, VCPUs: 1},
		"database": {Name: "database", Type: "app", State: "stopped", MemoryMB: 2048, VCPUs: 2},
		"agent1":   {Name: "agent1", Type: "agent", State: "stopped", MemoryMB: 512, VCPUs: 1},
	})

	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	w := fyneApp.NewWindow("test")
	defer w.Close()

	table := newVMTable(model, w, nil, nil, nil)

	// No filter: all 3 VMs.
	entries := table.filteredVMs()
	if len(entries) != 3 {
		t.Fatalf("expected 3 VMs, got %d", len(entries))
	}

	// Sorted by name.
	if entries[0].Name != "agent1" || entries[1].Name != "database" || entries[2].Name != "web" {
		t.Errorf("unexpected sort order: %v", entries)
	}

	// Filter: only "web".
	model.SetFilter("web")
	entries = table.filteredVMs()
	if len(entries) != 1 {
		t.Fatalf("expected 1 VM after filter, got %d", len(entries))
	}
	if entries[0].Name != "web" {
		t.Errorf("expected 'web', got %q", entries[0].Name)
	}

	// State: web is running, others stopped.
	model.SetFilter("")
	entries = table.filteredVMs()
	if entries[2].State != "running" {
		t.Errorf("expected web to be running, got %q", entries[2].State)
	}
	if entries[0].State != "stopped" {
		t.Errorf("expected agent1 to be stopped, got %q", entries[0].State)
	}
}

func TestActionsStart(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)
	fake := newFakeClient()

	model.SetVMs(map[string]*agent.VMInfo{
		"web": {Name: "web", Type: "app", State: "stopped", MemoryMB: 1024, VCPUs: 1},
	})
	model.SelectVM("web")

	actions := NewActions(model, fake, func(string) {})

	if err := actions.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.startCalls) != 1 || fake.startCalls[0] != "web" {
		t.Errorf("expected StartVM called with 'web', got %v", fake.startCalls)
	}
}

func TestActionsDelete(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)
	fake := newFakeClient()

	model.SetVMs(map[string]*agent.VMInfo{
		"web": {Name: "web", Type: "app", State: "stopped", MemoryMB: 1024, VCPUs: 1},
	})
	model.SelectVM("web")

	actions := NewActions(model, fake, func(string) {})

	if err := actions.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// VM should be removed from model.
	if len(model.GetVMs()) != 0 {
		t.Errorf("expected VM removed from model, got %d", len(model.GetVMs()))
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleteCalls) != 1 || fake.deleteCalls[0] != "web" {
		t.Errorf("expected DeleteVM called with 'web', got %v", fake.deleteCalls)
	}
}
