package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

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
