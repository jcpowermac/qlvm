package gui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// Run creates and runs the minimal Fyne application for the spike.
func Run() {
	a := app.NewWithID("io.qlvm.gui")
	w := a.NewWindow("qlvm-gui spike")
	w.Resize(fyne.NewSize(900, 600))

	fmt.Println("spike: window created, setting content")

	label := widget.NewLabelWithStyle("Fyne spike on Wayland — rendering test", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	content := container.NewVBox(
		label,
		widget.NewSeparator(),
		widget.NewLabel("If you can read this, Fyne works on Wayland."),
		widget.NewButton("Close", func() {
			w.Close()
		}),
	)

	w.SetContent(content)
	fmt.Println("spike: content set, showing window")

	// Auto-close after 5s so it doesn't hang if the window is invisible.
	go func() {
		time.Sleep(5 * time.Second)
		fmt.Println("spike: auto-closing after 5s")
		fyne.Do(func() {
			w.Close()
		})
	}()

	w.ShowAndRun()
	fmt.Println("spike: window closed")
}
