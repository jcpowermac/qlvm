package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// templateTable displays templates in a table with columns.
type templateTable struct {
	*widget.Table
	model    *AppModel
	onSelect func(dir string)
}

// templateTableDefaultWidths are the default column widths.
var templateTableDefaultWidths = []float32{300, 250, 80} // Image, Digest, VMs

// templateTableColumns defines the column headers.
var templateTableColumns = []string{"Image", "Digest", "VMs"}

// newTemplateTable creates a template table view.
func newTemplateTable(model *AppModel, onSelect func(dir string)) *templateTable {
	t := &templateTable{
		model:    model,
		onSelect: onSelect,
	}

	t.Table = widget.NewTable(
		func() (rows, cols int) {
			return len(t.filteredTemplates()), len(templateTableColumns)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			t.updateCell(id, cell)
		},
	)

	// Set default column widths (enables resizing).
	for i, w := range templateTableDefaultWidths {
		t.SetColumnWidth(i, w)
	}

	t.Table.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabel("")
	}
	t.Table.UpdateHeader = func(id widget.TableCellID, cell fyne.CanvasObject) {
		cell.(*widget.Label).SetText(templateTableColumns[id.Col])
		cell.(*widget.Label).TextStyle = fyne.TextStyle{Bold: true}
	}

	t.Table.OnSelected = func(id widget.TableCellID) {
		tpls := t.filteredTemplates()
		if id.Row < len(tpls) && t.onSelect != nil {
			t.onSelect(tpls[id.Row].Dir)
		}
	}

	return t
}

// filteredTemplates returns templates matching the current filter.
func (t *templateTable) filteredTemplates() []TemplateEntry {
	tpls := t.model.GetTemplates()
	vms := t.model.GetVMs()
	filter := strings.ToLower(t.model.FilterText())

	entries := make([]TemplateEntry, 0, len(tpls))
	for dir, tpl := range tpls {
		if filter != "" && !strings.Contains(strings.ToLower(tpl.Image), filter) {
			continue
		}
		vmCount := 0
		for _, meta := range vms {
			if meta.Image == tpl.Image {
				vmCount++
			}
		}
		entries = append(entries, TemplateEntry{
			Dir:     dir,
			Image:   tpl.Image,
			Digest:  tpl.Digest,
			VMCount: vmCount,
		})
	}

	// Sort by image ref.
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Image > entries[j].Image {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	return entries
}

// TemplateEntry is a single template row.
type TemplateEntry struct {
	Dir     string
	Image   string
	Digest  string
	VMCount int
}

// updateCell updates a table cell.
func (t *templateTable) updateCell(id widget.TableCellID, cell fyne.CanvasObject) {
	tpls := t.filteredTemplates()
	if id.Row >= len(tpls) {
		cell.(*widget.Label).SetText("")
		return
	}

	entry := tpls[id.Row]
	label := cell.(*widget.Label)

	var text string
	switch id.Col {
	case 0:
		text = entry.Image
	case 1:
		text = truncateDigest(entry.Digest)
	case 2:
		text = fmt.Sprintf("%d", entry.VMCount)
	}

	label.SetText(text)
}


