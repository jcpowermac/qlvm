package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/provisioner"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXen is a no-op Xen for construction tests.
type fakeXen struct{}

func (f *fakeXen) CreateDomain(_ *vm.DomainSpec) error { return nil }
func (f *fakeXen) Destroy(_ string) error              { return nil }
func (f *fakeXen) Shutdown(_ string) error             { return nil }
func (f *fakeXen) List() ([]xenctl.DomainInfo, error)  { return nil, nil }
func (f *fakeXen) Running(_ string) (bool, error)      { return false, nil }
func (f *fakeXen) Console(_ string) error              { return nil }
func (f *fakeXen) Close() error                        { return nil }

// fakeOVNPorter is a no-op OVNPorter for construction tests.
type fakeOVNPorter struct{}

func (f *fakeOVNPorter) AddLSPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeOVNPorter) DelLSPort(_ context.Context, _ string) error          { return nil }

// fakeVifPorter is a no-op VifPorter for construction tests.
type fakeVifPorter struct{}

func (f *fakeVifPorter) AddVifPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeVifPorter) DelVifPort(_ context.Context, _ string) error          { return nil }
func (f *fakeVifPorter) StaleVifPorts(_ context.Context, _ string) ([]string, error) { return nil, nil }

func TestNew(t *testing.T) {
	x := &fakeXen{}
	o := &fakeOVNPorter{}
	v := &fakeVifPorter{}
	cfg := &config.Config{
		Domains: []config.Domain{{Name: "alpha", Subnet: "10.100.1"}},
		Network: config.Network{NIC: "eth0", Gateway: "192.0.2.1", RouterIP: "192.0.2.1", DNS: []string{"1.1.1.1"}},
		VM:      config.VMDefaults{MemoryMB: 4096, VCPUs: 2},
	}

	a := New(x, o, v, cfg, "/var/lib/qvm/coremap.yaml", "/var/lib/qvm")

	require.NotNil(t, a)
	assert.Equal(t, x, a.xen)
	assert.Equal(t, o, a.ovn)
	assert.Equal(t, v, a.ovs)
	assert.Equal(t, cfg, a.cfg)
	assert.Equal(t, "/var/lib/qvm/coremap.yaml", a.coremapPath)
	assert.Equal(t, "/var/lib/qvm", a.root)
	assert.NotNil(t, a.progress)
	assert.Empty(t, a.progress)
	assert.Nil(t, a.conn)
}

func TestDBusConstants(t *testing.T) {
	assert.Equal(t, "io.qlvm.Agent", ServiceName)
	assert.Equal(t, "/io/qlvm/Agent", ObjectPath)
	assert.Equal(t, "io.qlvm.Agent", Iface)
}

func TestProgressEmitWithoutBus(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	// emitProgress should not panic without a bus connection.
	a.emitProgress("op1", "starting")
	a.mu.Lock()
	tracker, ok := a.progress["op1"]
	a.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "op1", tracker.ID)
	assert.Equal(t, "starting", tracker.Message)
	assert.False(t, tracker.Done)

	// Update the message.
	a.emitProgress("op1", "working")
	a.mu.Lock()
	tracker = a.progress["op1"]
	a.mu.Unlock()
	assert.Equal(t, "working", tracker.Message)

	// Mark as done — entry is removed from the map to free memory.
	a.emitProgressDone("op1")
	a.mu.Lock()
	_, exists := a.progress["op1"]
	a.mu.Unlock()
	assert.False(t, exists, "progress entry should be removed after done")

	// emitProgressDone for unknown id should not panic.
	a.emitProgressDone("unknown")
}

// TestRunRegistersBus verifies that Run attempts to register on the bus.
// The full registration test requires a live D-Bus system bus; here we
// verify the error path when the bus is unavailable and that the constants
// are used correctly. Integration tests (Task 9) cover the live bus case.
func TestRunRegistersBus(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")
	// Timeout prevents an indefinite hang if the system bus is available
	// and the name is acquired (Run would otherwise block until the bus
	// closes).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run attempts to register on the system bus. Three outcomes are
	// possible:
	// 1. No bus available: error mentions "system bus".
	// 2. Bus available, name acquired: returns the service name after timeout.
	// 3. Bus available, name refused by policy: error mentions the name.
	name, err := a.Run(ctx)
	switch {
	case err == nil:
		assert.Equal(t, ServiceName, name)
		assert.Nil(t, a.conn)
	case errors.Is(err, context.DeadlineExceeded):
		// ctx timed out after acquiring the name — the bus was available
		// and the test environment doesn't have a live bus to close on its own.
		// This is acceptable: it means registration succeeded.
		assert.Nil(t, a.conn)
	case errors.Is(err, context.Canceled):
		// ctx was cancelled (should not happen in this test).
		t.Fatal("unexpected context cancellation")
	default:
		// Verify the error is from the expected path.
		assert.True(t,
			strings.Contains(err.Error(), "system bus") ||
				strings.Contains(err.Error(), ServiceName),
			"unexpected error: %v", err)
		assert.Equal(t, "", name)
		assert.Nil(t, a.conn)
	}
}

// Verify fake types satisfy the interfaces they're used for.
var _ xenctl.Xen = &fakeXen{}

var _ vm.OVNPorter = &fakeOVNPorter{}

var _ ovs.VifPorter = &fakeVifPorter{}

// fakePodman is a recording fake for the template.Podman interface.
type fakePodman struct {
	pullErr               error
	pullDigest            string
	listBuilderContainers []string
	listBuilderErr        error
	removeContainerErr    error
	runImageBuilderErr    error
	runImageBuilderCreatesRaw bool
	inspectImageLabels    map[string]string
	inspectImageErr       error
	createContainerID     string
	createContainerErr    error
	copyFromErr           error

	// recorded calls
	pullCalls         int
	listCalls         int
	removeCalls       []string
	runCalls          int
	runImageBuilderWorkdir string
	inspectCalls      int
	createCalls       int
	copyCalls         int
}

func (f *fakePodman) Pull(_ context.Context, _ string) (string, error) {
	f.pullCalls++
	return f.pullDigest, f.pullErr
}

func (f *fakePodman) RunImageBuilder(_ context.Context, workdir, _ string, _ io.Writer) error {
	f.runCalls++
	f.runImageBuilderWorkdir = workdir
	if f.runImageBuilderCreatesRaw {
		rawDir := filepath.Join(workdir, "image")
		if err := os.MkdirAll(rawDir, 0o755); err != nil { //nolint:gosec,G301
			return err
		}
		if err := os.WriteFile(filepath.Join(rawDir, "fedora.raw"), []byte("fake"), 0o644); err != nil { //nolint:gosec,G306
			return err
		}
	}
	return f.runImageBuilderErr
}

func (f *fakePodman) ListBuilderContainers(_ context.Context) ([]string, error) {
	f.listCalls++
	return f.listBuilderContainers, f.listBuilderErr
}

func (f *fakePodman) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.removeCalls = append(f.removeCalls, id)
	return f.removeContainerErr
}

func (f *fakePodman) InspectImage(_ context.Context, _ string) (map[string]string, error) {
	f.inspectCalls++
	return f.inspectImageLabels, f.inspectImageErr
}

func (f *fakePodman) CreateContainer(_ context.Context, _ string, _ []string) (string, error) {
	f.createCalls++
	return f.createContainerID, f.createContainerErr
}

func (f *fakePodman) CopyFromContainer(_ context.Context, _, _ string, _ io.Writer) error {
	f.copyCalls++
	return f.copyFromErr
}

var _ template.Podman = &fakePodman{}

// setupTemplateTest creates a temp root with fake SSH keys and returns the
// root path. The caller must defer cleanup.
func setupTemplateTest(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()

	// Create templates dir.
	if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}

	// Create a fake SSH key in a temp home directory.
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK test@test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")

	return root
}

// writeVMMeta creates a fake VM meta.yaml that references a template.
func writeVMMeta(t *testing.T, root, vmName, image, digest string) {
	t.Helper()
	vmDir := filepath.Join(root, "vms", vmName)
	if err := os.MkdirAll(vmDir, 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}
	// Write a minimal meta.yaml that vm.LoadMeta can parse.
	meta := fmt.Sprintf("name: %s\nimage: %s\ndigest: %s\ntype: app\ndomain: default\nmemory_mb: 4096\nvcpus: 2\n", vmName, image, digest)
	if err := os.WriteFile(filepath.Join(vmDir, "meta.yaml"), []byte(meta), 0o644); err != nil { //nolint:gosec,G306
		t.Fatal(err)
	}
}

func TestCreateTemplateEmptyImage(t *testing.T) {
	root := setupTemplateTest(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	_, err := a.CreateTemplate("", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image is required")
}

func TestCreateTemplatePodmanFactoryError(t *testing.T) {
	root := setupTemplateTest(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.podmanFactory = func(_ context.Context, _ string) (template.Podman, error) {
		return nil, errors.New("podman connection failed")
	}

	_, err := a.CreateTemplate("quay.io/fedora/fedora:42", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podman")
	assert.Contains(t, err.Error(), "podman connection failed")
}

func TestCreateTemplatePullError(t *testing.T) {
	root := setupTemplateTest(t)
	pod := &fakePodman{pullErr: errors.New("pull failed")}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.podmanFactory = func(_ context.Context, _ string) (template.Podman, error) {
		return pod, nil
	}

	_, err := a.CreateTemplate("quay.io/fedora/fedora:42", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull")
	assert.Contains(t, err.Error(), "pull failed")
	assert.Equal(t, 1, pod.pullCalls)
}

func TestCreateTemplateExistsNotForce(t *testing.T) {
	root := setupTemplateTest(t)

	image := "quay.io/fedora/fedora:42"
	digest := "sha256:abc"

	// Create an existing template dir using the canonical DirOfRef path.
	existingDir := template.DirOfRef(root, image, template.NormalizeDigest(digest))
	if err := os.MkdirAll(existingDir, 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}

	pod := &fakePodman{pullDigest: digest}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.podmanFactory = func(_ context.Context, _ string) (template.Podman, error) {
		return pod, nil
	}

	_, err := a.CreateTemplate(image, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateTemplateExistsForceVMReferences(t *testing.T) {
	root := setupTemplateTest(t)

	image := "quay.io/fedora/fedora:42"
	digest := "sha256:abc"

	// Create an existing template dir using the canonical DirOfRef path.
	tplDir := template.DirOfRef(root, image, template.NormalizeDigest(digest))
	if err := os.MkdirAll(tplDir, 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}
	writeVMMeta(t, root, "testvm", image, digest)

	pod := &fakePodman{pullDigest: digest}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.podmanFactory = func(_ context.Context, _ string) (template.Podman, error) {
		return pod, nil
	}

	_, err := a.CreateTemplate(image, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "referenced by VMs")
	assert.Contains(t, err.Error(), "testvm")
}

func TestCreateTemplateSuccess(t *testing.T) {
	root := setupTemplateTest(t)

	image := "quay.io/fedora/fedora:42"
	digest := "sha256:abc"

	// Use a fake podman whose RunImageBuilder creates a raw file so
	// template.Ensure's adoptRaw step succeeds and proceeds to the bake.
	pod := &fakePodman{
		pullDigest:                  digest,
		runImageBuilderCreatesRaw: true,
	}

	var bakeCalled bool
	var bakeDir string
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.podmanFactory = func(_ context.Context, _ string) (template.Podman, error) {
		return pod, nil
	}
	a.bakeFn = func(_ context.Context, dir, _, _ string) error {
		bakeCalled = true
		bakeDir = dir
		return nil
	}

	dirName, err := a.CreateTemplate(image, false)
	require.NoError(t, err)

	// Verify the template dir was created and returned.
	require.NotEmpty(t, dirName)
	expectedDir := filepath.Join(root, "templates", dirName)
	_, err = os.Stat(expectedDir)
	require.NoError(t, err, "template dir should exist: %s", expectedDir)

	// Verify the bake callback was invoked with the correct dir.
	assert.True(t, bakeCalled, "bake callback should have been invoked")
	assert.Equal(t, expectedDir, bakeDir, "bake callback should receive the template dir")

	// Verify podman pull and run-image-builder were called.
	assert.Equal(t, 1, pod.pullCalls)
	assert.NotEmpty(t, pod.runImageBuilderWorkdir, "RunImageBuilder should have been called with the template dir")

	// Verify a META file was created by template.Ensure.
	_, err = os.Stat(filepath.Join(expectedDir, "META"))
	require.NoError(t, err, "META file should exist after successful bake")
}

func TestDeleteTemplateEmptyDir(t *testing.T) {
	root := setupTemplateTest(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.DeleteTemplate("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid template directory")
}

func TestDeleteTemplateInvalidDirNames(t *testing.T) {
	root := setupTemplateTest(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	for _, name := range []string{"../etc", "foo/bar", "foo\\bar", ".."} {
		err := a.DeleteTemplate(name)
		require.Error(t, err, "name %q", name)
		assert.Contains(t, err.Error(), "invalid template directory", "name %q", name)
	}
}

func TestDeleteTemplateNotFound(t *testing.T) {
	root := setupTemplateTest(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.DeleteTemplate("nonexistent-template")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDeleteTemplateInUse(t *testing.T) {
	root := setupTemplateTest(t)

	image := "quay.io/fedora/fedora:42"
	digest := "sha256:abc"

	// Create a template dir using the canonical DirOfRef path.
	fullDir := template.DirOfRef(root, image, template.NormalizeDigest(digest))
	if err := os.MkdirAll(fullDir, 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}
	writeVMMeta(t, root, "testvm", image, digest)

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	// DeleteTemplate takes the dir basename.
	tplDir := filepath.Base(fullDir)
	err := a.DeleteTemplate(tplDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "referenced by VMs")
	assert.Contains(t, err.Error(), "testvm")
}

func TestDeleteTemplateSuccess(t *testing.T) {
	root := setupTemplateTest(t)

	// Create a template dir.
	tplDir := "fedora-42-sha256-abc"
	if err := os.MkdirAll(filepath.Join(root, "templates", tplDir), 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}
	// Put a marker file to verify removal.
	marker := filepath.Join(root, "templates", tplDir, "marker")
	if err := os.WriteFile(marker, []byte("test"), 0o644); err != nil { //nolint:gosec,G306
		t.Fatal(err)
	}

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.DeleteTemplate(tplDir)
	require.NoError(t, err)

	// Verify the dir was removed.
	_, err = os.Stat(filepath.Join(root, "templates", tplDir))
	assert.True(t, os.IsNotExist(err), "template dir should be removed")
}

// --- Auxiliary method tests ---

// fakeProvisionRunner records Provision calls and optionally returns an error.
type fakeProvisionRunner struct {
	hosts []string
	ops   []provisioner.Op
	err   error
}

func (f *fakeProvisionRunner) Run(_ context.Context, host string, ops []provisioner.Op) error {
	f.hosts = append(f.hosts, host)
	f.ops = append(f.ops, ops...)
	return f.err
}

// fakeKernelHost fakes the kernelSyncer interface for SyncKernel tests.
type fakeKernelHost struct {
	runOutput string
	runErr    error
	files     map[string][]byte
	fetchErr  error
}

func (f *fakeKernelHost) Run(_ context.Context, _ string) (string, error) {
	return f.runOutput, f.runErr
}

func (f *fakeKernelHost) FetchFile(_ context.Context, path string) (io.ReadCloser, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	if data, ok := f.files[path]; ok {
		return io.NopCloser(strings.NewReader(string(data))), nil
	}
	return nil, fmt.Errorf("file not found: %s", path)
}

// setupVMTest creates a temp qlvm root with a VM meta file and a temp
// home dir with an SSH config entry for the VM.
func setupVMTest(t *testing.T, name string) (root, home string) {
	t.Helper()
	root = t.TempDir()
	home = t.TempDir()
	vmDir := filepath.Join(root, "vms", name)
	require.NoError(t, os.MkdirAll(vmDir, 0o755)) //nolint:gosec,G301
	m := &vm.Meta{
		Name:     name,
		Type:     "app",
		Image:    "fedora",
		IP:       "192.168.122.10",
		MAC:      "aa:bb:cc:dd:ee:ff",
		MemoryMB: 2048,
		VCPUs:    2,
	}
	require.NoError(t, m.Save(vmDir))
	// Create SSH config with an entry for the VM.
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"), []byte(
		"Host "+name+"\n  HostName 192.168.122.10\n  User user\n",
	), 0o600))
	return root, home
}

func TestProvisionVMInvalidName(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.ProvisionVM("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision")
}

func TestProvisionVMMissingVM(t *testing.T) {
	root := t.TempDir()
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.ProvisionVM("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision nonexistent")
}

func TestProvisionVMSuccess(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	provDir := setupProvisionTest(t, "testvm")
	runner := &fakeProvisionRunner{}
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.provisionRunner = runner
	a.provisionDir = provDir
	a.homeDir = home
	a.progressHook = capture.hook

	err := a.ProvisionVM("testvm")
	require.NoError(t, err)

	// Verify the runner was called.
	assert.NotEmpty(t, runner.hosts, "runner should have been called")
	assert.NotEmpty(t, runner.ops, "runner should have received ops")

	// Verify progress was tracked — use progressHook since the map is cleaned up.
	require.NotEmpty(t, capture.events, "should have captured progress events")
	last := capture.events[len(capture.events)-1]
	assert.True(t, last.done, "last event should be ProgressDone")
}

func TestProvisionVMRunnerError(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	provDir := setupProvisionTest(t, "testvm")
	runner := &fakeProvisionRunner{err: errors.New("sftp failed")}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.provisionRunner = runner
	a.provisionDir = provDir
	a.homeDir = home

	err := a.ProvisionVM("testvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision testvm")
	assert.Contains(t, err.Error(), "sftp failed")
}

// setupProvisionTest creates a temp provision dir with base/dotfiles content.
func setupProvisionTest(t *testing.T, _ string) string {
	t.Helper()
	provDir := t.TempDir()
	dotDir := filepath.Join(provDir, "base", "dotfiles")
	require.NoError(t, os.MkdirAll(dotDir, 0o755)) //nolint:gosec,G301
	require.NoError(t, os.WriteFile(filepath.Join(dotDir, ".bashrc"), []byte("# test"), 0o644)) //nolint:gosec,G306
	return provDir
}

func TestSyncKernelInvalidName(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	err := a.SyncKernel("../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel")
}

func TestSyncKernelMissingVM(t *testing.T) {
	root := t.TempDir()
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)

	err := a.SyncKernel("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel nonexistent")
}

func TestSyncKernelSSHConnectError(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.homeDir = home
	a.sshConnectFunc = func(_ context.Context, _, _, _ string) (*ssh.Client, error) {
		return nil, errors.New("connection refused")
	}

	err := a.SyncKernel("testvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel testvm")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestSyncKernelSuccess(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runOutput: "kernel-core-6.11.9-300.fc44.x86_64",
		files: map[string][]byte{
			"/boot/vmlinuz-6.11.9-300.fc44":          []byte("vmlinuz data"),
			"/boot/initramfs-6.11.9-300.fc44.img": []byte("initramfs data"),
		},
	}
	a.progressHook = capture.hook

	err := a.SyncKernel("testvm")
	require.NoError(t, err)

	vmDir := filepath.Join(root, "vms", "testvm")
	// Verify versioned files were written.
	for _, name := range []string{"vmlinuz-6.11.9-300.fc44", "initramfs-6.11.9-300.fc44.img"} {
		data, err := os.ReadFile(filepath.Join(vmDir, name)) //nolint:gosec,G304
		require.NoError(t, err, "file %s should exist", name)
		assert.NotEmpty(t, data)
	}
	// Verify unversioned aliases were written.
	for _, name := range []string{"vmlinuz", "initramfs"} {
		_, err := os.Stat(filepath.Join(vmDir, name))
		require.NoError(t, err, "alias %s should exist", name)
	}

	// Verify progress was tracked — use progressHook since the map is cleaned up.
	require.NotEmpty(t, capture.events, "should have captured progress events")
	last := capture.events[len(capture.events)-1]
	assert.True(t, last.done, "last event should be ProgressDone")
}

func TestSyncKernelKernelQueryError(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runErr: errors.New("ssh session failed"),
	}

	err := a.SyncKernel("testvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel testvm")
	assert.Contains(t, err.Error(), "query vm kernel")
}

func TestSyncKernelBadKernelVersion(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runOutput: "no kernel found",
	}

	err := a.SyncKernel("testvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel testvm")
}

func TestSyncKernelFetchError(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runOutput: "kernel-core-6.11.9-300.fc44.x86_64",
		fetchErr:  errors.New("file not found on remote"),
	}

	err := a.SyncKernel("testvm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel testvm")
	assert.Contains(t, err.Error(), "fetch")
}

func TestSyncKernelFilesNoPartialWrite(t *testing.T) {
	// If fetch of the second file fails, no files should be written.
	dir := t.TempDir()
	ctx := context.Background()

	host := &fakeKernelHost{
		runOutput: "kernel-core-6.11.9-300.fc44.x86_64",
		files: map[string][]byte{
			"/boot/vmlinuz-6.11.9-300.fc44": []byte("vmlinuz data"),
			// initramfs missing — will fail fetch
		},
	}

	_, err := syncKernelFiles(ctx, host, dir)
	require.Error(t, err)

	// No files should have been written.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no files should be written on partial fetch failure")
}

// --- Progress signal ordering tests ---

// progressCapture records progress emissions in order.
type progressCapture struct {
	events []progressEvent
}

type progressEvent struct {
	id      string
	message string
	done    bool
}

func (p *progressCapture) hook(id, message string, done bool) {
	p.events = append(p.events, progressEvent{id: id, message: message, done: done})
}

// assertProgressOrder verifies that the captured events match the expected
// sequence of (message, done) pairs. The id is verified to be consistent
// across all events.
func assertProgressOrder(t *testing.T, p *progressCapture, expected []string) {
	t.Helper()
	require.NotEmpty(t, p.events, "no progress events captured")

	// All events should have the same ID.
	id := p.events[0].id
	require.NotEmpty(t, id, "operation ID should not be empty")
	for i, e := range p.events {
		assert.Equal(t, id, e.id, "event %d should have the same ID", i)
	}

	// Verify the sequence of messages (non-done events) followed by a done event.
	var messages []string
	for _, e := range p.events {
		if e.done {
			continue
		}
		messages = append(messages, e.message)
	}
	assert.Equal(t, expected, messages, "progress messages should match expected order")

	// The last event should be a ProgressDone.
	last := p.events[len(p.events)-1]
	assert.True(t, last.done, "last event should be ProgressDone")
	assert.Equal(t, id, last.id, "ProgressDone should have the same ID")
}

func TestProvisionVMProgressOrder(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	provDir := setupProvisionTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.provisionRunner = &fakeProvisionRunner{}
	a.provisionDir = provDir
	a.homeDir = home
	a.progressHook = capture.hook

	err := a.ProvisionVM("testvm")
	require.NoError(t, err)

	assertProgressOrder(t, capture, []string{
		"starting",
		"Syncing dotfiles into testvm",
	})
}

func TestSyncKernelProgressOrder(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runOutput: "kernel-core-6.11.9-300.fc44.x86_64",
		files: map[string][]byte{
			"/boot/vmlinuz-6.11.9-300.fc44":     []byte("vmlinuz data"),
			"/boot/initramfs-6.11.9-300.fc44.img": []byte("initramfs data"),
		},
	}
	a.progressHook = capture.hook

	err := a.SyncKernel("testvm")
	require.NoError(t, err)

	// The last message includes the vmDir path, so we verify the first three
	// messages and check that the fourth starts with "Synced kernel".
	require.GreaterOrEqual(t, len(capture.events)-1, 4, "should have at least 4 progress events (excluding done)")
	id := capture.events[0].id
	require.NotEmpty(t, id, "operation ID should not be empty")
	assert.Equal(t, "starting", capture.events[0].message)
	assert.Equal(t, "Querying kernel version on testvm", capture.events[1].message)
	assert.Equal(t, "Fetching kernel files from testvm", capture.events[2].message)
	assert.Contains(t, capture.events[3].message, "Synced kernel 6.11.9-300.fc44")
	last := capture.events[len(capture.events)-1]
	assert.True(t, last.done, "last event should be ProgressDone")
	assert.Equal(t, id, last.id, "ProgressDone should have the same ID")
}

func TestProvisionVMProgressOrderOnFailure(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	provDir := setupProvisionTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.provisionRunner = &fakeProvisionRunner{err: errors.New("sftp failed")}
	a.provisionDir = provDir
	a.homeDir = home
	a.progressHook = capture.hook

	err := a.ProvisionVM("testvm")
	require.Error(t, err)

	// On failure, progress should still be emitted in order and ProgressDone
	// should be the last event.
	assertProgressOrder(t, capture, []string{
		"starting",
		"Syncing dotfiles into testvm",
	})
}

func TestSyncKernelProgressOrderOnFailure(t *testing.T) {
	root, _ := setupVMTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.kernelHost = &fakeKernelHost{
		runErr: errors.New("ssh session failed"),
	}
	a.progressHook = capture.hook

	err := a.SyncKernel("testvm")
	require.Error(t, err)

	// On failure, progress should still be emitted in order and ProgressDone
	// should be the last event.
	require.NotEmpty(t, capture.events, "should have captured progress events")
	id := capture.events[0].id
	require.NotEmpty(t, id, "operation ID should not be empty")
	assert.Equal(t, "starting", capture.events[0].message)
	last := capture.events[len(capture.events)-1]
	assert.True(t, last.done, "last event should be ProgressDone")
	assert.Equal(t, id, last.id, "ProgressDone should have the same ID")
}

func TestUniqueProgressIDs(t *testing.T) {
	root, home := setupVMTest(t, "testvm")
	provDir := setupProvisionTest(t, "testvm")
	capture := &progressCapture{}
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", root)
	a.provisionRunner = &fakeProvisionRunner{}
	a.provisionDir = provDir
	a.homeDir = home
	a.progressHook = capture.hook

	// Run two provision operations; each should get a unique progress ID.
	require.NoError(t, a.ProvisionVM("testvm"))
	firstID := capture.events[0].id
	require.NotEmpty(t, firstID, "first operation ID should not be empty")

	capture.events = nil
	require.NoError(t, a.ProvisionVM("testvm"))
	secondID := capture.events[0].id
	require.NotEmpty(t, secondID, "second operation ID should not be empty")
	assert.NotEqual(t, firstID, secondID, "each operation should have a unique ID")
}
