package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// App is the main GUI application.
type App struct {
	fyneApp fyne.App
	window  fyne.Window
	model   *AppModel

	vmTable      *vmTable
	templateTable *templateTable
	actions      *Actions
	logPane      *LogPane
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
	g.actions = NewActions(model, x, o, v, cfg, coremapPath, g.logPane.Add)

	g.buildLayout()
	g.window.ShowAndRun()
	return g
}

// buildLayout creates the main window with menu bar and content area.
func (g *App) buildLayout() {
	// Menu bar.
	g.window.SetMainMenu(g.buildMenu())

	// Content: filter + tabbed table views.
	filter := widget.NewEntry()
	filter.PlaceHolder = "Filter VMs/Templates"
	filter.OnChanged = func(s string) {
		g.model.SetFilter(s)
		g.vmTable.Refresh()
		g.templateTable.Refresh()
	}

	// VM table view.
	g.vmTable = newVMTable(g.model, func(name string) {
		ShowVMDetailDialog(g.window, g.model, name)
	})

	// Template table view.
	g.templateTable = newTemplateTable(g.model, func(dir string) {
		ShowTemplateDetailDialog(g.window, g.model, dir)
	})

	tabs := container.NewAppTabs(
		container.NewTabItem("VMs", g.vmTable),
		container.NewTabItem("Templates", g.templateTable),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	tabs.OnSelected = func(item *container.TabItem) {
		g.model.SetTab(item.Text)
	}

	// Main content: filter at top, tabs below.
	content := container.NewBorder(
		filter, // top
		nil,    // bottom
		nil,    // left
		nil,    // right
		tabs,
	)

	g.window.SetContent(content)
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
		fyne.NewMenuItem("Start", func() { g.runAction(g.actions.Start) }),
		fyne.NewMenuItem("Stop", func() { g.runAction(g.actions.Stop) }),
		fyne.NewMenuItem("Kill", func() { g.runAction(g.actions.Kill) }),
		fyne.NewMenuItem("Restart", func() { g.runAction(g.actions.Restart) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Delete", func() { g.runAction(g.actions.Delete) }),
	)

	templateMenu := fyne.NewMenu("Template",
		fyne.NewMenuItem("Delete", func() {
			ShowDeleteTemplateDialog(g.window, g.model, g.logPane.Add, g.templateTable.Refresh)
		}),
	)

	viewMenu := fyne.NewMenu("View",
		fyne.NewMenuItem("Refresh", func() {
			g.vmTable.Refresh()
			g.templateTable.Refresh()
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Toggle Log", func() {
			// Toggle log pane visibility.
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
