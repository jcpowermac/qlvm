package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// vmTable displays VMs in a table with columns.
type vmTable struct {
	*widget.Table
	model    *AppModel
	onSelect func(name string)
}

// vmTableDefaultWidths are the default column widths.
var vmTableDefaultWidths = []float32{200, 100, 100, 100, 80, 100, 120} // Name, Type, State, Memory, vCPUs, Domain, IP

// vmTableColumns defines the column headers.
var vmTableColumns = []string{"Name", "Type", "State", "Memory", "vCPUs", "Domain", "IP"}

// newVMTable creates a VM table view.
func newVMTable(model *AppModel, onSelect func(name string)) *vmTable {
	t := &vmTable{
		model:    model,
		onSelect: onSelect,
	}

	t.Table = widget.NewTable(
		func() (rows, cols int) {
			return len(t.filteredVMs()), len(vmTableColumns)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			t.updateCell(id, cell)
		},
	)

	// Set default column widths (enables resizing).
	for i, w := range vmTableDefaultWidths {
		t.SetColumnWidth(i, w)
	}

	t.Table.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabel("")
	}
	t.Table.UpdateHeader = func(id widget.TableCellID, cell fyne.CanvasObject) {
		cell.(*widget.Label).SetText(vmTableColumns[id.Col])
		cell.(*widget.Label).TextStyle = fyne.TextStyle{Bold: true}
	}

	t.Table.OnSelected = func(id widget.TableCellID) {
		vms := t.filteredVMs()
		if id.Row < len(vms) && t.onSelect != nil {
			t.onSelect(vms[id.Row].Name)
		}
	}

	return t
}

// filteredVMs returns VMs matching the current filter, sorted by name.
func (t *vmTable) filteredVMs() []VMEntry {
	vms := t.model.GetVMs()
	states := t.model.GetDomainStates()
	filter := strings.ToLower(t.model.FilterText())

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
			Domain: meta.Domain,
			IP:     meta.IP,
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
	Memory int
	VCpus  int
	Domain string
	IP     string
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
	case 6:
		text = entry.IP
	}

	label.SetText(text)
}


