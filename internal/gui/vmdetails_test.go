package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// canvasChildren returns the direct child objects of a canvas object.
// It handles plain containers and the Scroll widget (whose child is a
// single Content, not a *fyne.Container) so tests can walk the whole tree.
func canvasChildren(c fyne.CanvasObject) []fyne.CanvasObject {
	switch v := c.(type) {
	case *fyne.Container:
		return v.Objects
	case *container.Scroll:
		if v.Content != nil {
			return []fyne.CanvasObject{v.Content}
		}
	}
	return nil
}

func findChecks(c fyne.CanvasObject) []*widget.Check {
	var out []*widget.Check
	if chk, ok := c.(*widget.Check); ok {
		out = append(out, chk)
	}
	for _, child := range canvasChildren(c) {
		out = append(out, findChecks(child)...)
	}
	return out
}

func findButton(c fyne.CanvasObject, label string) *widget.Button {
	if b, ok := c.(*widget.Button); ok && b.Text == label {
		return b
	}
	for _, child := range canvasChildren(c) {
		if b := findButton(child, label); b != nil {
			return b
		}
	}
	return nil
}

func findLabelText(c fyne.CanvasObject) []string {
	var out []string
	if l, ok := c.(*widget.Label); ok {
		out = append(out, l.Text)
	}
	for _, child := range canvasChildren(c) {
		out = append(out, findLabelText(child)...)
	}
	return out
}

func TestAppsTabPopulatesCheckboxes(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	fake := newFakeClient()
	fake.SetApps([]agent.AppInfo{
		{Name: "Code", Icon: "code", Whitelisted: true},
		{Name: "Firefox", Icon: "firefox", Whitelisted: false},
	})
	var logs []string
	tab := newAppsTab("web", fake, func(s string) { logs = append(logs, s) })

	checks := findChecks(tab)
	if len(checks) != 2 {
		t.Fatalf("expected 2 checkboxes, got %d", len(checks))
	}
	byName := map[string]*widget.Check{}
	for _, c := range checks {
		byName[c.Text] = c
	}
	if !byName["Code"].Checked {
		t.Error("Code should be checked (whitelisted)")
	}
	if byName["Firefox"].Checked {
		t.Error("Firefox should be unchecked")
	}
}

func TestAppsTabSaveSendsCheckedSet(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	fake := newFakeClient()
	fake.SetApps([]agent.AppInfo{
		{Name: "Code", Whitelisted: true},
		{Name: "Firefox", Whitelisted: false},
	})
	tab := newAppsTab("web", fake, func(string) {})

	checks := findChecks(tab)
	for _, c := range checks {
		if c.Text == "Firefox" {
			c.SetChecked(true)
		}
	}
	test.Tap(findButton(tab, "Save"))
	if len(fake.AppSaveCalls()) != 1 {
		t.Fatalf("expected 1 save call, got %d", len(fake.AppSaveCalls()))
	}
	got := fake.AppSaveCalls()[0]
	if len(got) != 2 || got[0] != "Code" || got[1] != "Firefox" {
		t.Errorf("save should send exactly the checked set [Code Firefox], got %v", got)
	}
}

func TestAppsTabSaveAllUncheckedSendsEmpty(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	fake := newFakeClient()
	fake.SetApps([]agent.AppInfo{{Name: "Code", Whitelisted: true}})
	tab := newAppsTab("web", fake, func(string) {})

	for _, c := range findChecks(tab) {
		c.SetChecked(false)
	}
	test.Tap(findButton(tab, "Save"))
	if len(fake.AppSaveCalls()) != 1 {
		t.Fatalf("expected 1 save call, got %d", len(fake.AppSaveCalls()))
	}
	got := fake.AppSaveCalls()[0]
	if len(got) != 0 {
		t.Errorf("save with nothing checked must send an empty list (all apps), got %v", got)
	}
}

func TestAppsTabSyncRefreshesList(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	fake := newFakeClient()
	fake.SetApps([]agent.AppInfo{{Name: "Old", Whitelisted: true}})
	fake.SetSyncResult(2, nil)
	fake.SetAppsAfterSync([]agent.AppInfo{
		{Name: "New1", Whitelisted: false},
		{Name: "New2", Whitelisted: false},
	})
	var logs []string
	tab := newAppsTab("web", fake, func(s string) { logs = append(logs, s) })

	test.Tap(findButton(tab, "Save")) // quiet any pending state
	test.Tap(findButton(tab, "Sync"))

	checks := findChecks(tab)
	if len(checks) != 2 {
		t.Fatalf("after sync expected 2 checkboxes (New1, New2), got %d", len(checks))
	}
	for _, c := range checks {
		if c.Checked {
			t.Errorf("newly synced apps have no whitelist file: %s should be unchecked", c.Text)
		}
	}
	labels := findLabelText(tab)
	found := false
	for _, l := range labels {
		if l == "synced 2 apps from web" {
			found = true
		}
	}
	if !found {
		t.Errorf("status label should show 'synced 2 apps from web', got %v", labels)
	}
	if len(logs) == 0 {
		t.Error("sync should append to the log")
	}
}

func TestAppsTabEmptyCacheShowsHint(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	fake := newFakeClient()
	fake.SetApps(nil)
	tab := newAppsTab("web", fake, func(string) {})

	if len(findChecks(tab)) != 0 {
		t.Fatalf("no checkboxes expected for an empty cache")
	}
	found := false
	for _, l := range findLabelText(tab) {
		if l == "No apps cached — click Sync" {
			found = true
		}
	}
	if !found {
		t.Error("empty cache must show the 'No apps cached — click Sync' hint")
	}
}
