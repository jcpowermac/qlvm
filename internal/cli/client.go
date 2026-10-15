package cli

import (
	"fmt"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// DaemonClient is the interface of agent.Client methods used by the CLI.
// It is a subset of agent.Client that the CLI actually calls. Tests can
// implement this interface with a fake to avoid a live daemon.
type DaemonClient interface {
	// Read methods
	ListVMs() ([]agent.VMInfo, error)
	ListTemplates() ([]agent.TemplateInfo, error)
	GetCoremap() (*agent.CoremapInfo, error)

	// Write methods
	CreateVM(opts *agent.CreateVMOptions) (string, error)
	DeleteVM(name string) error
	StartVM(name string) error
	StopVM(name string) error
	KillVM(name string) error
	RestartVM(name string) error
	CreateTemplate(image string, force bool) (string, error)
	DeleteTemplate(dir string) error
	ProvisionVM(name string) error
	SyncKernel(name string) error

	// Progress
	SubscribeProgress(callback func(id, message string)) func()
}

// client is the shared D-Bus client for commands that delegate to the
// daemon. Lazy-initialized on first use; commands that stay CLI-side
// (vm run, vm console, apps) never call getClient.
var client DaemonClient

// clientFactory creates a new DaemonClient. Replaced in tests with a
// fake client factory.
var clientFactory = func() (DaemonClient, error) {
	c, err := agent.NewClient()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// getClient returns the shared D-Bus client, connecting on first use.
// Returns a clear error if the daemon is not running.
func getClient() (DaemonClient, error) {
	if client == nil {
		c, err := clientFactory()
		if err != nil {
			return nil, fmt.Errorf("qlvm daemon is not running or not reachable: %w\n"+
				"Start the daemon with: sudo systemctl start qlvm-agent", err)
		}
		client = c
	}
	return client, nil
}

// resetClient clears the shared client. Used in tests.
func resetClient() {
	client = nil
}
