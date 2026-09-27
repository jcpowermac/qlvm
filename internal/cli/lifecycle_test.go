package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

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
