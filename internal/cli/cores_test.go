package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func TestCoresCmd(t *testing.T) {
	fc := &fakeClient{
		getCoremapResult: &agent.CoremapInfo{
			TotalCores: 8,
			PCores:     []int{0, 1, 2, 3},
			ECores:     []int{4, 5, 6, 7},
			Dom0Cores:  []int{0, 1},
			Assignments: []agent.CoremapAssignmentInfo{
				{VM: "vm2", Cores: []int{2, 3}},
				{VM: "vm1", Cores: []int{4, 5}},
			},
		},
	}
	useFakeClient(t, fc)

	cmd := coresCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	got := out.String()
	assert.Contains(t, got, "Topology: 8 cores (4 P, 4 E)")
	assert.Contains(t, got, "P cores: 0-3")
	assert.Contains(t, got, "E cores: 4-7")
	assert.Contains(t, got, "dom0: cores 0-1")
	// Assignments are sorted by VM name: vm1 before vm2.
	assert.Contains(t, got, "vm1")
	assert.Contains(t, got, "cores 4-5")
	assert.Contains(t, got, "vm2")
	assert.Contains(t, got, "cores 2-3")
	assert.Contains(t, got, "Free: 6-7")
}

func TestCoresCmdNoAssignments(t *testing.T) {
	fc := &fakeClient{
		getCoremapResult: &agent.CoremapInfo{
			TotalCores: 4,
			PCores:     []int{0, 1},
			ECores:     []int{2, 3},
			Dom0Cores:  []int{0},
		},
	}
	useFakeClient(t, fc)

	cmd := coresCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	got := out.String()
	assert.Contains(t, got, "Topology: 4 cores (2 P, 2 E)")
	assert.Contains(t, got, "dom0: cores 0")
	assert.Contains(t, got, "No VMs assigned.")
}

func TestCoresCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := coresCmd()
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestCoresCmdError(t *testing.T) {
	fc := &fakeClient{getCoremapErr: errors.New("get coremap failed")}
	useFakeClient(t, fc)

	cmd := coresCmd()
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get coremap failed")
}

func TestCoresList(t *testing.T) {
	tests := []struct {
		cores []int
		want  string
	}{
		{nil, "(none)"},
		{[]int{6}, "6"},
		{[]int{6, 7}, "6-7"},
		{[]int{6, 7, 8}, "6-8"},
		{[]int{0, 1}, "0-1"},
		{[]int{0, 1, 6, 7}, "0-1, 6-7"},
		{[]int{6, 8, 10}, "6, 8, 10"},
		{[]int{6, 7, 9, 10}, "6-7, 9-10"},
	}
	for _, tt := range tests {
		got := coresList(tt.cores)
		if got != tt.want {
			t.Errorf("coresList(%v) = %q, want %q", tt.cores, got, tt.want)
		}
	}
}
