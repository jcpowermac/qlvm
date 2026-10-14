package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
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

	vmList         *VMListView
	templateList   *TemplateListView
	configPanel    *ConfigPanel
	actions        *Actions
	logPane        *LogPane
}

// New creates the GUI application with the given model.
func New(model *AppModel) *App {
	a := app.NewWithID("io.qlvm.gui")
	w := a.NewWindow("qlvm GUI")
	w.Resize(fyne.NewSize(900, 600))
	w.SetMaster()

	g := &App{
		fyneApp: a,
		window:  w,
		model:   model,
	}

	g.buildLayout()
	g.window.ShowAndRun()
	return g
}

// NewWithDependencies creates the GUI application with explicit control
// plane dependencies (for testing or when the control planes are already
// initialized).
func NewWithDependencies(model *AppModel, x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath string) *App {
	a := app.NewWithID("io.qlvm.gui")
	w := a.NewWindow("qlvm GUI")
	w.Resize(fyne.NewSize(900, 600))
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

// buildLayout creates the main window layout with left pane (VM/template
// list) and right pane (context-sensitive config).
func (g *App) buildLayout() {
	// Left pane: navigation tabs + list + action bar.
	leftPane := g.buildLeftPane()

	// Right pane: context-sensitive config panel.
	rightPane := g.buildRightPane()

	// Split the window into left and right panes.
	split := container.NewHSplit(leftPane, rightPane)
	split.SetOffset(0.35)

	g.window.SetContent(split)
}

// buildLeftPane creates the left navigation pane with tabs and a list.
func (g *App) buildLeftPane() fyne.CanvasObject {
	// Filter/search box.
	filter := widget.NewEntry()
	filter.PlaceHolder = "Filter VMs/Templates"
	filter.OnChanged = func(s string) {
		g.model.SetFilter(s)
		g.vmList.Refresh()
	}

	// VM list view.
	g.vmList = NewVMListView(g.model)
	g.vmList.OnSelectionChange = func(name string) {
		g.configPanel.Refresh()
	}

	// Template list view.
	g.templateList = NewTemplateListView(g.model)
	g.templateList.OnSelectionChange = func(dir string) {
		g.configPanel.Refresh()
	}

	// Tab container for VMs/Templates.
	tabs := container.NewAppTabs(
		container.NewTabItem("VMs", g.vmList),
		container.NewTabItem("Templates", g.templateList),
	)
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.OnSelected = func(item *container.TabItem) {
		g.model.SetTab(item.Text)
		g.configPanel.Refresh()
	}

	// Action bar at the bottom with lifecycle buttons.
	startBtn := widget.NewButton("Start", func() { g.runAction(g.actions.Start) })
	stopBtn := widget.NewButton("Stop", func() { g.runAction(g.actions.Stop) })
	killBtn := widget.NewButton("Kill", func() { g.runAction(g.actions.Kill) })
	deleteBtn := widget.NewButton("Delete", func() { g.runAction(g.actions.Delete) })
	createBtn := widget.NewButton("Create VM", func() {
		ShowCreateVMDialog(g.window, g.model, g.logPane.Add, func() {
			// Refresh the VM list after creation.
			g.vmList.Refresh()
		})
	})

	actionBar := container.NewHBox(
		createBtn,
		startBtn,
		stopBtn,
		killBtn,
		deleteBtn,
		layout.NewSpacer(),
		widget.NewButton("Refresh", func() {
			g.vmList.Refresh()
			g.configPanel.Refresh()
		}),
	)

	return container.NewBorder(filter, actionBar, nil, nil, tabs)
}

// runAction runs an action and shows errors in a dialog.
func (g *App) runAction(fn func() error) {
	if err := fn(); err != nil {
		dialog.ShowError(err, g.window)
	}
}

// buildRightPane creates the right context-sensitive config panel.
func (g *App) buildRightPane() fyne.CanvasObject {
	g.configPanel = NewConfigPanel(g.model)
	return g.configPanel
}
