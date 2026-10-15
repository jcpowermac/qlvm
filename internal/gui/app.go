package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// App is the main GUI application.
type App struct {
	fyneApp       fyne.App
	window        fyne.Window
	model         *AppModel
	vmTable       *vmTable
	templateTable *templateTable
	actions       *Actions
	logPane       *LogPane
	logVisible    bool
	statusLabel   *widget.Label
}

// New creates the GUI application with the given model.
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

	// Create Xen connection and Actions.
	x, err := xenctl.New()
	if err != nil {
		model.Log(fmt.Sprintf("Warning: cannot connect to Xen: %v", err))
	} else {
		cfg := model.GetConfig()
		if cfg == nil {
			cfg, err = config.Load(defaultConfigPath)
			if err != nil {
				model.Log(fmt.Sprintf("Warning: cannot load config: %v", err))
			} else {
				model.SetConfig(cfg)
			}
		}
		g.actions = NewActions(model, x, nil, nil, cfg, "", g.logPane.Add)
	}

	g.buildLayout()
	g.window.ShowAndRun()
	return g
}

// NewWithDependencies creates the GUI application with explicit control
// plane dependencies.
func NewWithDependencies(model *AppModel, x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath string) *App {
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
	g.actions = NewActions(model, x, o, v, cfg, coremapPath, g.logPane.Add)

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
			g.vmTable = newVMTable(g.model, g.window, g.actions, func(name string) {
				// Single click just selects; right-click opens context menu.
			})

			// Template table view.
			g.templateTable = newTemplateTable(g.model, g.window, func(dir string) {
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
	if g.actions != nil && g.actions.xen != nil {
		parts = append(parts, "Xen: connected")
	} else {
		parts = append(parts, "Xen: not connected")
	}
	vms := g.model.GetVMs()
	states := g.model.GetDomainStates()
	running := 0
	for name := range vms {
		if s, ok := states[name]; ok && s == "running" {
			running++
		}
	}
	parts = append(parts, fmt.Sprintf("VMs: %d (%d running)", len(vms), running))
	return strings.Join(parts, "  |  ")
}

// defaultConfigPath is the qlvm configuration file.
const defaultConfigPath = "/etc/qvm/qlvm.yaml"

// loadData loads VMs, templates, and domain states from the control planes.
func (g *App) loadData() {
	// Load config if not already loaded.
	if g.model.GetConfig() == nil {
		cfg, err := config.Load(defaultConfigPath)
		if err != nil {
			g.model.Log(fmt.Sprintf("Failed to load config: %v", err))
		} else {
			g.model.SetConfig(cfg)
		}
	}

	// Load VM metadata.
	vms := make(map[string]*vm.Meta)
	entries, err := os.ReadDir(filepath.Join(installRoot, "vms"))
	if err != nil {
		g.model.Log(fmt.Sprintf("Failed to read VMs: %v", err))
	} else {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			meta, err := vm.LoadMeta(filepath.Join(installRoot, "vms", e.Name()))
			if err == nil {
				vms[meta.Name] = meta
			}
		}
	}
	g.model.SetVMs(vms)

	// Load template metadata.
	tpls := make(map[string]*template.Template)
	entries, err = os.ReadDir(filepath.Join(installRoot, "templates"))
	if err != nil {
		g.model.Log(fmt.Sprintf("Failed to read templates: %v", err))
	} else {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			tpl, err := template.LoadMeta(filepath.Join(installRoot, "templates", e.Name()))
			if err == nil {
				tpls[e.Name()] = tpl
			}
		}
	}
	g.model.SetTemplates(tpls)

	// Load domain states from Xen.
	states := make(map[string]string)
	if g.actions != nil && g.actions.xen != nil {
		domains, err := g.actions.xen.List()
		if err != nil {
			g.model.Log(fmt.Sprintf("Failed to list Xen domains: %v", err))
		}
		for _, d := range domains {
			states[d.Name] = d.State
		}
	}
	g.model.SetDomainStates(states)

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
			ShowCreateVMDialog(g.window, g.model, g.logPane.Add, g.vmTable.Refresh)
		}),
		fyne.NewMenuItem("Create Template", func() {
			ShowCreateTemplateDialog(g.window, g.model, g.logPane.Add, g.templateTable.Refresh)
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
			ShowDeleteTemplateDialog(g.window, g.model, g.logPane.Add, g.templateTable.Refresh)
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

// runAction runs an action and shows errors in a dialog.
func (g *App) runAction(fn func() error) {
	if err := fn(); err != nil {
		dialog.ShowError(err, g.window)
	}
}
