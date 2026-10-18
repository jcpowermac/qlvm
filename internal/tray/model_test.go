package tray

import (
	"testing"

	"github.com/jcpowermac/qlvm/internal/apps"
)

// fakeVisible returns a fixed set of apps for a fixed VM, empty otherwise.
func fakeVisible(vm string, appSet ...apps.AppEntry) func(string) ([]apps.AppEntry, error) {
	return func(name string) ([]apps.AppEntry, error) {
		if name == vm {
			return appSet, nil
		}
		return nil, nil
	}
}

func TestComputeMenuShape(t *testing.T) {
	states := []VMState{{Name: "web", State: "running"}, {Name: "db", State: "stopped"}}
	visible := fakeVisible("web",
		apps.AppEntry{Name: "Firefox", Base: "firefox.desktop"},
		apps.AppEntry{Name: "Code", Base: "code.desktop"},
	)
	tree := ComputeMenu(states, visible)

	if tree.Header != "qlvm" {
		t.Errorf("header = %q, want qlvm", tree.Header)
	}
	if len(tree.VMs) != 2 {
		t.Fatalf("expected 2 VM menus, got %d", len(tree.VMs))
	}
	// Sorted by name.
	if tree.VMs[0].Name != "db" || tree.VMs[1].Name != "web" {
		t.Fatalf("VMs not sorted by name: %v", []string{tree.VMs[0].Name, tree.VMs[1].Name})
	}

	// web: running → Stop action; two apps sorted by Name.
	web := tree.VMs[1]
	if !web.HasApps {
		t.Error("web should have apps")
	}
	if len(web.Apps) != 2 {
		t.Fatalf("web apps = %d, want 2", len(web.Apps))
	}
	if web.Apps[0].Label != "Code" || web.Apps[1].Label != "Firefox" {
		t.Errorf("apps not sorted by Name: %v", web.Apps)
	}
	if web.Apps[0].ID != "vm/web/app/code.desktop" {
		t.Errorf("app id = %q", web.Apps[0].ID)
	}
	hasStop, hasStart := false, false
	for _, it := range web.Actions {
		if it.ID == "vm/web/stop" {
			hasStop = true
		}
		if it.ID == "vm/web/start" {
			hasStart = true
		}
		if it.ID == "vm/web/sync" && !it.Enabled {
			t.Error("sync action should be enabled")
		}
	}
	if !hasStop || hasStart {
		t.Errorf("running web should offer Stop (got start=%v stop=%v)", hasStart, hasStop)
	}

	// db: stopped → Start action; no apps → placeholder.
	db := tree.VMs[0]
	if db.HasApps {
		t.Error("db has no cached apps; HasApps should be false")
	}
	if len(db.Apps) != 1 || db.Apps[0].Enabled || db.Apps[0].ID != "vm/db/app/none" {
		t.Errorf("db should show a single disabled placeholder, got %+v", db.Apps)
	}
	hasStartDB := false
	for _, it := range db.Actions {
		if it.ID == "vm/db/start" {
			hasStartDB = true
		}
	}
	if !hasStartDB {
		t.Error("stopped db should offer Start")
	}

	// Footer.
	ids := map[string]bool{}
	for _, it := range tree.Footer {
		ids[it.ID] = true
	}
	if !ids["refresh-all"] || !ids["quit"] {
		t.Errorf("footer missing refresh-all/quit: %+v", tree.Footer)
	}
}

func TestComputeMenuSignatureStable(t *testing.T) {
	visible := fakeVisible("web", apps.AppEntry{Name: "Firefox", Base: "firefox.desktop"})
	a := ComputeMenu([]VMState{{Name: "web", State: "running"}}, visible)
	b := ComputeMenu([]VMState{{Name: "web", State: "running"}}, visible)
	if a.Signature() != b.Signature() {
		t.Error("equal trees must have equal signatures")
	}
	// State change alters the signature.
	c := ComputeMenu([]VMState{{Name: "web", State: "stopped"}}, visible)
	if a.Signature() == c.Signature() {
		t.Error("state change must alter the signature")
	}
	// App-set change alters the signature.
	d := ComputeMenu([]VMState{{Name: "web", State: "running"}},
		fakeVisible("web", apps.AppEntry{Name: "Firefox", Base: "firefox.desktop"},
			apps.AppEntry{Name: "Extra", Base: "extra.desktop"}))
	if a.Signature() == d.Signature() {
		t.Error("app-set change must alter the signature")
	}
}
