package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvisionCmd(t *testing.T) {
	fc := &fakeClient{}
	useFakeClient(t, fc)

	cmd := provisionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"vm1"}, fc.provisionVMCalls)
	assert.Contains(t, out.String(), "provisioned vm1")
}

func TestProvisionCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := provisionCmd()
	cmd.SetArgs([]string{"vm1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestProvisionCmdError(t *testing.T) {
	fc := &fakeClient{
		provisionVMErr: errors.New("provision failed"),
	}
	useFakeClient(t, fc)

	cmd := provisionCmd()
	cmd.SetArgs([]string{"vm1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision vm1")
}
