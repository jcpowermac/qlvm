package gui

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// ShowVMDetailDialog shows a tabbed dialog with full VM details and editing.
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

	// --- Identity tab (read-only) ---
	identity := widget.NewForm(
		widget.NewFormItem("Name", widget.NewLabel(meta.Name)),
		widget.NewFormItem("Type", widget.NewLabel(meta.Type)),
		widget.NewFormItem("Domain", widget.NewLabel(meta.Domain)),
		widget.NewFormItem("IP", widget.NewLabel(meta.IP)),
		widget.NewFormItem("MAC", widget.NewLabel(meta.MAC)),
		widget.NewFormItem("UUID", widget.NewLabel(meta.UUID)),
	)

	// --- Resources tab (editable) ---
	memoryEntry := widget.NewEntry()
	memoryEntry.SetText(strconv.Itoa(meta.MemoryMB))

	vcpuEntry := widget.NewEntry()
	vcpuEntry.SetText(strconv.Itoa(meta.VCPUs))

	coresLabel := widget.NewLabel(formatCores(meta.Cores))

	resources := widget.NewForm(
		widget.NewFormItem("Memory (MB)", memoryEntry),
		widget.NewFormItem("vCPUs", vcpuEntry),
		widget.NewFormItem("Cores", coresLabel),
		widget.NewFormItem("State", widget.NewLabel(state)),
	)

	// --- Storage tab (read-only) ---
	storage := widget.NewForm(
		widget.NewFormItem("Image", widget.NewLabel(meta.Image)),
		widget.NewFormItem("Digest", widget.NewLabel(truncateDigest(meta.Digest))),
	)
	if meta.WorkspaceSizeMB > 0 {
		storage.Append("Workspace (MB)", widget.NewLabel(strconv.Itoa(meta.WorkspaceSizeMB)))
	}
	if len(meta.Mounts) > 0 {
		mountsText := ""
		for _, m := range meta.Mounts {
			mountsText += fmt.Sprintf("%s → %s\n", m.Host, m.Guest)
		}
		storage.Append("Mounts", widget.NewLabel(mountsText))
	}

	// --- Status tab (read-only) ---
	status := widget.NewForm(
		widget.NewFormItem("Created", widget.NewLabel(meta.Created.Format("2006-01-02 15:04:05"))),
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

	// --- Save button ---
	saveBtn := widget.NewButton("Save", func() {
		// Validate memory.
		mem, err := strconv.Atoi(memoryEntry.Text)
		if err != nil || mem <= 0 {
			dialog.ShowError(fmt.Errorf("invalid memory: %s", memoryEntry.Text), parent)
			return
		}

		// Validate vCPUs.
		vcpus, err := strconv.Atoi(vcpuEntry.Text)
		if err != nil || vcpus <= 0 {
			dialog.ShowError(fmt.Errorf("invalid vCPUs: %s", vcpuEntry.Text), parent)
			return
		}

		// Check if VM is running (resource changes require stopped VM).
		if state == "running" {
			if mem != meta.MemoryMB || vcpus != meta.VCPUs {
				dialog.ShowConfirm("VM is running",
					"Changing resources requires the VM to be stopped. Save anyway (changes apply on next start)?",
					func(confirm bool) {
						if confirm {
							saveVMChanges(model, meta, mem, vcpus, parent)
						}
					}, parent)
				return
			}
		}

		saveVMChanges(model, meta, mem, vcpus, parent)
	})

	content := container.NewBorder(
		tabs,   // top
		saveBtn, // bottom
		nil,    // left
		nil,    // right
		nil,
	)

	d := dialog.NewCustom("VM: "+meta.Name, "Close", container.NewScroll(content), parent)
	d.Resize(fyne.NewSize(500, 500))
	d.Show()
}

// saveVMChanges saves edited VM metadata.
func saveVMChanges(model *AppModel, meta *vm.Meta, memoryMB, vcpus int, parent fyne.Window) {
	// Update the meta in memory.
	meta.MemoryMB = memoryMB
	meta.VCPUs = vcpus

	// Save to disk.
	vmDir := vmDirOf(meta.Name)
	if err := meta.Save(vmDir); err != nil {
		dialog.ShowError(fmt.Errorf("failed to save VM metadata: %v", err), parent)
		model.Log(fmt.Sprintf("Failed to save VM %s metadata: %v", meta.Name, err))
		return
	}

	// Update the model.
	vms := model.GetVMs()
	vms[meta.Name] = meta
	model.SetVMs(vms)

	model.Log(fmt.Sprintf("VM %s updated: memory=%dMB, vcpus=%d", meta.Name, memoryMB, vcpus))
}

// vmDirOf returns the VM's directory path.
func vmDirOf(name string) string {
	return fmt.Sprintf("%s/vms/%s", installRoot, name)
}
