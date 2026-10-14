package gui

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/template"
)

// ShowCreateVMDialog shows the create VM dialog and creates the VM on OK.
func ShowCreateVMDialog(parent fyne.Window, model *AppModel, log func(string), onCreated func()) {
	cfg := model.GetConfig()
	tpls := model.GetTemplates()

	// Build template options.
	templateOptions := []string{}
	templateMap := map[string]*template.Template{}
	for _, tpl := range tpls {
		templateOptions = append(templateOptions, tpl.Image)
		templateMap[tpl.Image] = tpl
	}
	if len(templateOptions) == 0 {
		templateOptions = []string{"(no templates available)"}
	}

	// Build domain options.
	domainOptions := []string{}
	for _, d := range cfg.Domains {
		domainOptions = append(domainOptions, d.Name)
	}
	if len(domainOptions) == 0 {
		domainOptions = []string{"work"} // default
	}

	// Type options.
	typeOptions := []string{"app", "disposable", "agent"}

	// Form fields.
	nameEntry := widget.NewEntry()
	nameEntry.PlaceHolder = "vm-name"

	templateSelect := widget.NewSelect(templateOptions, func(s string) {})
	templateSelect.SetSelected(templateOptions[0])

	domainSelect := widget.NewSelect(domainOptions, func(s string) {})
	domainSelect.SetSelected(domainOptions[0])

	typeSelect := widget.NewSelect(typeOptions, func(s string) {})
	typeSelect.SetSelected("app")

	memoryEntry := widget.NewEntry()
	memoryEntry.PlaceHolder = "1024"
	memoryEntry.SetText(strconv.Itoa(cfg.VM.MemoryMB))

	vcpuEntry := widget.NewEntry()
	vcpuEntry.PlaceHolder = "1"
	vcpuEntry.SetText(strconv.Itoa(cfg.VM.VCPUs))

	workspaceEntry := widget.NewEntry()
	workspaceEntry.PlaceHolder = "0 (agent VMs only)"
	workspaceEntry.SetText("0")

	form := widget.NewForm(
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Template", templateSelect),
		widget.NewFormItem("Domain", domainSelect),
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
			dialog.ShowError(fmt.Errorf("invalid memory: %s", memoryEntry.Text), parent)
			return
		}

		vcpus, err := strconv.Atoi(vcpuEntry.Text)
		if err != nil || vcpus <= 0 {
			dialog.ShowError(fmt.Errorf("invalid vCPUs: %s", vcpuEntry.Text), parent)
			return
		}

		ws, err := strconv.Atoi(workspaceEntry.Text)
		if err != nil || ws < 0 {
			dialog.ShowError(fmt.Errorf("invalid workspace size: %s", workspaceEntry.Text), parent)
			return
		}

		tplRef := templateSelect.Selected
		if _, ok := templateMap[tplRef]; !ok {
			dialog.ShowError(fmt.Errorf("no template available"), parent)
			return
		}

		log(fmt.Sprintf("Creating VM %s (type=%s, domain=%s, mem=%dMB, vcpus=%d)...",
			name, typeSelect.Selected, domainSelect.Selected, mem, vcpus))

		// Note: actual VM creation requires vm.Create with full CreateDeps.
		// Wired up in the Actions struct when dependencies are available.
		log(fmt.Sprintf("VM %s creation requested (pending full wiring)", name))

		if onCreated != nil {
			onCreated()
		}
	}, parent)

	d.Show()
}
