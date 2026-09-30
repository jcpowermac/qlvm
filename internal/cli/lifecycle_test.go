package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// fakeXen records stopForced calls in f.events and injects errors.
type fakeXen struct {
	events      *[]string
	running     map[string]bool
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
	return f.running[name], nil
}
func (f *fakeXen) Close() error { return nil }

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
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1"}, events, "graceful stop succeeded — no destroy")
}

func TestStopForcedShutdownRefusedDestroys(t *testing.T) {
	var events []string
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, shutdownErr: errors.New("no acpi response")}
	require.NoError(t, stopForced(x, "vm1"))
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events, "shutdown refused — destroy follows")
}

func TestStopForcedBothFailSurfacesDestroyError(t *testing.T) {
	var events []string
	killErr := errors.New("domain already gone")
	x := &fakeXen{events: &events, running: map[string]bool{"vm1": true}, shutdownErr: errors.New("no acpi response"), destroyErr: killErr}
	err := stopForced(x, "vm1")
	require.ErrorIs(t, err, killErr, "destroy error surfaces when both calls fail")
	require.Equal(t, []string{"shutdown:vm1", "destroy:vm1"}, events)
}
