package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ShowCreateTemplateDialog shows the create template dialog.
func ShowCreateTemplateDialog(parent fyne.Window, model *AppModel, log func(string), onCreated func()) {
	// Form fields.
	imageEntry := widget.NewEntry()
	imageEntry.PlaceHolder = "quay.io/fedora/fedora:44"

	forceCheck := widget.NewCheck("Force re-bake", func(bool) {})

	form := widget.NewForm(
		widget.NewFormItem("Image Ref", imageEntry),
		widget.NewFormItem("Force", forceCheck),
	)

	d := dialog.NewCustomConfirm("Create Template", "Create", "Cancel", form, func(confirmed bool) {
		if !confirmed {
			return
		}

		image := imageEntry.Text
		if image == "" {
			dialog.ShowError(fmt.Errorf("image ref is required"), parent)
			return
		}

		log(fmt.Sprintf("Creating template %s (force=%v)...", image, forceCheck.Checked))

		// Note: actual template creation requires template.Ensure with
		// a podman client. This is a long-running operation (~5 min) that
		// should run in a goroutine with progress updates. For now, we log
		// the intent.
		log(fmt.Sprintf("Template %s creation requested (pending full wiring)", image))

		if onCreated != nil {
			onCreated()
		}
	}, parent)

	d.Show()
}

// ShowDeleteTemplateDialog shows a confirmation dialog for deleting a template.
func ShowDeleteTemplateDialog(parent fyne.Window, model *AppModel, log func(string), onDeleted func()) {
	dir := model.SelectedTemplateDir()
	if dir == "" {
		dialog.ShowError(fmt.Errorf("no template selected"), parent)
		return
	}

	tpls := model.GetTemplates()
	tpl, ok := tpls[dir]
	if !ok {
		dialog.ShowError(fmt.Errorf("template not found"), parent)
		return
	}

	d := dialog.NewConfirm("Delete Template",
		fmt.Sprintf("Delete template %s? This will remove the template disk but not affect VMs using it until they are recreated.", tpl.Image),
		func(confirmed bool) {
			if !confirmed {
				return
			}

			log(fmt.Sprintf("Deleting template %s...", tpl.Image))

			// Note: actual deletion requires os.RemoveAll on the template dir.
			// Wired up when the Actions struct has access to the filesystem.
			log(fmt.Sprintf("Template %s deletion requested (pending full wiring)", tpl.Image))

			// Remove from model.
			t := model.GetTemplates()
			delete(t, dir)
			model.SetTemplates(t)
			model.SelectTemplate("")

			if onDeleted != nil {
				onDeleted()
			}
		}, parent)

	d.Show()
}
