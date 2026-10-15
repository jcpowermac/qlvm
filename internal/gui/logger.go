package gui

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// LogPane displays operation output in a scrollable, monospace text area.
type LogPane struct {
	*fyne.Container
	mu      sync.Mutex
	entries []string
	entry   *widget.Entry
}

// NewLogPane creates a new log pane.
func NewLogPane() *LogPane {
	l := &LogPane{}

	l.entry = widget.NewMultiLineEntry()
	l.entry.Wrapping = fyne.TextWrapWord
	l.entry.SetPlaceHolder("Operation log")
	l.entry.Disable() // Read-only.

	scroll := container.NewScroll(l.entry)

	l.Container = container.New(layout.NewVBoxLayout(), scroll)

	return l
}

// Add appends a line to the log.
func (l *LogPane) Add(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format("15:04:05")
	line := fmt.Sprintf("[%s] %s", timestamp, msg)
	l.entries = append(l.entries, line)

	// Update the entry text.
	text := ""
	for _, e := range l.entries {
		text += e + "\n"
	}
	l.entry.SetText(text)

	// Scroll to the bottom.
	// Note: Fyne doesn't have a direct scroll-to-bottom API, so we rely on
	// the entry's internal scrolling when text is appended.
}

// Clear removes all log entries.
func (l *LogPane) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = nil
	l.entry.SetText("")
}
