package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateCmd(t *testing.T) {
	fc := &fakeClient{
		createVMResult: "vm1",
	}
	useFakeClient(t, fc)

	cmd := createCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Flags().Set("template", "os-bolt-sha256:aaa"))
	require.NoError(t, cmd.Flags().Set("type", "app"))
	require.NoError(t, cmd.Flags().Set("memory", "512"))
	require.NoError(t, cmd.Flags().Set("vcpus", "2"))
	require.NoError(t, cmd.Flags().Set("mount", "/host/path:/guest/path"))
	require.NoError(t, cmd.Flags().Set("cores", "6-7"))
	require.NoError(t, cmd.Execute())

	require.Len(t, fc.createVMCalls, 1)
	opts := fc.createVMCalls[0]
	assert.Equal(t, "vm1", opts.Name)
	assert.Equal(t, "app", opts.Type)
	assert.Equal(t, "os-bolt-sha256:aaa", opts.Template)
	assert.Equal(t, uint64(512), opts.MemoryMB)
	assert.Equal(t, uint8(2), opts.VCPUs)
	assert.Equal(t, []string{"/host/path:/guest/path"}, opts.Mounts)
	assert.Equal(t, "6-7", opts.Cores)
	assert.Contains(t, out.String(), "created vm1")
}

func TestCreateCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := createCmd()
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Flags().Set("template", "os-bolt-sha256:aaa"))
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestCreateCmdError(t *testing.T) {
	fc := &fakeClient{
		createVMErr: errors.New("create failed"),
	}
	useFakeClient(t, fc)

	cmd := createCmd()
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Flags().Set("template", "os-bolt-sha256:aaa"))
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create failed")
}

func TestCreateCmdRequiresTemplate(t *testing.T) {
	fc := &fakeClient{}
	useFakeClient(t, fc)

	cmd := createCmd()
	cmd.SetArgs([]string{"vm1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
}

func TestParseCoresFlagPassthrough(t *testing.T) {
	// parseCoresFlag is now a passthrough to the daemon.
	assert.Equal(t, "", parseCoresFlag(""))
	assert.Equal(t, "6-7", parseCoresFlag("6-7"))
	assert.Equal(t, "6,7,8", parseCoresFlag("6,7,8"))
}
