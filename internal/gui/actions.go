package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// Actions provides VM lifecycle operations bound to the app model and
// underlying control planes.
type Actions struct {
	model    *AppModel
	xen      xenctl.Xen
	ovn      vm.OVNPorter
	ovs      ovs.VifPorter
	cfg      *config.Config
	coremap  string
	log      func(string) // log callback for operation output
}

// NewActions creates an Actions bound to the app model and control planes.
func NewActions(model *AppModel, x xenctl.Xen, o vm.OVNPorter, v ovs.VifPorter, cfg *config.Config, coremapPath string, log func(string)) *Actions {
	return &Actions{
		model:   model,
		xen:     x,
		ovn:     o,
		ovs:     v,
		cfg:     cfg,
		coremap: coremapPath,
		log:     log,
	}
}

// Start starts the selected VM.
func (a *Actions) Start() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}

	vms := a.model.GetVMs()
	meta, ok := vms[name]
	if !ok {
		return fmt.Errorf("VM %q not found", name)
	}

	vmDir := a.vmDir(name)

	// Load template.
	templates := a.model.GetTemplates()
	var tpl *template.Template
	for _, t := range templates {
		if t.Image == meta.Image {
			tpl = t
			break
		}
	}
	if tpl == nil {
		// Try to load from the template directory.
		tplDir := a.templateDir(meta.Image)
		tpl, _ = template.LoadMeta(tplDir)
	}

	a.log(fmt.Sprintf("Starting VM %s...", name))
	ctx := context.Background()
	if err := xenctl.Start(ctx, a.xen, a.ovs, meta, vmDir, tpl); err != nil {
		a.log(fmt.Sprintf("Failed to start %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s started.", name))
	return nil
}

// Stop stops the selected VM gracefully.
func (a *Actions) Stop() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}

	a.log(fmt.Sprintf("Stopping VM %s...", name))
	if err := a.xen.Shutdown(name); err != nil {
		a.log(fmt.Sprintf("Failed to stop %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s stopping.", name))
	return nil
}

// Kill force-destroys the selected VM.
func (a *Actions) Kill() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}

	a.log(fmt.Sprintf("Killing VM %s...", name))
	if err := a.xen.Destroy(name); err != nil {
		a.log(fmt.Sprintf("Failed to kill %s: %v", name, err))
		return err
	}
	a.log(fmt.Sprintf("VM %s killed.", name))
	return nil
}

// Restart stops and starts the selected VM.
func (a *Actions) Restart() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}

	a.log(fmt.Sprintf("Restarting VM %s...", name))
	if err := a.xen.Destroy(name); err != nil {
		return fmt.Errorf("failed to destroy: %w", err)
	}
	if err := a.Start(); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}
	a.log(fmt.Sprintf("VM %s restarted.", name))
	return nil
}

// Delete removes the selected VM.
func (a *Actions) Delete() error {
	name := a.model.SelectedVMName()
	if name == "" {
		return fmt.Errorf("no VM selected")
	}

	a.log(fmt.Sprintf("Deleting VM %s...", name))
	ctx := context.Background()
	vmDir := a.vmDir(name)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	if err := xenctl.Delete(ctx, a.xen, a.ovn, a.ovs, home, vmDir, name, a.coremap); err != nil {
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

// installRoot is the per-VM/template state tree (spec §5.2).
const installRoot = "/var/lib/qvm"

// vmDir returns the VM directory path.
func (a *Actions) vmDir(name string) string {
	return filepath.Join(installRoot, "vms", name)
}

// templateDir returns the template directory path for an image.
func (a *Actions) templateDir(image string) string {
	// Template directories are named after the image ref.
	return filepath.Join(installRoot, "templates", image)
}

// CoresView returns the current coremap data.
func (a *Actions) CoresView() (*coremap.Coremap, error) {
	if a.coremap == "" {
		return nil, fmt.Errorf("no coremap path configured")
	}
	return coremap.Load(a.coremap)
}
