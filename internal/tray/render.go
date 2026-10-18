package tray

import "fyne.io/systray"

// Renderer builds the *systray menu from a MenuTree and dispatches clicks.
// Top-level items are created with systray.AddMenuItem; it tracks them so
// Render can clear them before rebuilding (systray has no "clear all
// top-level children" primitive).
type Renderer struct {
	dispatch func(id string)
	topLevel []*systray.MenuItem
}

// NewRenderer wraps a click-dispatch closure.
func NewRenderer(dispatch func(id string)) *Renderer {
	return &Renderer{dispatch: dispatch}
}

// Render replaces the top-level menu with the given tree. Safe to call
// repeatedly; only top-level items and their subtrees are touched.
func (r *Renderer) Render(tree MenuTree) {
	for _, item := range r.topLevel {
		item.Remove()
	}
	r.topLevel = nil

	for _, vm := range tree.VMs {
		top := systray.AddMenuItem(vm.VMMenuLabel(), vm.Tooltip())
		r.topLevel = append(r.topLevel, top)
		addChildren(top, vm.Apps, r.dispatch)
		top.AddSeparator()
		addChildren(top, vm.Actions, r.dispatch)
	}
	for _, it := range tree.Footer {
		child := systray.AddMenuItem(it.Label, it.Label)
		r.topLevel = append(r.topLevel, child)
		wire(child, it, r.dispatch)
	}
}

// RenderUnreachable replaces the menu with a single disabled item reporting
// that the daemon is unreachable (poll error path).
func (r *Renderer) RenderUnreachable(msg string) {
	for _, item := range r.topLevel {
		item.Remove()
	}
	r.topLevel = nil
	child := systray.AddMenuItem(msg, msg)
	child.Disable()
	r.topLevel = append(r.topLevel, child)
}

// addChildren appends each Item as a leaf under parent and wires dispatch.
func addChildren(parent *systray.MenuItem, items []Item, dispatch func(id string)) {
	for _, it := range items {
		child := parent.AddSubMenuItem(it.Label, it.Label)
		wire(child, it, dispatch)
	}
}

// wire disables the item if not enabled and starts a goroutine that dispatches
// its ID on each click. The goroutine exits when the item is Removed (systray
// closes ClickedCh).
func wire(child *systray.MenuItem, it Item, dispatch func(id string)) {
	if !it.Enabled {
		child.Disable()
	}
	go func(id string, ch <-chan struct{}) {
		for range ch {
			dispatch(id)
		}
	}(it.ID, child.ClickedCh)
}
