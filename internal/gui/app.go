package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// App is the main GUI application.
type App struct {
	fyneApp fyne.App
	window  fyne.Window
	model   *AppModel
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
	// Tab container for VMs/Templates.
	tabs := container.NewAppTabs(
		container.NewTabItem("VMs", widget.NewLabel("VM list placeholder")),
		container.NewTabItem("Templates", widget.NewLabel("Template list placeholder")),
	)
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.OnSelected = func(item *container.TabItem) {
		g.model.SetTab(item.Text)
	}

	// Action bar at the bottom.
	actionBar := container.NewHBox(
		widget.NewButton("Create VM", func() {}),
		layout.NewSpacer(),
		widget.NewButton("Refresh", func() {}),
	)

	return container.NewBorder(nil, actionBar, nil, nil, tabs)
}

// buildRightPane creates the right context-sensitive config panel.
func (g *App) buildRightPane() fyne.CanvasObject {
	return widget.NewLabel("Select a VM or template to view its configuration.")
}
