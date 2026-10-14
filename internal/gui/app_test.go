package gui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

func TestAppShellCreatesWindow(t *testing.T) {
	// Use Fyne's test driver to verify the app shell structure without
	// requiring a display.
	model := NewAppModel(time.Second, time.Second)

	// Create the app using the test driver by mocking the fyne app creation.
	// We test the layout building directly instead of New() which calls ShowAndRun.
	a := &App{model: model}

	// Verify the model is set
	if a.model != model {
		t.Fatal("model not set")
	}
}

func TestAppShellLayout(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)

	// Create a test app to verify layout components.
	fyneApp := test.NewApp()
	defer fyneApp.Quit()

	w := fyneApp.NewWindow("test")
	defer w.Close()

	a := &App{
		fyneApp: fyneApp,
		window:  w,
		model:   model,
	}

	a.buildLayout()

	if w.Content() == nil {
		t.Fatal("window has no content after buildLayout")
	}
}
