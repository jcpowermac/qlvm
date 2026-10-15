package agent

import (
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Client is the D-Bus client used by qlvm-gui (and later the CLI). It
// mirrors Agent's methods but calls them over the system bus. The GUI uses
// Client instead of importing internal packages directly.
type Client struct {
	bus *dbus.Conn
	obj dbus.BusObject
}

// NewClient connects to the system bus and returns a Client bound to the
// Agent. Returns an error if the daemon is not running.
func NewClient() (*Client, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	obj := bus.Object(ServiceName, dbus.ObjectPath(ObjectPath))
	// Verify the daemon is running by pinging the exported object. The
	// Peer.Ping interface is required on all D-Bus objects; if the daemon
	// is not running or has not exported the object, this call fails.
	if err := obj.Call("org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("daemon not running: %w", err)
	}
	return &Client{bus: bus, obj: obj}, nil
}

// --- Read methods ---

// ListVMs returns all VMs (running from Xen, stopped from meta.yaml).
func (c *Client) ListVMs() ([]VMInfo, error) {
	var result []VMInfo
	err := c.obj.Call(Iface+".ListVMs", 0).Store(&result)
	return result, err
}

// ListTemplates returns all templates.
func (c *Client) ListTemplates() ([]TemplateInfo, error) {
	var result []TemplateInfo
	err := c.obj.Call(Iface+".ListTemplates", 0).Store(&result)
	return result, err
}

// ListDomains returns all Xen domains.
func (c *Client) ListDomains() ([]DomainInfo, error) {
	var result []DomainInfo
	err := c.obj.Call(Iface+".ListDomains", 0).Store(&result)
	return result, err
}

// GetVM returns a single VM by name.
func (c *Client) GetVM(name string) (*VMInfo, error) {
	var result *VMInfo
	err := c.obj.Call(Iface+".GetVM", 0, name).Store(&result)
	return result, err
}

// GetTemplate returns a single template by directory.
func (c *Client) GetTemplate(dir string) (*TemplateInfo, error) {
	var result *TemplateInfo
	err := c.obj.Call(Iface+".GetTemplate", 0, dir).Store(&result)
	return result, err
}

// GetConfig returns the current configuration.
func (c *Client) GetConfig() (*ConfigInfo, error) {
	var result *ConfigInfo
	err := c.obj.Call(Iface+".GetConfig", 0).Store(&result)
	return result, err
}

// GetCoremap returns the current CPU core map.
func (c *Client) GetCoremap() (*CoremapInfo, error) {
	var result *CoremapInfo
	err := c.obj.Call(Iface+".GetCoremap", 0).Store(&result)
	return result, err
}

// --- Write methods ---

// CreateVM prepares a new VM from a template. Returns the VM name.
func (c *Client) CreateVM(opts *CreateVMOptions) (string, error) {
	var result string
	err := c.obj.Call(Iface+".CreateVM", 0, opts).Store(&result)
	return result, err
}

// DeleteVM removes a VM (domain, OVN/OVS ports, state dir, ssh block).
func (c *Client) DeleteVM(name string) error {
	return c.obj.Call(Iface+".DeleteVM", 0, name).Err
}

// StartVM boots a prepared VM.
func (c *Client) StartVM(name string) error {
	return c.obj.Call(Iface+".StartVM", 0, name).Err
}

// StopVM gracefully stops a running VM.
func (c *Client) StopVM(name string) error {
	return c.obj.Call(Iface+".StopVM", 0, name).Err
}

// KillVM force-kills a running VM.
func (c *Client) KillVM(name string) error {
	return c.obj.Call(Iface+".KillVM", 0, name).Err
}

// RestartVM restarts a VM (graceful stop, force-kill if it lingers, then start).
func (c *Client) RestartVM(name string) error {
	return c.obj.Call(Iface+".RestartVM", 0, name).Err
}

// CreateTemplate bakes a new template from a container image. Returns the template directory.
func (c *Client) CreateTemplate(image string, force bool) (string, error) {
	var result string
	err := c.obj.Call(Iface+".CreateTemplate", 0, image, force).Store(&result)
	return result, err
}

// DeleteTemplate removes a template directory.
func (c *Client) DeleteTemplate(dir string) error {
	return c.obj.Call(Iface+".DeleteTemplate", 0, dir).Err
}

// ProvisionVM provisions a running VM (installs waypipe, configures ssh).
func (c *Client) ProvisionVM(name string) error {
	return c.obj.Call(Iface+".ProvisionVM", 0, name).Err
}

// SyncKernel updates a VM's kernel from the template.
func (c *Client) SyncKernel(name string) error {
	return c.obj.Call(Iface+".SyncKernel", 0, name).Err
}

// --- Progress ---

// SubscribeProgress registers a callback for progress signals. Returns a
// cancel function. The GUI uses this to update progress bars during
// long-running operations. Progress signals carry (id, message);
// ProgressDone signals carry (id) and invoke the callback with an empty
// message to indicate completion.
func (c *Client) SubscribeProgress(callback func(id, message string)) func() {
	optsProgress := []dbus.MatchOption{
		dbus.WithMatchOption("type", "signal"),
		dbus.WithMatchInterface(Iface),
		dbus.WithMatchMember("Progress"),
	}
	optsDone := []dbus.MatchOption{
		dbus.WithMatchOption("type", "signal"),
		dbus.WithMatchInterface(Iface),
		dbus.WithMatchMember("ProgressDone"),
	}
	if err := c.bus.AddMatchSignal(optsProgress...); err != nil {
		return func() {}
	}
	if err := c.bus.AddMatchSignal(optsDone...); err != nil {
		_ = c.bus.RemoveMatchSignal(optsProgress...)
		return func() {}
	}

	ch := make(chan *dbus.Signal, 16)
	c.bus.Signal(ch)

	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			c.bus.RemoveSignal(ch)
			_ = c.bus.RemoveMatchSignal(optsProgress...)
			_ = c.bus.RemoveMatchSignal(optsDone...)
		})
	}

	go func() {
		for {
			select {
			case sig, ok := <-ch:
				if !ok {
					return
				}
				switch sig.Name {
				case Iface + ".Progress":
					var id, message string
					if err := dbus.Store(sig.Body, &id, &message); err == nil {
						callback(id, message)
					}
				case Iface + ".ProgressDone":
					var id string
					if err := dbus.Store(sig.Body, &id); err == nil {
						callback(id, "")
					}
				}
			case <-done:
				return
			case <-c.bus.Context().Done():
				return
			}
		}
	}()

	return cancel
}

// Close releases the bus connection.
func (c *Client) Close() error {
	return c.bus.Close()
}
