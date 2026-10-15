package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncKernelCmd(t *testing.T) {
	fc := &fakeClient{}
	useFakeClient(t, fc)

	cmd := syncKernelCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"vm1"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"vm1"}, fc.syncKernelCalls)
	assert.Contains(t, out.String(), "synced kernel to vm1")
	assert.Contains(t, out.String(), "qlvm vm stop vm1 && qlvm vm start vm1")
}

func TestSyncKernelCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := syncKernelCmd()
	cmd.SetArgs([]string{"vm1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestSyncKernelCmdError(t *testing.T) {
	fc := &fakeClient{
		syncKernelErr: errors.New("sync failed"),
	}
	useFakeClient(t, fc)

	cmd := syncKernelCmd()
	cmd.SetArgs([]string{"vm1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync-kernel vm1")
}
