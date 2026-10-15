package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// templateTable displays templates in a table with columns and right-click context menu.
type templateTable struct {
	*rightClickTable
	model    *AppModel
	window   fyne.Window
	client   Client
	onSelect func(dir string)
}

// templateTableColumns defines the column headers.
var templateTableColumns = []string{"Image", "Digest", "Size", "VMs"}

// templateTableDefaultWidths are the default column widths.
var templateTableDefaultWidths = []float32{300, 250, 100, 80} // Image, Digest, Size, VMs

// newTemplateTable creates a template table view.
func newTemplateTable(model *AppModel, window fyne.Window, client Client, onSelect func(dir string)) *templateTable {
	table := widget.NewTableWithHeaders(
		func() (rows, cols int) {
			return len(model.GetTemplates()), len(templateTableColumns)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(_ widget.TableCellID, cell fyne.CanvasObject) {
			// Will be updated by the templateTable wrapper.
		},
	)

	// Show only column headers (no row headers).
	table.ShowHeaderRow = true
	table.ShowHeaderColumn = false

	// Override header content to show column names instead of A, B, C...
	table.UpdateHeader = func(id widget.TableCellID, cell fyne.CanvasObject) {
		if id.Row < 0 {
			// Column header.
			cell.(*widget.Label).SetText(templateTableColumns[id.Col])
			cell.(*widget.Label).TextStyle = fyne.TextStyle{Bold: true}
		} else if id.Col < 0 {
			// Row header (shouldn't appear since ShowHeaderColumn=false).
			cell.(*widget.Label).SetText("")
		}
	}

	// Set default column widths (enables resizing).
	for i, w := range templateTableDefaultWidths {
		table.SetColumnWidth(i, w)
	}

	// Single click selects the row (no dialog).
	table.OnSelected = func(id widget.TableCellID) {
		tpls := model.GetTemplates()
		filter := strings.ToLower(model.FilterText())

		entries := make([]TemplateEntry, 0, len(tpls))
		for dir, tpl := range tpls {
			if filter != "" && !strings.Contains(strings.ToLower(tpl.Image), filter) {
				continue
			}
			entries = append(entries, TemplateEntry{
				Dir:       dir,
				Image:     tpl.Image,
				Digest:    tpl.Digest,
				SizeBytes: tpl.SizeBytes,
				VMCount:   tpl.VMCount,
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
		if id.Row < len(entries) {
			model.SelectTemplate(entries[id.Row].Dir)
		}
	}

	t := &templateTable{
		model:    model,
		window:   window,
		client:   client,
		onSelect: onSelect,
	}
	t.rightClickTable = newRightClickTable(table, func(pos fyne.Position) {
		t.showContextMenu(pos)
	})

	// Override the cell update function.
	t.UpdateCell = func(id widget.TableCellID, cell fyne.CanvasObject) {
		t.updateCell(id, cell)
	}

	// Override the length function to use filtered templates.
	t.Length = func() (rows int, cols int) {
		return len(t.filteredTemplates()), len(templateTableColumns)
	}

	return t
}

// showContextMenu shows the right-click context menu for the selected template.
func (t *templateTable) showContextMenu(pos fyne.Position) {
	dir := t.model.SelectedTemplateDir()
	if dir == "" {
		return
	}

	tpls := t.model.GetTemplates()
	if _, ok := tpls[dir]; !ok {
		return
	}

	menu := fyne.NewMenu("",
		fyne.NewMenuItem("Details...", func() {
			ShowTemplateDetailDialog(t.window, t.model, dir)
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Delete", func() {
			ShowDeleteTemplateDialog(t.window, t.model, t.client, t.model.Log, t.Refresh)
		}),
	)

	widget.ShowPopUpMenuAtPosition(menu, t.window.Canvas(), pos)
}

// filteredTemplates returns templates matching the current filter.
func (t *templateTable) filteredTemplates() []TemplateEntry {
	tpls := t.model.GetTemplates()
	filter := strings.ToLower(t.model.FilterText())

	entries := make([]TemplateEntry, 0, len(tpls))
	for dir, tpl := range tpls {
		if filter != "" && !strings.Contains(strings.ToLower(tpl.Image), filter) {
			continue
		}
		entries = append(entries, TemplateEntry{
			Dir:       dir,
			Image:     tpl.Image,
			Digest:    tpl.Digest,
			SizeBytes: tpl.SizeBytes,
			VMCount:   tpl.VMCount,
		})
	}

	// Sort by image ref, then digest (stable ordering for same image).
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Image > entries[j].Image ||
				(entries[i].Image == entries[j].Image && entries[i].Digest > entries[j].Digest) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	return entries
}

// TemplateEntry is a single template row.
type TemplateEntry struct {
	Dir       string
	Image     string
	Digest    string
	SizeBytes uint64
	VMCount   int
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
		text = formatSize(entry.SizeBytes)
	case 3:
		text = fmt.Sprintf("%d", entry.VMCount)
	}

	label.SetText(text)
}
