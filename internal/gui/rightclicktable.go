package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// rightClickTable wraps a Table to add right-click context menu support.
type rightClickTable struct {
	*widget.Table
	onRightClick func(pos fyne.Position)
}

// newRightClickTable creates a table wrapper with right-click handling.
func newRightClickTable(t *widget.Table, onRightClick func(pos fyne.Position)) *rightClickTable {
	return &rightClickTable{
		Table:        t,
		onRightClick: onRightClick,
	}
}

// MouseDown intercepts mouse events to handle right-clicks.
func (r *rightClickTable) MouseDown(e *desktop.MouseEvent) {
	if e.Button == desktop.MouseButtonSecondary && r.onRightClick != nil {
		// Convert window-relative position to canvas-relative position.
		pos := e.Position
		r.onRightClick(pos)
		return // Don't pass to the table (avoid selecting the cell).
	}
	r.Table.MouseDown(e)
}

// CreateRenderer returns the table's renderer.
func (r *rightClickTable) CreateRenderer() fyne.WidgetRenderer {
	return r.Table.CreateRenderer()
}

// MinSize returns the table's minimum size.
func (r *rightClickTable) MinSize() fyne.Size {
	return r.Table.MinSize()
}

// Resize resizes the table.
func (r *rightClickTable) Resize(size fyne.Size) {
	r.Table.Resize(size)
}

// Refresh refreshes the table.
func (r *rightClickTable) Refresh() {
	r.Table.Refresh()
}

// Move moves the table.
func (r *rightClickTable) Move(pos fyne.Position) {
	r.Table.Move(pos)
}

// Visible returns whether the table is visible.
func (r *rightClickTable) Visible() bool {
	return r.Table.Visible()
}

// Hide hides the table.
func (r *rightClickTable) Hide() {
	r.Table.Hide()
}

// Show shows the table.
func (r *rightClickTable) Show() {
	r.Table.Show()
}
