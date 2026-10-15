package gui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ShowVMDetailDialog shows a tabbed dialog with VM details.
// Resource editing is not available via the D-Bus agent yet; the Resources
// tab is read-only.
func ShowVMDetailDialog(parent fyne.Window, model *AppModel, name string) {
	vms := model.GetVMs()

	info, ok := vms[name]
	if !ok {
		dialog.ShowError(fmt.Errorf("VM %q not found", name), parent)
		return
	}

	state := info.State
	if state == "" {
		state = "stopped"
	}

	// --- Identity tab (read-only) ---
	identity := widget.NewForm(
		widget.NewFormItem("Name", widget.NewLabel(info.Name)),
		widget.NewFormItem("Type", widget.NewLabel(info.Type)),
		widget.NewFormItem("Domain", widget.NewLabel(info.Domain)),
	)

	// --- Resources tab (read-only; editing not supported via agent yet) ---
	resources := widget.NewForm(
		widget.NewFormItem("Memory (MB)", widget.NewLabel(fmt.Sprintf("%d", info.MemoryMB))),
		widget.NewFormItem("vCPUs", widget.NewLabel(fmt.Sprintf("%d", info.VCPUs))),
		widget.NewFormItem("State", widget.NewLabel(state)),
	)

	// --- Storage tab (read-only) ---
	storage := widget.NewForm(
		widget.NewFormItem("Image", widget.NewLabel(info.Image)),
		widget.NewFormItem("Digest", widget.NewLabel(truncateDigest(info.Digest))),
	)
	if len(info.Mounts) > 0 {
		mountsText := ""
		for _, m := range info.Mounts {
			mountsText += m + "\n"
		}
		storage.Append("Mounts", widget.NewLabel(mountsText))
	}

	// --- Status tab (read-only) ---
	created := "unknown"
	if info.CreatedAt > 0 {
		created = time.Unix(info.CreatedAt, 0).Format("2006-01-02 15:04:05")
	}
	status := widget.NewForm(
		widget.NewFormItem("Created", widget.NewLabel(created)),
		widget.NewFormItem("State", widget.NewLabel(state)),
	)

	// --- Tabs ---
	tabs := container.NewAppTabs(
		container.NewTabItem("Identity", identity),
		container.NewTabItem("Resources", resources),
		container.NewTabItem("Storage", storage),
		container.NewTabItem("Status", status),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	content := container.NewScroll(tabs)

	d := dialog.NewCustom("VM: "+info.Name, "Close", content, parent)
	d.Resize(fyne.NewSize(500, 500))
	d.Show()
}
