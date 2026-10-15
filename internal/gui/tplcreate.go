package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ShowCreateTemplateDialog shows the create template dialog and creates the
// template via the D-Bus agent.
func ShowCreateTemplateDialog(parent fyne.Window, _ *AppModel, client Client, log func(string), onCreated func()) {
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

		if client == nil {
			dialog.ShowError(fmt.Errorf("agent not connected"), parent)
			return
		}

		log(fmt.Sprintf("Creating template %s (force=%v)...", image, forceCheck.Checked))

		// Template creation is a long-running operation; run it in a goroutine.
		go func() {
			if _, err := client.CreateTemplate(image, forceCheck.Checked); err != nil {
				log(fmt.Sprintf("Failed to create template %s: %v", image, err))
				dialog.ShowError(fmt.Errorf("failed to create template: %v", err), parent)
				return
			}
			log(fmt.Sprintf("Template %s created.", image))
			if onCreated != nil {
				onCreated()
			}
		}()
	}, parent)

	d.Show()
}

// ShowDeleteTemplateDialog shows a confirmation dialog for deleting a template
// via the D-Bus agent.
func ShowDeleteTemplateDialog(parent fyne.Window, model *AppModel, client Client, log func(string), onDeleted func()) {
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

			if client == nil {
				dialog.ShowError(fmt.Errorf("agent not connected"), parent)
				return
			}

			log(fmt.Sprintf("Deleting template %s...", tpl.Image))

			go func() {
				if err := client.DeleteTemplate(dir); err != nil {
					log(fmt.Sprintf("Failed to delete template %s: %v", tpl.Image, err))
					dialog.ShowError(fmt.Errorf("failed to delete template: %v", err), parent)
					return
				}
				log(fmt.Sprintf("Template %s deleted.", tpl.Image))

				// Remove from model.
				t := model.GetTemplates()
				delete(t, dir)
				model.SetTemplates(t)
				model.SelectTemplate("")

				if onDeleted != nil {
					onDeleted()
				}
			}()
		}, parent)

	d.Show()
}
