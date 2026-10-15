package gui

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// ShowCreateVMDialog shows the create VM dialog and creates the VM on OK
// via the D-Bus agent.
func ShowCreateVMDialog(parent fyne.Window, model *AppModel, client Client, log func(string), onCreated func()) {
	tpls := model.GetTemplates()

	// Build template options.
	templateOptions := []string{}
	for _, tpl := range tpls {
		templateOptions = append(templateOptions, tpl.Image)
	}
	if len(templateOptions) == 0 {
		dialog.ShowError(fmt.Errorf("no templates available. Create a template first."), parent)
		return
	}

	// Type options.
	typeOptions := []string{"app", "disposable", "agent"}

	// Form fields.
	nameEntry := widget.NewEntry()
	nameEntry.PlaceHolder = "vm-name"

	templateSelect := widget.NewSelect(templateOptions, func(s string) {})
	templateSelect.SetSelected(templateOptions[0])

	typeSelect := widget.NewSelect(typeOptions, func(s string) {})
	typeSelect.SetSelected("app")

	memoryEntry := widget.NewEntry()
	memoryEntry.PlaceHolder = "1024"

	vcpuEntry := widget.NewEntry()
	vcpuEntry.PlaceHolder = "1"

	workspaceEntry := widget.NewEntry()
	workspaceEntry.PlaceHolder = "0 (agent VMs only)"
	workspaceEntry.SetText("0")

	form := widget.NewForm(
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Template", templateSelect),
		widget.NewFormItem("Type", typeSelect),
		widget.NewFormItem("Memory (MB)", memoryEntry),
		widget.NewFormItem("vCPUs", vcpuEntry),
		widget.NewFormItem("Workspace (MB)", workspaceEntry),
	)

	d := dialog.NewCustomConfirm("Create VM", "Create", "Cancel", form, func(confirmed bool) {
		if !confirmed {
			return
		}

		name := nameEntry.Text
		if name == "" {
			dialog.ShowError(fmt.Errorf("VM name is required"), parent)
			return
		}

		mem, err := strconv.Atoi(memoryEntry.Text)
		if err != nil || mem <= 0 {
			mem = 1024 // default
		}

		vcpus, err := strconv.Atoi(vcpuEntry.Text)
		if err != nil || vcpus <= 0 {
			vcpus = 1 // default
		}

		ws, err := strconv.Atoi(workspaceEntry.Text)
		if err != nil || ws < 0 {
			ws = 0
		}

		if client == nil {
			dialog.ShowError(fmt.Errorf("agent not connected"), parent)
			return
		}

		log(fmt.Sprintf("Creating VM %s (type=%s, mem=%dMB, vcpus=%d)...",
			name, typeSelect.Selected, mem, vcpus))

		opts := &agent.CreateVMOptions{
			Name:     name,
			Template: templateSelect.Selected,
			Type:     typeSelect.Selected,
			MemoryMB: uint64(mem),
			VCPUs:    uint8(vcpus),
		}
		_ = ws // workspace size not supported via agent yet

		// VM creation may take a while; run it in a goroutine.
		go func() {
			if _, err := client.CreateVM(opts); err != nil {
				log(fmt.Sprintf("Failed to create VM %s: %v", name, err))
				dialog.ShowError(fmt.Errorf("failed to create VM: %v", err), parent)
				return
			}
			log(fmt.Sprintf("VM %s created.", name))
			if onCreated != nil {
				onCreated()
			}
		}()
	}, parent)

	d.Show()
}
