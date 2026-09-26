package systemd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn records D-Bus method calls with canned unit state.
type fakeConn struct {
	calls     []string
	active    string
	fileState string
}

func (f *fakeConn) UnitActive(unit string) (string, error) {
	f.calls = append(f.calls, "UnitActive:"+unit)
	return f.active, nil
}
func (f *fakeConn) UnitFileState(unit string) (string, error) {
	f.calls = append(f.calls, "UnitFileState:"+unit)
	return f.fileState, nil
}
func (f *fakeConn) StartUnit(unit string) error {
	f.calls = append(f.calls, "StartUnit:"+unit)
	return nil
}
func (f *fakeConn) EnableUnit(unit string) error {
	f.calls = append(f.calls, "EnableUnit:"+unit)
	return nil
}

func TestEnableStartSkipsActive(t *testing.T) {
	f := &fakeConn{active: "active", fileState: "enabled"}
	m := New(f)
	require.NoError(t, m.EnableStart(context.Background(), "qlvm-ovs.service"))
	assert.Equal(t, []string{
		"UnitActive:qlvm-ovs.service",
		"UnitFileState:qlvm-ovs.service",
	}, f.calls)
}

func TestEnableStartStartsAndEnables(t *testing.T) {
	f := &fakeConn{active: "inactive", fileState: "disabled"}
	m := New(f)
	require.NoError(t, m.EnableStart(context.Background(), "qlvm-ovs.service"))
	assert.Equal(t, []string{
		"UnitActive:qlvm-ovs.service",
		"UnitFileState:qlvm-ovs.service",
		"StartUnit:qlvm-ovs.service",
		"EnableUnit:qlvm-ovs.service",
	}, f.calls)
}
