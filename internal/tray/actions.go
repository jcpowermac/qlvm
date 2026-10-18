package tray

import "fmt"

// VMMenuLabel returns the top-level item text for a VM, with a state marker.
func (m VMMenu) VMMenuLabel() string {
	return stateMarker(m.State) + " " + m.Name
}

// LaunchApp runs one tray app selection, mirroring `qlvm apps` (see
// apps.Launch): a stopped VM is started, then waited on for its control
// channel, before the app is projected over waypipe (ssh mode, v1). Each
// failing seam stops the chain and reports through notify.
func LaunchApp(
	running func(vm string) bool,
	start func(vm string) error,
	waitControl func(vm string) error,
	runApp func(vm, exec string) error,
	notify func(msg string),
	vm, exec string,
) error {
	if !running(vm) {
		if err := start(vm); err != nil {
			notify(fmt.Sprintf("start %s: %v", vm, err))
			return err
		}
		if err := waitControl(vm); err != nil {
			notify(fmt.Sprintf("wait %s: %v", vm, err))
			return err
		}
	}
	if err := runApp(vm, exec); err != nil {
		notify(fmt.Sprintf("run %s/%s: %v", vm, exec, err))
		return err
	}
	return nil
}

// SyncVMFor refreshes one VM's desktop cache. The daemon fetches the list over
// SSH, which requires a running VM, so a stopped VM is a clear, actionable
// error rather than an opaque ssh dial failure.
func SyncVMFor(running func(vm string) bool, sync func(vm string) (int, error), name string) (int, error) {
	if !running(name) {
		return 0, fmt.Errorf("sync %s: start the VM first (its desktop list is fetched over SSH)", name)
	}
	return sync(name)
}
