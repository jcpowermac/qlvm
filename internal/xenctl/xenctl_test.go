package xenctl

import (
	"context"
	"errors"
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

func TestDomainState(t *testing.T) {
	// Flag combos mirror libxl dominfo (domctl rc flags); "blocked, not
	// running" is the live -b---- case that was mislabelled stopped.
	tests := []struct {
		dying, running, paused, blocked bool
		want                            string
	}{
		{dying: true, want: "dying"},
		{running: true, want: "running"},
		{running: true, blocked: true, want: "running", /* a live guest is running even if a vCPU is blocked on I/O */},
		{paused: true, want: "paused"},
		{blocked: true, want: "blocked"},
		{want: "stopped"},
	}
	for _, tc := range tests {
		got := domainState(tc.dying, tc.running, tc.paused, tc.blocked)
		require.Equal(t, tc.want, got, "state for dying=%v running=%v paused=%v blocked=%v", tc.dying, tc.running, tc.paused, tc.blocked)
	}
}

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
		[]byte("Host other\n  HostName 10.100.1.99\n\nHost vm1\n  HostName 10.100.1.11\n  User user\n"), 0o600))

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
	require.Equal(t, "Host other\n  HostName 10.100.1.99\n", string(data),
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

// errOVN always fails DelLSPort with a fixed error.
type errOVN struct{ err error }

func (e *errOVN) AddLSPort(_ context.Context, _, _, _, _ string) error { return nil }
func (e *errOVN) DelLSPort(_ context.Context, _ string) error          { return e.err }

func TestDeleteToleratesMissingLSPort(t *testing.T) {
	vmDir := t.TempDir()
	testMeta(t, vmDir, "disposable")
	x := &fakeXen{running: map[string]bool{}, events: &[]string{}}
	vif := &fakeVif{events: &[]string{}}
	ovn := &errOVN{err: errors.New(`switch port "vm1" not found`)}

	require.NoError(t, Delete(context.Background(), x, ovn, vif, t.TempDir(), vmDir, "vm1"),
		"a retry after an interrupted delete must not strand the vmDir")
	_, statErr := os.Stat(vmDir)
	require.True(t, os.IsNotExist(statErr), "vmDir removed despite missing port")
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
