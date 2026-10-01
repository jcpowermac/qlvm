package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// fakeXen records stopForced calls in f.events and injects errors.
// Running responses come from runningSeq (one value per call, FIFO) when
// set, else fall back to the running map.
type fakeXen struct {
	events      *[]string
	running     map[string]bool
	runningSeq  []bool
	runningErr  error
	shutdownErr error
	destroyErr  error
}

func (f *fakeXen) CreateDomain(*vm.DomainSpec) error { return nil }
func (f *fakeXen) Destroy(name string) error {
	*f.events = append(*f.events, "destroy:"+name)
	delete(f.running, name)
	return f.destroyErr
}
func (f *fakeXen) Shutdown(name string) error {
	*f.events = append(*f.events, "shutdown:"+name)
	return f.shutdownErr
}
func (f *fakeXen) List() ([]xenctl.DomainInfo, error) { return nil, nil }
func (f *fakeXen) Running(name string) (bool, error) {
	if f.runningErr != nil {
		return false, f.runningErr
	}
	if len(f.runningSeq) > 0 {
		v := f.runningSeq[0]
		f.runningSeq = f.runningSeq[1:]
		return v, nil
	}
	return f.running[name], nil
}
func (f *fakeXen) Close() error { return nil }

// shortStopSeams shrinks stopPollInterval/stopMaxWait so stopForced's poll
// loop finishes in milliseconds, restoring both on test cleanup.
func shortStopSeams(t *testing.T, interval, max time.Duration) {
	t.Helper()
	oldI, oldW := stopPollInterval, stopMaxWait
	stopPollInterval, stopMaxWait = interval, max
	t.Cleanup(func() { stopPollInterval, stopMaxWait = oldI, oldW })
}

func TestListRows(t *testing.T) {
	infos := []xenctl.DomainInfo{
		{Name: "vm1", ID: 1, MemMB: 512, VCPUs: 2, State: "running"},
		{Name: "vm2", ID: 2, MemMB: 1024, VCPUs: 1, State: "paused"},
	}
	stopped := []*vm.Meta{{Name: "vm3", Type: "disposable", MemoryMB: 256, VCPUs: 1}}

	want := "NAME             TYPE         STATE         MEM  VCPUS\n" +
		"vm1              app          running       512      2\n" +
		"vm2              app          paused       1024      1\n" +
		"available:\n" +
		"vm3              disposable   stopped       256      1\n"

	require.Equal(t, want, listRows(infos, stopped, map[string]*vm.Meta{
		"vm1": {Name: "vm1", Type: "app"},
		"vm2": {Name: "vm2", Type: "app"},
	}))
}

func TestListRowsNoStopped(t *testing.T) {
	infos := []xenctl.DomainInfo{{Name: "vm1", ID: 1, MemMB: 512, VCPUs: 2, State: "running"}}
	want := "NAME             TYPE         STATE         MEM  VCPUS\n" +
		"vm1              app          running       512      2\n"
	require.Equal(t, want, listRows(infos, nil, map[string]*vm.Meta{"vm1": {Name: "vm1", Type: "app"}}))
}

func TestStopForcedShutdownOK(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 10*time.Millisecond)
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningSeq: []bool{false}}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1"}, events, "domain gone after shutdown — no destroy")
}

func TestStopForcedGoneAfterPolls(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 10*time.Millisecond)
	// Present twice, then gone: the poll loop must observe the exit.
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningSeq: []bool{true, true, false}}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1"}, events, "domain left on its own — no destroy")
}

func TestStopForcedLingersDestroys(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 5*time.Millisecond)
	// Still present through the whole (shortened) max wait — zombie path.
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningSeq: []bool{true, true, true, true, true, true, true, true, true, true}}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events, "linger past max wait — destroy follows")
}

func TestStopForcedLingersDestroyErrorSurfaces(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 2*time.Millisecond)
	killErr := errors.New("destroy failed")
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningSeq: []bool{true, true, true, true, true}, destroyErr: killErr}
	err := stopForced(x, "vm1")
	require.ErrorIs(t, err, killErr, "lingering destroy error surfaces")
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events)
}

func TestStopForcedRunningErrorSurfaces(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 10*time.Millisecond)
	pollErr := errors.New("libxl context broken")
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningErr: pollErr}
	err := stopForced(x, "vm1")
	require.ErrorIs(t, err, pollErr, "Running error surfaces instead of masquerading as gone")
	require.Equal(t, []string{"shutdown:vm1"}, events, "no destroy when the context is broken")
}

func TestStopForcedShutdownRefusedDestroys(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 10*time.Millisecond)
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, shutdownErr: errors.New("no acpi response")}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events, "shutdown refused — destroy follows")
}

func TestStopCmdWaitsForShutdown(t *testing.T) {
	var events []string
	shortStopSeams(t, time.Millisecond, 10*time.Millisecond)
	old := xenctlNew
	defer func() { xenctlNew = old }()
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, runningSeq: []bool{true, false}}
	xenctlNew = func() (xenctl.Xen, error) { return x, nil }

	cmd := stopCmd()
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, []string{"shutdown:vm1"}, events)
	require.Empty(t, x.runningSeq, "stopCmd must poll Running until the domain disappears")
}

func TestStopForcedBothFailSurfacesBothErrors(t *testing.T) {
	var events []string
	acpiErr := errors.New("no acpi response")
	killErr := errors.New("domain already gone")
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, shutdownErr: acpiErr, destroyErr: killErr}
	err := stopForced(x, "vm1")
	require.ErrorIs(t, err, acpiErr, "shutdown error must not be dropped when destroy also fails")
	require.ErrorIs(t, err, killErr, "destroy error surfaces when both calls fail")
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events)
}
