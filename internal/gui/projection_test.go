package gui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// TestGuiRunAppWaylandDisplayMissing verifies the WAYLAND_DISPLAY guard
// fires when the environment is empty.
func TestGuiRunAppWaylandDisplayMissing(t *testing.T) {
	orig := os.Getenv("WAYLAND_DISPLAY")
	t.Setenv("WAYLAND_DISPLAY", "")
	defer func() { os.Setenv("WAYLAND_DISPLAY", orig) }()

	err := guiRunWaypipe([]string{"--test"}, nil, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WAYLAND_DISPLAY is empty")
}

// TestGuiRunAppNoVM verifies GuiRunApp returns an error when the VM meta
// is missing.
func TestGuiRunAppNoVM(t *testing.T) {
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	err := GuiRunApp(context.Background(), "nonexistent-vm-xyz", "", projection.ConnectSSH, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run nonexistent-vm-xyz:")
}

// TestGuiRunAppTokenMissing verifies GuiRunApp returns an error when the
// VM meta has no token and the connect mode requires one.
func TestGuiRunAppTokenMissing(t *testing.T) {
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	vmDir := filepath.Join(guiQlvmRoot, "vms", "testvm")
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	// Meta without a token.
	meta := `name: testvm
type: app
ip: 10.100.1.5
memory_mb: 1024
vcpus: 1
`
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "meta.yaml"), []byte(meta), 0o644))

	origWayland := os.Getenv("WAYLAND_DISPLAY")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	defer func() { os.Setenv("WAYLAND_DISPLAY", origWayland) }()

	err := GuiRunApp(context.Background(), "testvm", "firefox", projection.ConnectTCP, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no control token")
}

// TestGuiRunAppSSHSucceeds verifies GuiRunApp calls waypipe with the
// correct SSH args when the meta is valid.
func TestGuiRunAppSSHSucceeds(t *testing.T) {
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	vmDir := filepath.Join(guiQlvmRoot, "vms", "testvm")
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	meta := `name: testvm
type: app
ip: 10.100.1.5
memory_mb: 1024
vcpus: 1
`
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "meta.yaml"), []byte(meta), 0o644))

	origWayland := os.Getenv("WAYLAND_DISPLAY")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	defer func() { os.Setenv("WAYLAND_DISPLAY", origWayland) }()

	// Capture the waypipe args to verify they match the SSH invocation.
	var capturedArgs []string
	origWaypipe := guiRunWaypipe
	guiRunWaypipe = func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		capturedArgs = args
		return nil
	}
	defer func() { guiRunWaypipe = origWaypipe }()

	err := GuiRunApp(context.Background(), "testvm", "firefox --private", projection.ConnectSSH, io.Discard, io.Discard)
	require.NoError(t, err)

	// Verify the args match the expected SSH invocation.
	assert.Contains(t, capturedArgs, "ssh")
	assert.Contains(t, capturedArgs, "user@10.100.1.5")
	assert.Contains(t, capturedArgs, "firefox")
	assert.Contains(t, capturedArgs, "--private")
	assert.Contains(t, capturedArgs, "--xwls")
	assert.Contains(t, capturedArgs, "[testvm] ")
}

// TestGuiRunAppTCPNoDom0IP verifies GuiRunApp returns an error when the
// config has no dom0_ip for TCP mode.
func TestGuiRunAppTCPNoDom0IP(t *testing.T) {
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	vmDir := filepath.Join(guiQlvmRoot, "vms", "testvm")
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	meta := `name: testvm
type: app
ip: 10.100.1.5
token: tok-test-123
memory_mb: 1024
vcpus: 1
`
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "meta.yaml"), []byte(meta), 0o644))

	// Create a temp config without dom0_ip.
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "qlvm.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("network:\n  router_ip: 10.100.0.1\n"), 0o644))
	origCfgPath := guiConfigPath
	guiConfigPath = cfgPath
	defer func() { guiConfigPath = origCfgPath }()

	origWayland := os.Getenv("WAYLAND_DISPLAY")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	defer func() { os.Setenv("WAYLAND_DISPLAY", origWayland) }()

	err := GuiRunApp(context.Background(), "testvm", "firefox", projection.ConnectTCP, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dom0's uplink address")
}

// TestGuiRunAppMetaLoad verifies the meta is loaded correctly from the
// GUI's qlvm root path.
func TestGuiRunAppMetaLoad(t *testing.T) {
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	vmDir := filepath.Join(guiQlvmRoot, "vms", "testvm")
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	meta := `name: testvm
type: app
ip: 10.100.1.5
mac: aa:bb:cc:dd:ee:ff
memory_mb: 2048
vcpus: 2
token: tok
uuid: 1234-5678
`
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "meta.yaml"), []byte(meta), 0o644))

	// Load the meta directly to verify it parses correctly.
	m, err := vm.LoadMeta(vmDir)
	require.NoError(t, err)
	assert.Equal(t, "testvm", m.Name)
	assert.Equal(t, "10.100.1.5", m.IP)
	assert.Equal(t, 2048, m.MemoryMB)
	assert.Equal(t, 2, m.VCPUs)
}

// TestActionsRunApp verifies the Actions.RunApp method delegates to
// GuiRunApp with the selected VM name.
func TestActionsRunApp(t *testing.T) {
	model := NewAppModel(0, 0)
	model.SetVMs(map[string]*agent.VMInfo{}) // Empty VMs map; we just need a model.
	model.SelectVM("testvm")

	// Capture the log messages.
	var logs []string
	logFn := func(msg string) { logs = append(logs, msg) }

	actions := NewActions(model, nil, logFn)

	// Override the root and waypipe to avoid real filesystem/waypipe calls.
	origRoot := guiQlvmRoot
	guiQlvmRoot = t.TempDir()
	defer func() { guiQlvmRoot = origRoot }()

	// Create a valid VM meta.
	vmDir := filepath.Join(guiQlvmRoot, "vms", "testvm")
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vmDir, "meta.yaml"),
		[]byte("name: testvm\ntype: app\nip: 10.100.1.5\nmemory_mb: 1024\nvcpus: 1\n"), 0o644))

	origWayland := os.Getenv("WAYLAND_DISPLAY")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	defer func() { os.Setenv("WAYLAND_DISPLAY", origWayland) }()

	// Capture the waypipe args.
	var capturedArgs []string
	origWaypipe := guiRunWaypipe
	guiRunWaypipe = func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		capturedArgs = args
		return nil
	}
	defer func() { guiRunWaypipe = origWaypipe }()

	err := actions.RunApp(context.Background(), "firefox", projection.ConnectSSH, io.Discard, io.Discard)
	require.NoError(t, err)

	// Verify the log messages.
	assert.Contains(t, logs, "Running app in VM testvm...")

	// Verify waypipe was called with the correct args.
	assert.Contains(t, capturedArgs, "ssh")
	assert.Contains(t, capturedArgs, "user@10.100.1.5")
	assert.Contains(t, capturedArgs, "firefox")
}

// TestActionsRunAppNoSelection verifies Actions.RunApp returns an error
// when no VM is selected.
func TestActionsRunAppNoSelection(t *testing.T) {
	model := NewAppModel(0, 0)
	model.SetVMs(map[string]*agent.VMInfo{})
	actions := NewActions(model, nil, func(string) {})

	err := actions.RunApp(context.Background(), "firefox", projection.ConnectSSH, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no VM selected")
}
