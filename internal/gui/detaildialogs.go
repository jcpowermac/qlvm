package gui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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
		if meta.Image == tpl.Image && meta.Digest == tpl.Digest {
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
		widget.NewFormItem("Digest", widget.NewLabel(tpl.Digest)),
		widget.NewFormItem("VMs", widget.NewLabel(vmListText)),
	)

	// Note about latest tags.
	var note *widget.Label
	if strings.HasSuffix(tpl.Image, ":latest") {
		note = widget.NewLabelWithStyle(
			"Note: 'latest' tags change when the upstream image is updated.\n" +
				"Multiple entries may exist for the same image name with different digests.",
			fyne.TextAlignLeading,
			fyne.TextStyle{Italic: true},
		)
	}

	var content fyne.CanvasObject
	if note != nil {
		content = container.NewVBox(form, widget.NewSeparator(), note)
	} else {
		content = form
	}

	d := dialog.NewCustom("Template: "+tpl.Image, "Close", content, parent)
	d.Resize(fyne.NewSize(400, 300))
	d.Show()
}
