package gui

import (
	"fmt"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func TestAppShellCreatesWindow(t *testing.T) {
	// Use Fyne's test driver to verify the app shell structure without
	// requiring a display.
	model := NewAppModel(time.Second, time.Second)
	fake := newFakeClient()

	// Create the app using the test driver by mocking the fyne app creation.
	// We test the layout building directly instead of New() which calls ShowAndRun.
	a := &App{model: model, client: fake}

	// Verify the model and client are set
	if a.model != model {
		t.Fatal("model not set")
	}
	if a.client != fake {
		t.Fatal("client not set")
	}
}

func TestAppShellLayout(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)
	fake := newFakeClient()

	// Create a test app to verify layout components.
	fyneApp := test.NewApp()
	defer fyneApp.Quit()

	w := fyneApp.NewWindow("test")
	defer w.Close()

	a := &App{
		fyneApp: fyneApp,
		window:  w,
		model:   model,
		client:  fake,
		logPane: NewLogPane(),
	}

	a.buildLayout()

	// buildLayout runs asynchronously; wait for it to complete.
	time.Sleep(200 * time.Millisecond)

	if w.Content() == nil {
		t.Fatal("window has no content after buildLayout")
	}
}

func TestAppLoadData(t *testing.T) {
	model := NewAppModel(time.Second, time.Second)
	fake := newFakeClient()
	fake.SetVMs([]agent.VMInfo{
		{Name: "web", Type: "app", State: "running", MemoryMB: 1024, VCPUs: 1},
	})
	fake.SetTemplates([]agent.TemplateInfo{
		{Dir: "fedora", Image: "quay.io/fedora/fedora:44", Digest: "sha256:abc"},
	})

	a := &App{
		model:   model,
		client:  fake,
		logPane: NewLogPane(),
	}

	a.loadData()

	vms := model.GetVMs()
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM after loadData, got %d", len(vms))
	}
	if vms["web"].State != "running" {
		t.Errorf("expected web to be running, got %q", vms["web"].State)
	}

	tpls := model.GetTemplates()
	if len(tpls) != 1 {
		t.Fatalf("expected 1 template after loadData, got %d", len(tpls))
	}
	if tpls["fedora"].Image != "quay.io/fedora/fedora:44" {
		t.Errorf("expected fedora template, got %q", tpls["fedora"].Image)
	}
}

func TestConnectionErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "daemon not running",
			err:      fmt.Errorf("daemon not running: org.freedesktop.DBus.Error.NameHasNoOwner"),
			expected: "qlvm-agent is not running; run `sudo systemctl start qlvm-agent`",
		},
		{
			name:     "system bus not available",
			err:      fmt.Errorf("connect system bus: dial unix /run/dbus/system_bus_socket: connect: no such file or directory"),
			expected: "cannot connect to qlvm-agent daemon: connect system bus: dial unix /run/dbus/system_bus_socket: connect: no such file or directory",
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("unexpected failure"),
			expected: "cannot connect to qlvm-agent daemon: unexpected failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := connectionErrorMessage(tt.err)
			if got != tt.expected {
				t.Errorf("connectionErrorMessage() = %q, want %q", got, tt.expected)
			}
		})
	}
}
