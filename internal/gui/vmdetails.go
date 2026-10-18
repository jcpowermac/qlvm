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
// tab is read-only. The Apps tab (whitelist) needs the client; addLog
// receives sync/save feedback (may be nil).
func ShowVMDetailDialog(parent fyne.Window, model *AppModel, client Client, addLog func(string), name string) {
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

	// --- Apps tab (desktop cache + per-VM whitelist) ---
	apps := newAppsTab(name, client, addLog)

	// --- Tabs ---
	tabs := container.NewAppTabs(
		container.NewTabItem("Identity", identity),
		container.NewTabItem("Resources", resources),
		container.NewTabItem("Storage", storage),
		container.NewTabItem("Apps", apps),
		container.NewTabItem("Status", status),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	content := container.NewScroll(tabs)

	d := dialog.NewCustom("VM: "+info.Name, "Close", content, parent)
	d.Resize(fyne.NewSize(500, 500))
	d.Show()
}

// appsTabState is the mutable state behind the Apps tab, kept separate from
// the widget tree so the populate/save logic is unit-testable.
type appsTabState struct {
	vmName string
	client Client
	addLog func(string)
	status *widget.Label
	box    *fyne.Container
	checks map[string]*widget.Check
}

// newAppsTab builds the "Apps" tab of the VM details dialog: a Sync button
// that refreshes the desktop cache through the daemon, a checkbox list of
// all cached apps (checked = whitelisted), and a Save button that applies
// the checkbox state as the new whitelist. Saving with everything
// unchecked sends an empty list, which is the "all apps visible" state.
func newAppsTab(vmName string, client Client, addLog func(string)) fyne.CanvasObject {
	if addLog == nil {
		addLog = func(string) {}
	}
	st := &appsTabState{vmName: vmName, client: client, addLog: addLog, checks: map[string]*widget.Check{}}
	st.status = widget.NewLabel("")
	st.box = container.NewVBox()

	syncBtn := widget.NewButton("Sync", func() {})
	syncBtn.OnTapped = func() {
		syncBtn.Disable()
		st.status.SetText("syncing…")
		n, err := client.SyncApps(vmName)
		syncBtn.Enable()
		if err != nil {
			st.status.SetText(err.Error())
			addLog(fmt.Sprintf("apps sync %s: %v", vmName, err))
			return
		}
		st.load()
		st.status.SetText(fmt.Sprintf("synced %d apps from %s", n, vmName))
		addLog(fmt.Sprintf("apps sync %s: synced %d apps", vmName, n))
	}

	saveBtn := widget.NewButton("Save", func() {})
	saveBtn.OnTapped = func() {
		saveBtn.Disable()
		names := st.checkedNames()
		err := client.SetAppWhitelist(vmName, names)
		saveBtn.Enable()
		if err != nil {
			st.status.SetText(err.Error())
			addLog(fmt.Sprintf("apps save %s: %v", vmName, err))
			return
		}
		st.status.SetText("saved")
		addLog(fmt.Sprintf("apps save %s: %d app(s) whitelisted", vmName, len(names)))
	}

	st.load()

	return container.NewBorder(
		container.NewHBox(syncBtn, st.status),
		container.NewHBox(saveBtn),
		nil, nil,
		container.NewScroll(st.box),
	)
}

// load refreshes the checkbox list from the daemon's cached app set.
func (st *appsTabState) load() {
	infos, err := st.client.ListApps(st.vmName)
	if err != nil {
		st.status.SetText(err.Error())
		st.addLog(fmt.Sprintf("apps %s: %v", st.vmName, err))
		return
	}
	st.box.Objects = st.box.Objects[:0]
	st.checks = map[string]*widget.Check{}
	if len(infos) == 0 {
		st.box.Add(widget.NewLabel("No apps cached — click Sync"))
		return
	}
	for _, a := range infos {
		c := widget.NewCheck(a.Name, func(bool) {})
		c.SetChecked(a.Whitelisted)
		st.checks[a.Name] = c
		st.box.Add(c)
	}
}

// checkedNames returns the names of the checked apps, in list order.
func (st *appsTabState) checkedNames() []string {
	names := make([]string, 0, len(st.checks))
	for _, c := range st.box.Objects {
		if chk, ok := c.(*widget.Check); ok && chk.Checked {
			names = append(names, chk.Text)
		}
	}
	return names
}
