package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// ConfigPanel displays the configuration of the selected VM or template.
type ConfigPanel struct {
	*fyne.Container
	model *AppModel

	// Placeholder shown when nothing is selected.
	placeholder *widget.Label
}

// NewConfigPanel creates a config panel bound to the app model.
func NewConfigPanel(model *AppModel) *ConfigPanel {
	p := &ConfigPanel{
		model: model,
	}

	p.placeholder = widget.NewLabel("Select a VM or template to view its configuration.")

	p.Container = fyne.NewContainerWithLayout(
		layout.NewVBoxLayout(),
		p.placeholder,
	)

	return p
}

// Refresh updates the panel based on the current selection.
func (p *ConfigPanel) Refresh() {
	if model := p.model; model != nil {
		tab := model.CurrentTabName()
		switch tab {
		case "vms":
			if name := model.SelectedVMName(); name != "" {
				p.showVMConfig(name)
			} else {
				p.showPlaceholder("Select a VM to view its configuration.")
			}
		case "templates":
			if dir := model.SelectedTemplateDir(); dir != "" {
				p.showTemplateConfig(dir)
			} else {
				p.showPlaceholder("Select a template to view its details.")
			}
		default:
			p.showPlaceholder("Select a VM or template to view its configuration.")
		}
	}
}

// showPlaceholder shows the empty state message.
func (p *ConfigPanel) showPlaceholder(msg string) {
	p.placeholder.SetText(msg)
	p.Objects = []fyne.CanvasObject{p.placeholder}
	p.Resize(p.Size())
	p.RefreshCanvas()
}

// showVMConfig shows the VM configuration panel.
func (p *ConfigPanel) showVMConfig(name string) {
	vms := p.model.GetVMs()
	states := p.model.GetDomainStates()

	meta, ok := vms[name]
	if !ok {
		p.showPlaceholder(fmt.Sprintf("VM %q not found.", name))
		return
	}

	state := "stopped"
	if s, ok := states[name]; ok {
		state = s
	}

	// Identity section.
	identity := widget.NewForm(
		widget.NewFormItem("Name", widget.NewLabel(meta.Name)),
		widget.NewFormItem("Type", widget.NewLabel(meta.Type)),
		widget.NewFormItem("Domain", widget.NewLabel(meta.Domain)),
		widget.NewFormItem("IP", widget.NewLabel(meta.IP)),
		widget.NewFormItem("MAC", widget.NewLabel(meta.MAC)),
		widget.NewFormItem("UUID", widget.NewLabel(meta.UUID)),
	)

	// Resources section.
	resources := widget.NewForm(
		widget.NewFormItem("Memory (MB)", widget.NewLabel(fmt.Sprintf("%d", meta.MemoryMB))),
		widget.NewFormItem("vCPUs", widget.NewLabel(fmt.Sprintf("%d", meta.VCPUs))),
		widget.NewFormItem("Cores", widget.NewLabel(formatCores(meta.Cores))),
		widget.NewFormItem("State", widget.NewLabel(state)),
	)

	// Storage section.
	storage := widget.NewForm(
		widget.NewFormItem("Image", widget.NewLabel(meta.Image)),
		widget.NewFormItem("Digest", widget.NewLabel(truncateDigest(meta.Digest))),
	)
	if meta.WorkspaceSizeMB > 0 {
		storage.Append("Workspace (MB)", widget.NewLabel(fmt.Sprintf("%d", meta.WorkspaceSizeMB)))
	}
	if len(meta.Mounts) > 0 {
		mountsText := ""
		for _, m := range meta.Mounts {
			mountsText += fmt.Sprintf("%s → %s\n", m.Host, m.Guest)
		}
		storage.Append("Mounts", widget.NewLabel(mountsText))
	}

	// Status section.
	status := widget.NewForm(
		widget.NewFormItem("Created", widget.NewLabel(meta.Created.Format("2006-01-02 15:04:05"))),
	)

	// Combine sections with a scroll container.
	sections := container.NewVBox(
		widget.NewLabelWithStyle("Identity", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		identity,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Resources", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		resources,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Storage", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		storage,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Status", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		status,
	)

	scroll := container.NewScroll(sections)
	p.Objects = []fyne.CanvasObject{scroll}
	p.Resize(p.Size())
	p.RefreshCanvas()
}

// showTemplateConfig shows the template configuration panel.
func (p *ConfigPanel) showTemplateConfig(dir string) {
	tpls := p.model.GetTemplates()

	tpl, ok := tpls[dir]
	if !ok {
		p.showPlaceholder(fmt.Sprintf("Template %q not found.", dir))
		return
	}

	form := widget.NewForm(
		widget.NewFormItem("Image Ref", widget.NewLabel(tpl.Image)),
		widget.NewFormItem("Digest", widget.NewLabel(truncateDigest(tpl.Digest))),
	)

	p.Objects = []fyne.CanvasObject{container.NewScroll(form)}
	p.Resize(p.Size())
	p.RefreshCanvas()
}

// formatCores formats a list of core IDs as a string.
func formatCores(cores []int) string {
	if len(cores) == 0 {
		return "auto"
	}
	result := ""
	for i, c := range cores {
		if i > 0 {
			result += ", "
		}
		result += fmt.Sprintf("%d", c)
	}
	return result
}

// truncateDigest truncates a long digest string for display.
func truncateDigest(digest string) string {
	if len(digest) > 20 {
		return digest[:20] + "…"
	}
	return digest
}

// RefreshCanvas triggers a full canvas refresh.
func (p *ConfigPanel) RefreshCanvas() {
	if p.Container != nil {
		p.Container.Refresh()
	}
}
