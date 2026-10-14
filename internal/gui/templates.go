package gui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// TemplateListView displays a list of templates with image ref, digest, and VM count.
type TemplateListView struct {
	*widget.List
	model *AppModel

	// OnSelectionChange is called when a template is selected.
	OnSelectionChange func(dir string)
}

// NewTemplateListView creates a template list view bound to the app model.
func NewTemplateListView(model *AppModel) *TemplateListView {
	l := &TemplateListView{
		model: model,
	}

	l.List = widget.NewList(
		func() int {
			return len(l.filteredTemplates())
		},
		func() fyne.CanvasObject {
			return l.createTemplateItem()
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			l.updateTemplateItem(id, item)
		},
	)

	l.List.OnSelected = func(id widget.ListItemID) {
		tpls := l.filteredTemplates()
		if id < len(tpls) {
			l.model.SelectTemplate(tpls[id].Dir)
			if l.OnSelectionChange != nil {
				l.OnSelectionChange(tpls[id].Dir)
			}
		}
	}

	return l
}

// filteredTemplates returns templates matching the current filter.
func (l *TemplateListView) filteredTemplates() []TemplateEntry {
	tpls := l.model.GetTemplates()
	vms := l.model.GetVMs()
	filter := strings.ToLower(l.model.FilterText())

	entries := make([]TemplateEntry, 0, len(tpls))
	for dir, tpl := range tpls {
		if filter != "" && !strings.Contains(strings.ToLower(tpl.Image), filter) {
			continue
		}
		// Count VMs referencing this template.
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

// TemplateEntry is a single template row in the list.
type TemplateEntry struct {
	Dir     string
	Image   string
	Digest  string
	VMCount int
}

// templateItem is a list item with named label fields.
type templateItem struct {
	*fyne.Container
	image   *widget.Label
	digest  *widget.Label
	vmCount *widget.Label
}

// createTemplateItem creates a new list item widget.
func (l *TemplateListView) createTemplateItem() fyne.CanvasObject {
	image := widget.NewLabel("")
	image.Truncation = fyne.TextTruncateEllipsis

	digest := widget.NewLabel("")
	digest.Truncation = fyne.TextTruncateEllipsis
	digest.Alignment = fyne.TextAlignTrailing

	vmCount := widget.NewLabel("")

	return &templateItem{
		Container: container.NewHBox(image, vmCount, digest),
		image:     image,
		digest:    digest,
		vmCount:   vmCount,
	}
}

// updateTemplateItem updates the content of a list item.
func (l *TemplateListView) updateTemplateItem(id widget.ListItemID, item fyne.CanvasObject) {
	tpls := l.filteredTemplates()
	if id >= len(tpls) {
		return
	}

	entry := tpls[id]
	ti, ok := item.(*templateItem)
	if !ok {
		return
	}

	ti.image.SetText(entry.Image)
	ti.digest.SetText(truncateDigest(entry.Digest))
	ti.vmCount.SetText(formatVMCount(entry.VMCount))
}

// formatVMCount formats the VM count as a string.
func formatVMCount(count int) string {
	if count == 0 {
		return ""
	}
	return " (" + string(rune('0'+count)) + " VMs)"
}

// Refresh reloads the list from the model.
func (l *TemplateListView) Refresh() {
	l.List.Refresh()
}
