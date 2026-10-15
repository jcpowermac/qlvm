// Package gui implements the qlvm Wayland GUI using Fyne. It connects to the
// qlvm-agent D-Bus daemon for VM management and runs waypipe locally for app
// projection.
package gui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// App is the main GUI application.
type App struct {
	fyneApp       fyne.App
	window        fyne.Window
	model         *AppModel
	client        Client
	cancelProgress func()
	vmTable       *vmTable
	templateTable *templateTable
	actions       *Actions
	logPane       *LogPane
	logVisible    bool
	statusLabel   *widget.Label
}

// New creates the GUI application with the given model. It connects to the
// qlvm-agent D-Bus daemon; if the daemon is not running, the GUI shows an
// error and the VM/template lists will be empty.
func New(model *AppModel) *App {
	a := app.NewWithID("io.qlvm.gui")
	w := a.NewWindow("qlvm GUI")
	w.Resize(fyne.NewSize(1000, 600))
	w.SetMaster()

	g := &App{
		fyneApp: a,
		window:  w,
		model:   model,
	}

	g.logPane = NewLogPane()
	model.SetLogCallback(g.logPane.Add)

	// Connect to the D-Bus daemon.
	c, err := agent.NewClient()
	if err != nil {
		model.Log(fmt.Sprintf("Error: %s", connectionErrorMessage(err)))
	} else {
		g.client = c
		g.cancelProgress = c.SubscribeProgress(func(id, message string) {
			if message != "" {
				model.Log(fmt.Sprintf("[%s] %s", id, message))
			}
		})
		g.actions = NewActions(model, c, g.logPane.Add)
	}

	g.buildLayout()
	g.window.ShowAndRun()
	return g
}

// NewWithDependencies creates the GUI application with an explicit client
// for dependency injection (used by tests).
func NewWithDependencies(model *AppModel, c Client) *App {
	a := app.NewWithID("io.qlvm.gui")
	w := a.NewWindow("qlvm GUI")
	w.Resize(fyne.NewSize(1000, 600))
	w.SetMaster()

	g := &App{
		fyneApp: a,
		window:  w,
		model:   model,
		client:  c,
	}

	g.logPane = NewLogPane()
	model.SetLogCallback(g.logPane.Add)
	if c != nil {
		g.cancelProgress = c.SubscribeProgress(func(id, message string) {
			if message != "" {
				model.Log(fmt.Sprintf("[%s] %s", id, message))
			}
		})
		g.actions = NewActions(model, c, g.logPane.Add)
	}

	g.buildLayout()
	g.window.ShowAndRun()
	return g
}

// buildLayout creates the main window with menu bar and content area.
func (g *App) buildLayout() {
	// Menu bar.
	g.window.SetMainMenu(g.buildMenu())

	// Build content off-screen first, then set it.
	go func() {
		time.Sleep(100 * time.Millisecond)
		fyne.Do(func() {
			// Content: filter + tabbed table views.
			filter := widget.NewEntry()
			filter.PlaceHolder = "Filter VMs/Templates"
			filter.OnChanged = func(s string) {
				g.model.SetFilter(s)
				g.vmTable.Refresh()
				g.templateTable.Refresh()
			}

			// VM table view.
			g.vmTable = newVMTable(g.model, g.window, g.actions, func(string) {
				// Single click just selects; right-click opens context menu.
			})

			// Template table view.
			g.templateTable = newTemplateTable(g.model, g.window, g.client, func(string) {
				// Single click just selects; right-click opens context menu.
			})

			tabs := container.NewAppTabs(
				container.NewTabItem("VMs", g.vmTable),
				container.NewTabItem("Templates", g.templateTable),
			)
			tabs.SetTabLocation(container.TabLocationTop)
			tabs.OnSelected = func(item *container.TabItem) {
				g.model.SetTab(item.Text)
			}

			// Log pane (initially hidden).
			g.logPane.Hide()

			// Status bar.
			g.statusLabel = widget.NewLabel(g.statusText())
			statusBar := container.NewBorder(
				nil, nil,
				widget.NewLabelWithStyle("qlvm", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				g.statusLabel,
				nil,
			)
			statusBar.Resize(fyne.NewSize(0, 24))

			// Main content: filter at top, tabs in middle, log + status at bottom.
			bottom := container.NewBorder(
				nil,
				statusBar,
				nil,
				nil,
				g.logPane,
			)
			content := container.NewBorder(
				filter, // top
				bottom, // bottom
				nil,    // left
				nil,    // right
				tabs,
			)

			g.window.SetContent(content)
			canvas.Refresh(content)
			g.window.RequestFocus()

			// Load initial data.
			go g.loadData()

			// Auto-refresh: periodically reload VMs and domain states.
			go g.startAutoRefresh()
		})
	}()
}

// startAutoRefresh periodically reloads VMs and domain states.
func (g *App) startAutoRefresh() {
	ticker := time.NewTicker(g.model.RunningInterval)
	defer ticker.Stop()
	for range ticker.C {
		go g.loadData()
	}
}

// statusText returns the status bar text.
func (g *App) statusText() string {
	parts := []string{}
	if g.client != nil {
		parts = append(parts, "Agent: connected")
	} else {
		parts = append(parts, "Agent: not connected")
	}
	vms := g.model.GetVMs()
	running := 0
	for _, info := range vms {
		if info.State == "running" {
			running++
		}
	}
	parts = append(parts, fmt.Sprintf("VMs: %d (%d running)", len(vms), running))
	return strings.Join(parts, "  |  ")
}

// loadData loads VMs and templates from the D-Bus agent.
func (g *App) loadData() {
	if g.client == nil {
		return
	}

	// Load VMs from the agent.
	vmInfos, err := g.client.ListVMs()
	if err != nil {
		g.model.Log(fmt.Sprintf("Failed to list VMs: %v", err))
	} else {
		vms := make(map[string]*agent.VMInfo, len(vmInfos))
		for i := range vmInfos {
			info := vmInfos[i]
			vms[info.Name] = &info
		}
		g.model.SetVMs(vms)
	}

	// Load templates from the agent.
	tplInfos, err := g.client.ListTemplates()
	if err != nil {
		g.model.Log(fmt.Sprintf("Failed to list templates: %v", err))
	} else {
		tpls := make(map[string]*agent.TemplateInfo, len(tplInfos))
		for i := range tplInfos {
			info := tplInfos[i]
			tpls[info.Dir] = &info
		}
		g.model.SetTemplates(tpls)
	}

	// Refresh the UI on the main thread.
	fyne.Do(func() {
		if g.vmTable != nil {
			g.vmTable.Refresh()
		}
		if g.templateTable != nil {
			g.templateTable.Refresh()
		}
		if g.statusLabel != nil {
			g.statusLabel.SetText(g.statusText())
		}
	})
}

// buildMenu creates the application menu bar.
func (g *App) buildMenu() *fyne.MainMenu {
	fileMenu := fyne.NewMenu("File",
		fyne.NewMenuItem("Create VM", func() {
			ShowCreateVMDialog(g.window, g.model, g.client, g.logPane.Add, g.vmTable.Refresh)
		}),
		fyne.NewMenuItem("Create Template", func() {
			ShowCreateTemplateDialog(g.window, g.model, g.client, g.logPane.Add, g.templateTable.Refresh)
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", func() {
			g.window.Close()
		}),
	)

	vmMenu := fyne.NewMenu("VM",
		fyne.NewMenuItem("Details...", func() {
			name := g.model.SelectedVMName()
			if name != "" {
				ShowVMDetailDialog(g.window, g.model, name)
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Start", func() { g.runAction(g.actions.Start) }),
		fyne.NewMenuItem("Stop", func() { g.runAction(g.actions.Stop) }),
		fyne.NewMenuItem("Kill", func() { g.runAction(g.actions.Kill) }),
		fyne.NewMenuItem("Restart", func() { g.runAction(g.actions.Restart) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Delete", func() { g.runAction(g.actions.Delete) }),
	)

	templateMenu := fyne.NewMenu("Template",
		fyne.NewMenuItem("Details...", func() {
			dir := g.model.SelectedTemplateDir()
			if dir != "" {
				ShowTemplateDetailDialog(g.window, g.model, dir)
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Delete", func() {
			ShowDeleteTemplateDialog(g.window, g.model, g.client, g.logPane.Add, g.templateTable.Refresh)
		}),
	)

	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("Refresh", func() {
			go g.loadData()
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Toggle Log", func() {
			if g.logVisible {
				g.logPane.Hide()
				g.logVisible = false
			} else {
				g.logPane.Show()
				g.logVisible = true
			}
			canvas.Refresh(g.window.Content())
		}),
	)

	return fyne.NewMainMenu(fileMenu, vmMenu, templateMenu, viewMenu)
}

// connectionErrorMessage returns a user-friendly error message for D-Bus
// connection failures. If the daemon is not running, it provides
// instructions to start it. Otherwise it reports the underlying error.
func connectionErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "daemon not running") {
		return "qlvm-agent is not running; run `sudo systemctl start qlvm-agent`"
	}
	return fmt.Sprintf("cannot connect to qlvm-agent daemon: %v", err)
}

// runAction runs an action and shows errors in a dialog.
func (g *App) runAction(fn func() error) {
	if err := fn(); err != nil {
		dialog.ShowError(err, g.window)
	}
}
