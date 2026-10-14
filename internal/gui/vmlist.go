package gui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// VMListView displays a list of VMs with name, type, state, memory, and vCPUs.
type VMListView struct {
	*widget.List
	model *AppModel

	// OnSelectionChange is called when a VM is selected.
	OnSelectionChange func(name string)
}

// NewVMListView creates a VM list view bound to the app model.
func NewVMListView(model *AppModel) *VMListView {
	l := &VMListView{
		model: model,
	}

	l.List = widget.NewList(
		func() int {
			return len(l.filteredVMs())
		},
		func() fyne.CanvasObject {
			return l.createListItem()
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			l.updateListItem(id, item)
		},
	)

	l.List.OnSelected = func(id widget.ListItemID) {
		vms := l.filteredVMs()
		if id < len(vms) {
			l.model.SelectVM(vms[id].Name)
			if l.OnSelectionChange != nil {
				l.OnSelectionChange(vms[id].Name)
			}
		}
	}

	return l
}

// filteredVMs returns VMs matching the current filter, sorted by name.
func (l *VMListView) filteredVMs() []VMEntry {
	vms := l.model.GetVMs()
	states := l.model.GetDomainStates()
	filter := strings.ToLower(l.model.FilterText())

	entries := make([]VMEntry, 0, len(vms))
	for name, meta := range vms {
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		state := "stopped"
		if s, ok := states[name]; ok {
			state = s
		}
		entries = append(entries, VMEntry{
			Name:   name,
			Type:   meta.Type,
			State:  state,
			Memory: meta.MemoryMB,
			VCpus:  meta.VCPUs,
		})
	}

	// Sort by name.
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	return entries
}

// VMEntry is a single VM row in the list.
type VMEntry struct {
	Name   string
	Type   string
	State  string
	Memory int
	VCpus  int
}

// vmItem is a list item with named label fields.
type vmItem struct {
	*fyne.Container
	name  *widget.Label
	state *widget.Label
}

// createListItem creates a new list item widget.
func (l *VMListView) createListItem() fyne.CanvasObject {
	name := widget.NewLabel("")
	name.Truncation = fyne.TextTruncateEllipsis

	state := widget.NewLabel("")
	state.Alignment = fyne.TextAlignTrailing

	return &vmItem{
		Container: container.NewBorder(nil, nil, name, state, nil),
		name:      name,
		state:     state,
	}
}

// updateListItem updates the content of a list item.
func (l *VMListView) updateListItem(id widget.ListItemID, item fyne.CanvasObject) {
	vms := l.filteredVMs()
	if id >= len(vms) {
		return
	}

	entry := vms[id]
	vi, ok := item.(*vmItem)
	if !ok {
		return
	}

	vi.name.SetText(entry.Name)
	vi.state.SetText(entry.State)
	switch entry.State {
	case "running":
		vi.state.Importance = widget.SuccessImportance
	case "dying":
		vi.state.Importance = widget.DangerImportance
	default:
		vi.state.Importance = widget.MediumImportance
	}
}

// Refresh reloads the list from the model.
func (l *VMListView) Refresh() {
	l.List.Refresh()
}
