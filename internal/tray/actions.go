// Package tray implements the Qubes-style system-tray menu for qlvm: a
// render-agnostic menu model (ComputeMenu/Signature), the click dispatch to
// Actions, and the StatusNotifierItem rendering used by cmd/qlvm-tray.
package tray

import (
	"fmt"
	"strings"
)

// Actions bundles the operations the tray dispatches. Each is a function so
// tests can inject fakes; cmd/qlvm-tray wires the concrete impl (agent client
// + cache + waypipe).
type Actions struct {
	StartVM   func(name string) error
	StopVM    func(name string) error
	SyncVM    func(name string) (int, error)
	SyncAll   func() error
	LaunchApp func(vm, exec string) error
	Quit      func()
	Notify    func(msg string)
}

type idKind int

const (
	kindUnknown idKind = iota
	kindApp
	kindSync
	kindStart
	kindStop
	kindRefreshAll
	kindQuit
)

// parseID maps a menu Item ID back to its kind, VM name, and (for apps) the
// .desktop basename. VM names are assumed not to contain '/'.
func parseID(id string) (idKind, string, string) {
	switch id {
	case idRefreshAll:
		return kindRefreshAll, "", ""
	case idQuit:
		return kindQuit, "", ""
	}
	if !strings.HasPrefix(id, "vm/") {
		return kindUnknown, "", ""
	}
	rest := strings.TrimPrefix(id, "vm/")
	if i := strings.Index(rest, "/app/"); i >= 0 {
		return kindApp, rest[:i], rest[i+len("/app/"):]
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		switch action := rest[i+1:]; action {
		case "sync":
			return kindSync, rest[:i], ""
		case "start":
			return kindStart, rest[:i], ""
		case "stop":
			return kindStop, rest[:i], ""
		}
	}
	return kindUnknown, "", ""
}

// Dispatch returns a func that maps a clicked menu Item ID to the matching
// Actions callback, reporting failures through Notify.
func Dispatch(a Actions) func(id string) {
	notify := a.Notify
	if notify == nil {
		notify = func(string) {}
	}
	return func(id string) {
		kind, name, arg := parseID(id)
		switch kind {
		case kindApp:
			if err := a.LaunchApp(name, arg); err != nil {
				notify(err.Error())
			}
		case kindSync:
			n, err := a.SyncVM(name)
			if err != nil {
				notify(err.Error())
			} else {
				notify(fmt.Sprintf("synced %d app(s) for %s", n, name))
			}
		case kindStart:
			if err := a.StartVM(name); err != nil {
				notify(err.Error())
			} else {
				notify("starting " + name)
			}
		case kindStop:
			if err := a.StopVM(name); err != nil {
				notify(err.Error())
			} else {
				notify("stopping " + name)
			}
		case kindRefreshAll:
			if err := a.SyncAll(); err != nil {
				notify(err.Error())
			} else {
				notify("refreshed all apps")
			}
		case kindQuit:
			a.Quit()
		}
	}
}

// VMOfID reports the VM a menu Item ID belongs to. ok is false for global
// items (refresh-all, quit) and unknown IDs.
func VMOfID(id string) (vm string, ok bool) {
	kind, name, _ := parseID(id)
	switch kind {
	case kindApp, kindSync, kindStart, kindStop:
		return name, true
	}
	return "", false
}

// WithBusy returns a copy of the tree with every item belonging to a busy VM
// (busy(vm) == true) disabled, so in-flight actions are visible. The input
// tree is not mutated. Global (footer) items are left untouched.
func WithBusy(tree MenuTree, busy func(vm string) bool) MenuTree {
	if busy == nil {
		return tree
	}
	out := tree
	out.VMs = make([]VMMenu, len(tree.VMs))
	copy(out.VMs, tree.VMs)
	for i := range out.VMs {
		if !busy(tree.VMs[i].Name) {
			continue
		}
		out.VMs[i].Apps = make([]Item, len(tree.VMs[i].Apps))
		copy(out.VMs[i].Apps, tree.VMs[i].Apps)
		for j := range out.VMs[i].Apps {
			out.VMs[i].Apps[j].Enabled = false
		}
		out.VMs[i].Actions = make([]Item, len(tree.VMs[i].Actions))
		copy(out.VMs[i].Actions, tree.VMs[i].Actions)
		for j := range out.VMs[i].Actions {
			out.VMs[i].Actions[j].Enabled = false
		}
	}
	return out
}

// VMMenuLabel returns the top-level item text for a VM, with a state marker.
func (m VMMenu) VMMenuLabel() string {
	return stateMarker(m.State) + " " + m.Name
}

// Tooltip returns the hover text for a VM's top-level item.
func (m VMMenu) Tooltip() string {
	return m.Name + " (" + m.State + ")"
}

// LaunchApp runs one tray app selection, mirroring `qlvm apps` (see
// apps.Launch): a stopped VM is started, then waited on for its control
// channel, before the app is projected over waypipe (ssh mode, v1). Each
// failing seam stops the chain and reports through notify.
func LaunchApp(
	running func(vm string) bool,
	start func(vm string) error,
	waitControl func(vm string) error,
	runApp func(vm, exec string) error,
	notify func(msg string),
	vm, exec string,
) error {
	if !running(vm) {
		if err := start(vm); err != nil {
			notify(fmt.Sprintf("start %s: %v", vm, err))
			return err
		}
		if err := waitControl(vm); err != nil {
			notify(fmt.Sprintf("wait %s: %v", vm, err))
			return err
		}
	}
	if err := runApp(vm, exec); err != nil {
		notify(fmt.Sprintf("run %s/%s: %v", vm, exec, err))
		return err
	}
	return nil
}

// SyncVMFor refreshes one VM's desktop cache. The daemon fetches the list over
// SSH, which requires a running VM, so a stopped VM is a clear, actionable
// error rather than an opaque ssh dial failure.
func SyncVMFor(running func(vm string) bool, sync func(vm string) (int, error), name string) (int, error) {
	if !running(name) {
		return 0, fmt.Errorf("sync %s: start the VM first (its desktop list is fetched over SSH)", name)
	}
	return sync(name)
}
