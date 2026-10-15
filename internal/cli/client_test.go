package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// fakeClient is a test double for DaemonClient that records method calls.
type fakeClient struct {
	// Read methods
	listVMsResult       []agent.VMInfo
	listVMsErr          error
	listTemplatesResult []agent.TemplateInfo
	listTemplatesErr    error
	getCoremapResult    *agent.CoremapInfo
	getCoremapErr       error

	// Write methods
	createVMResult      string
	createVMErr         error
	createVMCalls       []*agent.CreateVMOptions
	deleteVMCalls       []string
	deleteVMErr         error
	startVMCalls        []string
	startVMErr          error
	stopVMCalls         []string
	stopVMErr           error
	killVMCalls         []string
	killVMErr           error
	restartVMCalls      []string
	restartVMErr        error
	createTemplateResult string
	createTemplateErr    error
	createTemplateCalls  []struct {
		image string
		force bool
	}
	deleteTemplateCalls []string
	deleteTemplateErr   error
	provisionVMCalls    []string
	provisionVMErr      error
	syncKernelCalls     []string
	syncKernelErr       error

	// Progress
	progressCallback func(id, message string)
}

func (f *fakeClient) ListVMs() ([]agent.VMInfo, error) {
	return f.listVMsResult, f.listVMsErr
}

func (f *fakeClient) ListTemplates() ([]agent.TemplateInfo, error) {
	return f.listTemplatesResult, f.listTemplatesErr
}

func (f *fakeClient) GetCoremap() (*agent.CoremapInfo, error) {
	return f.getCoremapResult, f.getCoremapErr
}

func (f *fakeClient) CreateVM(opts *agent.CreateVMOptions) (string, error) {
	f.createVMCalls = append(f.createVMCalls, opts)
	return f.createVMResult, f.createVMErr
}

func (f *fakeClient) DeleteVM(name string) error {
	f.deleteVMCalls = append(f.deleteVMCalls, name)
	return f.deleteVMErr
}

func (f *fakeClient) StartVM(name string) error {
	f.startVMCalls = append(f.startVMCalls, name)
	return f.startVMErr
}

func (f *fakeClient) StopVM(name string) error {
	f.stopVMCalls = append(f.stopVMCalls, name)
	return f.stopVMErr
}

func (f *fakeClient) KillVM(name string) error {
	f.killVMCalls = append(f.killVMCalls, name)
	return f.killVMErr
}

func (f *fakeClient) RestartVM(name string) error {
	f.restartVMCalls = append(f.restartVMCalls, name)
	return f.restartVMErr
}

func (f *fakeClient) CreateTemplate(image string, force bool) (string, error) {
	f.createTemplateCalls = append(f.createTemplateCalls, struct {
		image string
		force bool
	}{image, force})
	return f.createTemplateResult, f.createTemplateErr
}

func (f *fakeClient) DeleteTemplate(dir string) error {
	f.deleteTemplateCalls = append(f.deleteTemplateCalls, dir)
	return f.deleteTemplateErr
}

func (f *fakeClient) ProvisionVM(name string) error {
	f.provisionVMCalls = append(f.provisionVMCalls, name)
	return f.provisionVMErr
}

func (f *fakeClient) SyncKernel(name string) error {
	f.syncKernelCalls = append(f.syncKernelCalls, name)
	return f.syncKernelErr
}

func (f *fakeClient) SubscribeProgress(callback func(id, message string)) func() {
	f.progressCallback = callback
	return func() {}
}

// useFakeClient installs the given fake client as the shared client and
// restores the original state on test cleanup.
func useFakeClient(t *testing.T, fc *fakeClient) {
	t.Helper()
	oldFactory := clientFactory
	oldClient := client
	clientFactory = func() (DaemonClient, error) { return fc, nil }
	client = fc
	t.Cleanup(func() {
		clientFactory = oldFactory
		client = oldClient
	})
}

// TestGetClientReturnsErrorWhenFactoryFails verifies that getClient returns
// a clear error when the factory fails.
func TestGetClientReturnsErrorWhenFactoryFails(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("connection refused")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	_, err := getClient()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

// TestGetClientIsLazy verifies that getClient only connects on first use.
func TestGetClientIsLazy(t *testing.T) {
	resetClient()
	old := clientFactory
	calls := 0
	clientFactory = func() (DaemonClient, error) {
		calls++
		return nil, fmt.Errorf("not implemented")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	// First call should invoke the factory.
	_, err := getClient()
	require.Error(t, err)
	assert.Equal(t, 1, calls, "factory should be called once on first use")

	// Second call should return the cached (nil) client without calling
	// the factory again. Since the first call failed, client is still nil,
	// so the factory will be called again. This is expected behavior: a
	// failed connection is not cached.
	_, err = getClient()
	require.Error(t, err)
	assert.Equal(t, 2, calls, "factory should be called again after a failure")
}

// TestGetClientCachesSuccess verifies that getClient caches a successful
// connection and does not call the factory again.
func TestGetClientCachesSuccess(t *testing.T) {
	resetClient()
	old := clientFactory
	calls := 0
	fc := &fakeClient{}
	clientFactory = func() (DaemonClient, error) {
		calls++
		return fc, nil
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	c1, err := getClient()
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, fc, c1)

	c2, err := getClient()
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "factory should not be called again")
	assert.Equal(t, fc, c2)
}

// TestAgentClientSatisfiesDaemonClient verifies that agent.Client
// implements the DaemonClient interface.
func TestAgentClientSatisfiesDaemonClient(_ *testing.T) {
	// This is a compile-time check: if agent.Client doesn't implement
	// DaemonClient, this assignment will fail to compile.
	var _ DaemonClient = (*agent.Client)(nil)
}
