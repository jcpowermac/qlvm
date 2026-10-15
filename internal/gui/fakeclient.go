package gui

import (
	"sync"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// fakeClient is a test implementation of the Client interface.
type fakeClient struct {
	mu          sync.Mutex
	vmInfos     []agent.VMInfo
	tplInfos    []agent.TemplateInfo
	startCalls  []string
	stopCalls   []string
	killCalls   []string
	restartCalls []string
	deleteCalls []string
	createVMCalls []*agent.CreateVMOptions
	createTplCalls []struct {
		image string
		force bool
	}
	deleteTplCalls []string
	progressFunc  func(id, message string)
	err           error
}

func newFakeClient() *fakeClient {
	return &fakeClient{}
}

func (f *fakeClient) SetVMs(infos []agent.VMInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vmInfos = infos
}

func (f *fakeClient) SetTemplates(infos []agent.TemplateInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tplInfos = infos
}

func (f *fakeClient) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeClient) ListVMs() ([]agent.VMInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.vmInfos, nil
}

func (f *fakeClient) ListTemplates() ([]agent.TemplateInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.tplInfos, nil
}

func (f *fakeClient) StartVM(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, name)
	return f.err
}

func (f *fakeClient) StopVM(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, name)
	return f.err
}

func (f *fakeClient) KillVM(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killCalls = append(f.killCalls, name)
	return f.err
}

func (f *fakeClient) RestartVM(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalls = append(f.restartCalls, name)
	return f.err
}

func (f *fakeClient) DeleteVM(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, name)
	return f.err
}

func (f *fakeClient) CreateVM(opts *agent.CreateVMOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createVMCalls = append(f.createVMCalls, opts)
	if f.err != nil {
		return "", f.err
	}
	return opts.Name, nil
}

func (f *fakeClient) CreateTemplate(image string, force bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createTplCalls = append(f.createTplCalls, struct {
		image string
		force bool
	}{image, force})
	if f.err != nil {
		return "", f.err
	}
	return image, nil
}

func (f *fakeClient) DeleteTemplate(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteTplCalls = append(f.deleteTplCalls, dir)
	return f.err
}

func (f *fakeClient) SubscribeProgress(callback func(id, message string)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progressFunc = callback
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.progressFunc = nil
	}
}

func (f *fakeClient) Close() error {
	return nil
}


