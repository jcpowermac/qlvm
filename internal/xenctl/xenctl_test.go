package xenctl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// fakeXen records lifecycle calls in f.events.
type fakeXen struct {
	running map[string]bool
	events  *[]string
}

func (f *fakeXen) CreateDomain(spec *vm.DomainSpec) error {
	*f.events = append(*f.events, "create:"+spec.Name)
	f.running[spec.Name] = true
	return nil
}
func (f *fakeXen) Destroy(name string) error {
	*f.events = append(*f.events, "destroy:"+name)
	delete(f.running, name)
	return nil
}
func (f *fakeXen) Shutdown(name string) error {
	*f.events = append(*f.events, "shutdown:"+name)
	return nil
}
func (f *fakeXen) List() ([]DomainInfo, error) { return nil, nil }
func (f *fakeXen) Running(name string) (bool, error) {
	return f.running[name], nil
}
func (f *fakeXen) Close() error { return nil }

// fakeVif is a VifPorter that reports f.stale from StaleVifPorts.
type fakeVif struct {
	stale  []string
	events *[]string
}

func (f *fakeVif) AddVifPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeVif) DelVifPort(_ context.Context, dev string) error {
	*f.events = append(*f.events, "vifdel:"+dev)
	return nil
}
func (f *fakeVif) StaleVifPorts(_ context.Context, _ string) ([]string, error) {
	return f.stale, nil
}

// fakeOVN records DelLSPort calls.
type fakeOVN struct{ events *[]string }

func (f *fakeOVN) AddLSPort(_ context.Context, _, _, _, _ string) error { return nil }
func (f *fakeOVN) DelLSPort(_ context.Context, name string) error {
	*f.events = append(*f.events, "ovndel:"+name)
	return nil
}

func testMeta(t *testing.T, dir, typ string) *vm.Meta {
	t.Helper()
	m := &vm.Meta{
		Name:     "vm1",
		Type:     typ,
		Domain:   "work",
		UUID:     "11111111-2222-3333-4444-555555555555",
		MAC:      "aa:bb:cc:dd:ee:01",
		MemoryMB: 512,
		VCPUs:    2,
	}
	require.NoError(t, m.Save(dir))
	return m
}

var testTpl = &template.Template{Dir: "/tmp/tpl", RootDev: "/dev/xvda3"}

func TestStartRefusesRunning(t *testing.T) {
	var events []string
	x := &fakeXen{running: map[string]bool{"vm1": true}, events: &events}
	vif := &fakeVif{events: &events}
	m := testMeta(t, t.TempDir(), "app")

	err := Start(context.Background(), x, vif, m, t.TempDir(), testTpl)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
	require.Empty(t, events, "no OVS or Xen action when the domain is already running")
}

func TestStartCleansStalePort(t *testing.T) {
	var events []string
	x := &fakeXen{running: map[string]bool{}, events: &events}
	vif := &fakeVif{stale: []string{"vif3.0"}, events: &events}
	m := testMeta(t, t.TempDir(), "app")

	require.NoError(t, Start(context.Background(), x, vif, m, t.TempDir(), testTpl))
	require.Equal(t, []string{"vifdel:vif3.0", "create:vm1"}, events,
		"stale port removed before CreateDomain")
}

func TestStartNoStalePorts(t *testing.T) {
	var events []string
	x := &fakeXen{running: map[string]bool{}, events: &events}
	vif := &fakeVif{events: &events}
	m := testMeta(t, t.TempDir(), "app")

	require.NoError(t, Start(context.Background(), x, vif, m, t.TempDir(), testTpl))
	require.Equal(t, []string{"create:vm1"}, events)
}

func TestDeleteOrdering(t *testing.T) {
	var events []string
	vmDir := t.TempDir()
	m := testMeta(t, vmDir, "app")
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "config"),
		[]byte("Host other\n  HostName 1.2.3.4\n\nHost vm1\n  HostName 10.100.1.11\n  User user\n"), 0o600))

	x := &fakeXen{running: map[string]bool{"vm1": true}, events: &events}
	vif := &fakeVif{stale: []string{"vif7.0"}, events: &events}
	ovn := &fakeOVN{events: &events}

	require.NoError(t, Delete(context.Background(), x, ovn, vif, home, vmDir, m.Name))
	require.Equal(t, []string{"destroy:vm1", "ovndel:vm1", "vifdel:vif7.0"}, events,
		"Destroy -> DelLSPort -> stale port cleanup, in that order")
	_, err := os.Stat(vmDir)
	require.True(t, os.IsNotExist(err), "vmDir removed")

	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config")) // #nosec G304 -- test fixture
	require.NoError(t, err)
	require.Equal(t, "Host other\n  HostName 1.2.3.4\n", string(data),
		"app type: the VM's ssh block is removed, others untouched")
}

func TestDeleteSkipsSSHForDisposable(t *testing.T) {
	var events []string
	vmDir := t.TempDir()
	m := testMeta(t, vmDir, "disposable")
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	pre := []byte("Host vm1\n  HostName 10.100.1.11\n")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "config"), pre, 0o600))

	x := &fakeXen{running: map[string]bool{}, events: &events}
	vif := &fakeVif{events: &events}
	ovn := &fakeOVN{events: &events}

	require.NoError(t, Delete(context.Background(), x, ovn, vif, home, vmDir, m.Name))
	require.Equal(t, []string{"ovndel:vm1"}, events, "stopped VM: no destroy, no stale cleanup needed")
	_, err := os.Stat(vmDir)
	require.True(t, os.IsNotExist(err), "vmDir removed")
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config")) // #nosec G304 -- test fixture
	require.NoError(t, err)
	require.Equal(t, pre, data, "disposable type: ssh config untouched")
}

func TestDeleteStopsRunningVM(t *testing.T) {
	var events []string
	vmDir := t.TempDir()
	testMeta(t, vmDir, "disposable")

	x := &fakeXen{running: map[string]bool{"vm1": true}, events: &events}
	vif := &fakeVif{events: &events}
	ovn := &fakeOVN{events: &events}

	require.NoError(t, Delete(context.Background(), x, ovn, vif, t.TempDir(), vmDir, "vm1"))
	require.Equal(t, []string{"destroy:vm1", "ovndel:vm1"}, events)
}
