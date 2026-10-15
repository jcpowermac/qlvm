package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// vmTable displays VMs in a table with columns and right-click context menu.
type vmTable struct {
	*rightClickTable
	model    *AppModel
	window   fyne.Window
	actions  *Actions
	onSelect func(name string)
}

// vmTableColumns defines the column headers.
var vmTableColumns = []string{"Name", "Type", "State", "Memory", "vCPUs", "Domain"}

// vmTableDefaultWidths are the default column widths.
var vmTableDefaultWidths = []float32{200, 100, 100, 100, 80, 100} // Name, Type, State, Memory, vCPUs, Domain

// newVMTable creates a VM table view.
func newVMTable(model *AppModel, window fyne.Window, actions *Actions, onSelect func(name string)) *vmTable {
	table := widget.NewTableWithHeaders(
		func() (rows, cols int) {
			return len(model.GetVMs()), len(vmTableColumns)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(_ widget.TableCellID, cell fyne.CanvasObject) {
			// Will be updated by the vmTable wrapper.
		},
	)

	// Show only column headers (no row headers).
	table.ShowHeaderRow = true
	table.ShowHeaderColumn = false

	// Override header content to show column names instead of A, B, C...
	table.UpdateHeader = func(id widget.TableCellID, cell fyne.CanvasObject) {
		if id.Row < 0 {
			// Column header.
			cell.(*widget.Label).SetText(vmTableColumns[id.Col])
			cell.(*widget.Label).TextStyle = fyne.TextStyle{Bold: true}
		} else if id.Col < 0 {
			// Row header (shouldn't appear since ShowHeaderColumn=false).
			cell.(*widget.Label).SetText("")
		}
	}

	// Set default column widths (enables resizing).
	for i, w := range vmTableDefaultWidths {
		table.SetColumnWidth(i, w)
	}

	// Single click selects the row (no dialog).
	table.OnSelected = func(id widget.TableCellID) {
		vms := model.GetVMs()
		filter := strings.ToLower(model.FilterText())

		entries := make([]VMEntry, 0, len(vms))
		for name, meta := range vms {
			if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
				continue
			}
			state := meta.State
			if state == "" {
				state = "stopped"
			}
			entries = append(entries, VMEntry{
				Name:   name,
				Type:   meta.Type,
				State:  state,
				Memory: meta.MemoryMB,
				VCpus:  meta.VCPUs,
				Domain: meta.Domain,
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
		if id.Row < len(entries) {
			model.SelectVM(entries[id.Row].Name)
		}
	}

	t := &vmTable{
		model:    model,
		window:   window,
		actions:  actions,
		onSelect: onSelect,
	}
	t.rightClickTable = newRightClickTable(table, func(pos fyne.Position) {
		t.showContextMenu(pos)
	})

	// Override the cell update function.
	t.UpdateCell = func(id widget.TableCellID, cell fyne.CanvasObject) {
		t.updateCell(id, cell)
	}

	// Override the length function to use filtered VMs.
	t.Length = func() (rows int, cols int) {
		return len(t.filteredVMs()), len(vmTableColumns)
	}

	return t
}

// showContextMenu shows the right-click context menu for the selected VM.
func (t *vmTable) showContextMenu(pos fyne.Position) {
	name := t.model.SelectedVMName()
	if name == "" {
		return
	}

	menu := fyne.NewMenu("",
		fyne.NewMenuItem("Details...", func() {
			ShowVMDetailDialog(t.window, t.model, name)
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Start", func() {
			if t.actions != nil {
				t.actions.Start()
			} else {
				t.model.Log(fmt.Sprintf("Cannot start %s: no agent connection", name))
			}
		}),
		fyne.NewMenuItem("Stop", func() {
			if t.actions != nil {
				t.actions.Stop()
			} else {
				t.model.Log(fmt.Sprintf("Cannot stop %s: no agent connection", name))
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Delete", func() {
			if t.actions != nil {
				t.actions.Delete()
			} else {
				t.model.Log(fmt.Sprintf("Cannot delete %s: no agent connection", name))
			}
		}),
	)

	widget.ShowPopUpMenuAtPosition(menu, t.window.Canvas(), pos)
}

// filteredVMs returns VMs matching the current filter, sorted by name.
func (t *vmTable) filteredVMs() []VMEntry {
	vms := t.model.GetVMs()
	filter := strings.ToLower(t.model.FilterText())

	entries := make([]VMEntry, 0, len(vms))
	for name, meta := range vms {
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		state := meta.State
		if state == "" {
			state = "stopped"
		}
		entries = append(entries, VMEntry{
			Name:   name,
			Type:   meta.Type,
			State:  state,
			Memory: meta.MemoryMB,
			VCpus:  meta.VCPUs,
			Domain: meta.Domain,
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

// VMEntry is a single VM row.
type VMEntry struct {
	Name   string
	Type   string
	State  string
	Memory uint64
	VCpus  uint8
	Domain string
}

// updateCell updates a table cell.
func (t *vmTable) updateCell(id widget.TableCellID, cell fyne.CanvasObject) {
	vms := t.filteredVMs()
	if id.Row >= len(vms) {
		cell.(*widget.Label).SetText("")
		return
	}

	entry := vms[id.Row]
	label := cell.(*widget.Label)

	var text string
	switch id.Col {
	case 0:
		text = entry.Name
	case 1:
		text = entry.Type
	case 2:
		text = entry.State
		switch entry.State {
		case "running":
			label.Importance = widget.SuccessImportance
		case "dying":
			label.Importance = widget.DangerImportance
		default:
			label.Importance = widget.MediumImportance
		}
	case 3:
		text = fmt.Sprintf("%d MB", entry.Memory)
	case 4:
		text = fmt.Sprintf("%d", entry.VCpus)
	case 5:
		text = entry.Domain
	}

	label.SetText(text)
}
