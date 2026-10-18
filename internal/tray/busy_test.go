package tray

import (
	"testing"
)

func TestVMOfID(t *testing.T) {
	cases := []struct {
		id string
		vm string
		ok bool
	}{
		{"vm/web/app/x.desktop", "web", true},
		{"vm/web/sync", "web", true},
		{"vm/web/start", "web", true},
		{"vm/web/stop", "web", true},
		{"refresh-all", "", false},
		{"quit", "", false},
		{"bogus", "", false},
	}
	for _, c := range cases {
		vm, ok := VMOfID(c.id)
		if vm != c.vm || ok != c.ok {
			t.Errorf("VMOfID(%q) = (%q,%v), want (%q,%v)", c.id, vm, ok, c.vm, c.ok)
		}
	}
}

func TestWithBusyDisablesBusyVMIterms(t *testing.T) {
	tree := MenuTree{
		Header: "qlvm",
		VMs: []VMMenu{
			{
				Name: "web", State: "running", HasApps: true,
				Apps:    []Item{{Label: "Firefox", Enabled: true, ID: "vm/web/app/x"}},
				Actions: []Item{{Label: "Sync app list", Enabled: true, ID: "vm/web/sync"}},
			},
			{
				Name: "db", State: "stopped",
				Apps:    []Item{{Label: "none", Enabled: false, ID: "vm/db/app/none"}},
				Actions: []Item{{Label: "Start", Enabled: true, ID: "vm/db/start"}},
			},
		},
		Footer: []Item{{Label: "Quit", Enabled: true, ID: "quit"}},
	}
	busy := func(vm string) bool { return vm == "web" }
	got := WithBusy(tree, busy)

	if got.VMs[0].Apps[0].Enabled || got.VMs[0].Actions[0].Enabled {
		t.Errorf("busy VM web items should be disabled: %+v", got.VMs[0])
	}
	if !got.VMs[1].Actions[0].Enabled {
		t.Errorf("non-busy VM db items should stay enabled: %+v", got.VMs[1])
	}
	// Busy state changes the signature.
	if tree.Signature() == got.Signature() {
		t.Error("busy state should change the signature")
	}
}
