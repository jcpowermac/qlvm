package gui

import "fmt"

// Actions provides VM lifecycle operations bound to the app model and the
// D-Bus agent client.
type Actions struct {
	model  *AppModel
	client Client
	log    func(string) // log callback for operation output
}

// NewActions creates an Actions bound to the app model and agent client.
func NewActions(model *AppModel, c Client, log func(string)) *Actions {
	return &Actions{
		model:  model,
		client: c,
		log:    log,
	}
}

// Start starts the selected VM via the agent.
func (a *Actions) Start() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}
	if a.client == nil {
		return fmt.Errorf("agent not connected")
	}

	a.log(fmt.Sprintf("Starting VM %s...", name))
	if err := a.client.StartVM(name); err != nil {
		a.log(fmt.Sprintf("Failed to start %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s started.", name))
	return nil
}

// Stop stops the selected VM gracefully via the agent.
func (a *Actions) Stop() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}
	if a.client == nil {
		return fmt.Errorf("agent not connected")
	}

	a.log(fmt.Sprintf("Stopping VM %s...", name))
	if err := a.client.StopVM(name); err != nil {
		a.log(fmt.Sprintf("Failed to stop %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s stopping.", name))
	return nil
}

// Kill force-destroys the selected VM via the agent.
func (a *Actions) Kill() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}
	if a.client == nil {
		return fmt.Errorf("agent not connected")
	}

	a.log(fmt.Sprintf("Killing VM %s...", name))
	if err := a.client.KillVM(name); err != nil {
		a.log(fmt.Sprintf("Failed to kill %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s killed.", name))
	return nil
}

// Restart stops and starts the selected VM via the agent.
func (a *Actions) Restart() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}
	if a.client == nil {
		return fmt.Errorf("agent not connected")
	}

	a.log(fmt.Sprintf("Restarting VM %s...", name))
	if err := a.client.RestartVM(name); err != nil {
		a.log(fmt.Sprintf("Failed to restart %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s restarted.", name))
	return nil
}

// Delete removes the selected VM via the agent.
func (a *Actions) Delete() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}
	if a.client == nil {
		return fmt.Errorf("agent not connected")
	}

	a.log(fmt.Sprintf("Deleting VM %s...", name))
	if err := a.client.DeleteVM(name); err != nil {
		a.log(fmt.Sprintf("Failed to delete %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s deleted.", name))

	// Remove from model.
	vms := a.model.GetVMs()
	delete(vms, name)
	a.model.SetVMs(vms)
	a.model.SelectVM("")
	return nil
}
