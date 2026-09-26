package nm

import (
	"context"
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn records D-Bus method calls and tracks existing con-names.
type fakeConn struct {
	calls    []string
	existing []string
}

func (f *fakeConn) ConNames() ([]string, error) {
	f.calls = append(f.calls, "ConNames")
	return f.existing, nil
}
func (f *fakeConn) AddConnection(spec map[string]map[string]any) error {
	id, _ := spec["connection"]["id"].(string)
	f.calls = append(f.calls, "AddConnection:"+id)
	f.existing = append(f.existing, id)
	return nil
}
func (f *fakeConn) SetConnectionValue(conName, key, value string) error {
	f.calls = append(f.calls, "SetConnectionValue:"+conName+":"+key+"="+value)
	return nil
}
func (f *fakeConn) Activate(conName, dev string) error {
	f.calls = append(f.calls, "Activate:"+conName+":"+dev)
	return nil
}
func (f *fakeConn) Deactivate(conName string) error {
	f.calls = append(f.calls, "Deactivate:"+conName)
	return nil
}

func testConfig() *config.Config {
	return &config.Config{
		Network: config.Network{NIC: "enp3s0", NICConnection: "Wired connection 1"},
	}
}

func TestMigrateNICRefusesSSH(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.168.1.5 51000 10.100.1.10 22")

	f := &fakeConn{}
	m := New(f)
	err := m.MigrateNIC(context.Background(), testConfig(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-nic-migration")
	assert.Empty(t, f.calls)

	f2 := &fakeConn{}
	m2 := New(f2)
	require.NoError(t, m2.MigrateNIC(context.Background(), testConfig(), true))
	assert.Equal(t, []string{
		"Activate:br-ex:br-ex",
		"Deactivate:Wired connection 1",
	}, f2.calls)
}

func TestEnsureOVSConnectionsSkipsExisting(t *testing.T) {
	f := &fakeConn{existing: []string{"br-ex"}}
	m := New(f)
	require.NoError(t, m.EnsureOVSConnections(context.Background(), testConfig()))
	assert.Equal(t, []string{
		"ConNames",
		"AddConnection:br-ex-port",
		"AddConnection:br-ex-iface",
		"AddConnection:enp3s0-port",
		"AddConnection:enp3s0-ovs",
	}, f.calls)
}
