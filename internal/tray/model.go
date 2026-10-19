package tray

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/jcpowermac/qlvm/internal/apps"
)

// VMState is the tray's per-VM view: name plus the daemon's state string
// (running, stopped, dying, paused, blocked, ...).
type VMState struct {
	Name  string
	State string
}

// Item is one leaf menu entry. ID encodes the dispatch target so the renderer
// can map a click back to an action without capturing closures.
type Item struct {
	Label   string
	Enabled bool
	ID      string
}

// VMMenu is one VM's submenu: its (whitelist-filtered) apps plus lifecycle
// actions.
type VMMenu struct {
	Name    string
	State   string
	HasApps bool
	Apps    []Item
	Actions []Item
}

// MenuTree is the full tray menu in a render-agnostic, hashable form.
type MenuTree struct {
	Header string
	VMs    []VMMenu
	Footer []Item
}

const (
	idRefreshAll = "refresh-all"
	idQuit       = "quit"
)

// stateMarker picks a glyph for the VM's state shown in the top-level item.
func stateMarker(state string) string {
	switch state {
	case "running":
		return "●"
	case "stopped":
		return "○"
	default:
		return "◌"
	}
}

// ComputeMenu builds the tray menu from VM states and a per-VM visible-apps
// lookup (which already applies the whitelist). VMs are sorted by name, apps
// by Name; a VM with no visible apps shows a single disabled placeholder.
func ComputeMenu(states []VMState, visible func(vm string) ([]apps.AppEntry, error)) MenuTree {
	sorted := make([]VMState, len(states))
	copy(sorted, states)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	tree := MenuTree{Header: "qlvm", VMs: make([]VMMenu, 0, len(sorted))}
	for _, s := range sorted {
		apps, _ := visible(s.Name)
		sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })

		menu := VMMenu{Name: s.Name, State: s.State}
		if len(apps) > 0 {
			menu.HasApps = true
			menu.Apps = make([]Item, 0, len(apps))
			for _, a := range apps {
				menu.Apps = append(menu.Apps, Item{
					Label:   a.Name,
					Enabled: true,
					ID:      "vm/" + s.Name + "/app/" + a.Base,
				})
			}
		} else {
			menu.Apps = []Item{{
				Label:   `No apps cached — use "Sync app list"`,
				Enabled: false,
				ID:      "vm/" + s.Name + "/app/none",
			}}
		}

		menu.Actions = []Item{{Label: "Sync app list", Enabled: true, ID: "vm/" + s.Name + "/sync"}}
		if s.State == "running" {
			menu.Actions = append(menu.Actions, Item{Label: "Stop", Enabled: true, ID: "vm/" + s.Name + "/stop"})
		} else {
			menu.Actions = append(menu.Actions, Item{Label: "Start", Enabled: true, ID: "vm/" + s.Name + "/start"})
		}
		tree.VMs = append(tree.VMs, menu)
	}

	tree.Footer = []Item{
		{Label: "Refresh all apps", Enabled: true, ID: idRefreshAll},
		{Label: "Quit", Enabled: true, ID: idQuit},
	}
	return tree
}

// Signature is a stable hash of the tree contents, used by the renderer to
// rebuild the menu only when the model actually changed.
func (t MenuTree) Signature() string {
	h := fnv.New32a()
	write := func(s string) {
		_, _ = fmt.Fprintf(h, "%s\x00", s) // fnv never fails to write
	}
	write(t.Header)
	for _, v := range t.VMs {
		write(v.Name)
		write(v.State)
		write(fmt.Sprintf("%v", v.HasApps))
		for _, a := range v.Apps {
			write(a.ID)
			write(a.Label)
			write(fmt.Sprintf("%v", a.Enabled))
		}
		for _, a := range v.Actions {
			write(a.ID)
			write(a.Label)
			write(fmt.Sprintf("%v", a.Enabled))
		}
	}
	for _, f := range t.Footer {
		write(f.ID)
		write(f.Label)
		write(fmt.Sprintf("%v", f.Enabled))
	}
	return fmt.Sprintf("%x", h.Sum32())
}
