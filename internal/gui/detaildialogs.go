package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ShowVMDetailDialog shows a dialog with full VM details.
func ShowVMDetailDialog(parent fyne.Window, model *AppModel, name string) {
	vms := model.GetVMs()
	states := model.GetDomainStates()

	meta, ok := vms[name]
	if !ok {
		dialog.ShowError(fmt.Errorf("VM %q not found", name), parent)
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
		widget.NewFormItem("Memory", widget.NewLabel(fmt.Sprintf("%d MB", meta.MemoryMB))),
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
		storage.Append("Workspace", widget.NewLabel(fmt.Sprintf("%d MB", meta.WorkspaceSizeMB)))
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

	// Combine sections.
	content := container.NewVBox(
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

	d := dialog.NewCustom("VM: "+meta.Name, "Close", container.NewScroll(content), parent)
	d.Resize(fyne.NewSize(500, 500))
	d.Show()
}

// ShowTemplateDetailDialog shows a dialog with template details.
func ShowTemplateDetailDialog(parent fyne.Window, model *AppModel, dir string) {
	tpls := model.GetTemplates()

	tpl, ok := tpls[dir]
	if !ok {
		dialog.ShowError(fmt.Errorf("template not found"), parent)
		return
	}

	// Count VMs using this template.
	vms := model.GetVMs()
	var vmNames []string
	for name, meta := range vms {
		if meta.Image == tpl.Image {
			vmNames = append(vmNames, name)
		}
	}

	vmListText := "none"
	if len(vmNames) > 0 {
		vmListText = ""
		for _, n := range vmNames {
			vmListText += n + "\n"
		}
	}

	form := widget.NewForm(
		widget.NewFormItem("Image", widget.NewLabel(tpl.Image)),
		widget.NewFormItem("Digest", widget.NewLabel(truncateDigest(tpl.Digest))),
		widget.NewFormItem("VMs", widget.NewLabel(vmListText)),
	)

	d := dialog.NewCustom("Template: "+tpl.Image, "Close", form, parent)
	d.Resize(fyne.NewSize(400, 300))
	d.Show()
}
