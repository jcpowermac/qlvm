package gui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestSpikeWindow verifies the minimal Fyne app creates a window with content.
func TestSpikeWindow(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	w := a.NewWindow("qlvm-gui spike")
	defer w.Close()

	btn := widget.NewButton("Works", func() {})
	w.SetContent(btn)

	// Verify the window has content
	if w.Content() == nil {
		t.Fatal("window has no content")
	}
}
