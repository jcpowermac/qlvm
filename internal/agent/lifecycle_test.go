package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingXen is a Xen fake that records calls and supports configurable
// state for lifecycle tests.
type trackingXen struct {
	destroyCalls   []string
	shutdownCalls  []string
	runningCalls   []string
	runningState   map[string]bool
	runningErr     error
	destroyErr     error
	shutdownErr    error
	createDomainErr error
}

func (f *trackingXen) CreateDomain(_ *vm.DomainSpec) error { return f.createDomainErr }
func (f *trackingXen) Destroy(name string) error {
	f.destroyCalls = append(f.destroyCalls, name)
	return f.destroyErr
}
func (f *trackingXen) Shutdown(name string) error {
	f.shutdownCalls = append(f.shutdownCalls, name)
	return f.shutdownErr
}
func (f *trackingXen) List() ([]xenctl.DomainInfo, error) { return nil, nil }
func (f *trackingXen) Running(name string) (bool, error) {
	f.runningCalls = append(f.runningCalls, name)
	if f.runningErr != nil {
		return false, f.runningErr
	}
	if state, ok := f.runningState[name]; ok {
		return state, nil
	}
	return false, nil
}
func (f *trackingXen) Console(_ string) error { return nil }
func (f *trackingXen) Close() error           { return nil }

var _ xenctl.Xen = &trackingXen{}

// --- validateVMName tests ---

func TestValidateVMName(t *testing.T) {
	validNames := []string{
		"my-vm",
		"my_vm",
		"my.vm",
		"myvm",
		"VM123",
		"a",
		"test-vm-01",
	}
	for _, name := range validNames {
		err := validateVMName(name)
		assert.NoError(t, err, "expected valid name %q", name)
	}

	invalidNames := map[string]string{
		"":           "empty",
		"../etc":     "traversal",
		"./etc":      "traversal",
		"foo/bar":    "path separator",
		"foo\\bar":   "backslash",
		".":          "dot",
		"..":         "dotdot",
		"foo bar":    "space",
		"foo@bar":    "special char",
		"foo#bar":    "special char",
		"foo*bar":    "special char",
		"/etc/passwd": "absolute path",
	}
	for name, reason := range invalidNames {
		err := validateVMName(name)
		assert.Error(t, err, "expected invalid name %q (%s)", name, reason)
	}

	// Too long
	longName := make([]byte, 129)
	for i := range longName {
		longName[i] = 'a'
	}
	err := validateVMName(string(longName))
	assert.Error(t, err, "expected name > 128 chars to be invalid")

	// Exactly 128 chars is valid
	exactName := make([]byte, 128)
	for i := range exactName {
		exactName[i] = 'a'
	}
	err = validateVMName(string(exactName))
	assert.NoError(t, err, "expected 128-char name to be valid")
}

// --- KillVM tests ---

func TestKillVM(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.KillVM("test-vm")
	require.NoError(t, err)
	assert.Equal(t, []string{"test-vm"}, x.destroyCalls)
}

func TestKillVMInvalidName(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.KillVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kill ../etc")
	assert.Contains(t, err.Error(), "path separator")
	assert.Empty(t, x.destroyCalls)
}

func TestKillVMSuccess(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.KillVM("test-vm")
	require.NoError(t, err)
	assert.Equal(t, []string{"test-vm"}, x.destroyCalls)
}

func TestKillVMXenError(t *testing.T) {
	x := &trackingXen{destroyErr: errors.New("domain not found")}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.KillVM("test-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kill test-vm")
	assert.Contains(t, err.Error(), "domain not found")
}

// --- StopVM tests ---

func TestStopVMGraceful(t *testing.T) {
	// Domain stops after Shutdown (Running returns false on first poll).
	x := &trackingXen{
		runningState: map[string]bool{"test-vm": false},
	}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.StopVM("test-vm")
	require.NoError(t, err)
	assert.Equal(t, []string{"test-vm"}, x.shutdownCalls)
	assert.Empty(t, x.destroyCalls)
}

func TestStopVMForceKillFallback(t *testing.T) {
	// Shutdown fails, Destroy succeeds.
	x := &trackingXen{shutdownErr: errors.New("acpi failed")}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.StopVM("test-vm")
	require.NoError(t, err)
	assert.Equal(t, []string{"test-vm"}, x.shutdownCalls)
	assert.Equal(t, []string{"test-vm"}, x.destroyCalls)
}

func TestStopVMInvalidName(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.StopVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop ../etc")
	assert.Empty(t, x.shutdownCalls)
	assert.Empty(t, x.destroyCalls)
}

// --- StartVM tests ---

func TestStartVMInvalidName(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.StartVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start ../etc")
}

func TestStartVMMissingMeta(t *testing.T) {
	root := testRoot(t)
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.StartVM("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start nonexistent")
}

// --- DeleteVM tests ---

func TestDeleteVMInvalidName(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.DeleteVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete ../etc")
}

func TestDeleteVMMissingMeta(t *testing.T) {
	root := testRoot(t)
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.DeleteVM("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete nonexistent")
}

// --- CreateVM tests ---

func TestCreateVMInvalidName(t *testing.T) {
	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	_, err := a.CreateVM(&CreateVMOptions{Name: "../etc", Template: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create ../etc")
}

func TestCreateVMEmptyName(t *testing.T) {
	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	_, err := a.CreateVM(&CreateVMOptions{Name: "", Template: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestCreateVMMissingTemplate(t *testing.T) {
	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	_, err := a.CreateVM(&CreateVMOptions{Name: "test-vm", Template: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template is required")
}

func TestCreateVMNonexistentTemplate(t *testing.T) {
	root := testRoot(t)
	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	_, err := a.CreateVM(&CreateVMOptions{Name: "test-vm", Template: "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create test-vm")
}

func TestCreateVMTemplateNotComplete(t *testing.T) {
	root := testRoot(t)
	// Create a template directory with only META (missing template.raw).
	tplDir := filepath.Join(root, "templates", "fedora-test-template")
	require.NoError(t, os.MkdirAll(tplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "META"), []byte("image: quay.io/fedora/fedora:42\ndigest: sha256:abc123\ntype: ostree\n"), 0o644))

	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	_, err := a.CreateVM(&CreateVMOptions{Name: "test-vm", Template: "fedora-test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete")
}

func TestCreateVMAgentTemplateTypeConflict(t *testing.T) {
	root := testRoot(t)
	// Create a complete agent template directory.
	tplDir := filepath.Join(root, "templates", "agent-test-template")
	require.NoError(t, os.MkdirAll(tplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "META"), []byte("image: agent-ref\ndigest: sha256:def456\ntype: agent\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "vmlinuz"), []byte("kernel"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "initramfs"), []byte("initramfs"), 0o755))

	a := New(&trackingXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	_, err := a.CreateVM(&CreateVMOptions{Name: "test-vm", Template: "agent-test", Type: "app"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible with agent template")
}

// --- RestartVM tests ---

func TestRestartVMInvalidName(t *testing.T) {
	x := &trackingXen{}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.RestartVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restart ../etc")
}

func TestRestartVMStopFails(t *testing.T) {
	// Shutdown and Destroy both fail.
	x := &trackingXen{
		shutdownErr: errors.New("shutdown failed"),
		destroyErr:  errors.New("destroy failed"),
	}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.RestartVM("test-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restart test-vm: stop")
}

// --- parseCoresFlag tests ---

func TestParseCoresFlag(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
		wantErr  bool
	}{
		{"", nil, false},
		{"6", []int{6}, false},
		{"6,7", []int{6, 7}, false},
		{"6-8", []int{6, 7, 8}, false},
		{"1,2-4,6", []int{1, 2, 3, 4, 6}, false},
		{" 6 , 7 ", []int{6, 7}, false},
		{"", nil, false},
		{"abc", nil, true},
		{"6-", nil, true},
		{"6-abc", nil, true},
		{"8-6", nil, true},
	}
	for _, tc := range tests {
		cores, err := parseCoresFlag(tc.input)
		if tc.wantErr {
			assert.Error(t, err, "input %q", tc.input)
		} else {
			assert.NoError(t, err, "input %q", tc.input)
			assert.Equal(t, tc.expected, cores, "input %q", tc.input)
		}
	}
}

// --- parseMounts tests ---

func TestParseMounts(t *testing.T) {
	tests := []struct {
		input    []string
		expected []vm.Mount
		wantErr  bool
	}{
		{nil, nil, false},
		{[]string{}, nil, false},
		{[]string{"/host:/guest"}, []vm.Mount{{Host: "/host", Guest: "/guest"}}, false},
		{[]string{"/h1:/g1", "/h2:/g2"}, []vm.Mount{{Host: "/h1", Guest: "/g1"}, {Host: "/h2", Guest: "/g2"}}, false},
		{[]string{"/host"}, nil, true},
		{[]string{":/guest"}, nil, true},
		{[]string{"/host:"}, nil, true},
	}
	for _, tc := range tests {
		ms, err := parseMounts(tc.input)
		if tc.wantErr {
			assert.Error(t, err, "input %v", tc.input)
		} else {
			assert.NoError(t, err, "input %v", tc.input)
			assert.Equal(t, tc.expected, ms, "input %v", tc.input)
		}
	}
}

// Verify template import is used (avoid unused import if template tests change).
var _ = template.IsAgent
