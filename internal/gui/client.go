package gui

import "github.com/jcpowermac/qlvm/internal/agent"

// Client is the interface for the qlvm D-Bus agent client. The real
// implementation is agent.Client; tests use a fake implementation.
type Client interface {
	ListVMs() ([]agent.VMInfo, error)
	ListTemplates() ([]agent.TemplateInfo, error)
	StartVM(name string) error
	StopVM(name string) error
	KillVM(name string) error
	RestartVM(name string) error
	DeleteVM(name string) error
	CreateVM(opts *agent.CreateVMOptions) (string, error)
	CreateTemplate(image string, force bool) (string, error)
	DeleteTemplate(dir string) error
	SubscribeProgress(callback func(id, message string)) func()
	Close() error
}

// Compile-time check that agent.Client satisfies the Client interface.
var _ Client = (*agent.Client)(nil)
