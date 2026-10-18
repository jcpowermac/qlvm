// Command qlvm-tray is the Qubes-style system tray for qlvm. It runs as the
// desktop user inside a Wayland session, polls the qlvm-agent daemon for VM
// states, and renders a per-VM menu (whitelist-filtered apps + start/stop/
// sync). Clicking an app starts the VM if it is stopped, waits for the guest
// control channel, and projects the app over waypipe (ssh mode, v1).
//
// Usage:
//
//	qlvm-tray [-cache-dir DIR]              run the tray (default)
//	qlvm-tray install-system-files          install + enable the user unit
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/apps"
	"github.com/jcpowermac/qlvm/internal/projection"
	"github.com/jcpowermac/qlvm/internal/tray"
	"github.com/jcpowermac/qlvm/internal/vm"
)

const (
	defaultCacheDir = "/var/lib/qlvm/desktop-cache"
	qlvmRoot        = "/var/lib/qlvm"
	pollEvery       = 2 * time.Second
)

// runWaypipe execs waypipe — the single local process qlvm may launch. The
// tray runs as the user in a Wayland session, so WAYLAND_DISPLAY is set.
var runWaypipe = func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("WAYLAND_DISPLAY is empty: qlvm-tray must run in a Wayland session")
	}
	cmd := exec.Command("waypipe", args...) // #nosec G204 -- waypipe is the single permitted local exec (design spec)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install-system-files" {
		if err := installSystemFiles(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runTray(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func runTray(args []string) error {
	fs := flag.NewFlagSet("qlvm-tray", flag.ExitOnError)
	cacheDir := fs.String("cache-dir", defaultCacheDir, "desktop cache dir (whitelist + cached apps)")
	fs.Parse(args)

	cl, err := agent.NewClient()
	if err != nil {
		return err
	}
	defer cl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var lastStates []tray.VMState

	running := func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range lastStates {
			if s.Name == name {
				return s.State == "running"
			}
		}
		return false
	}

	// Busy tracking: one action per VM in flight; its items are disabled
	// while it runs.
	var busyMu sync.Mutex
	busy := make(map[string]bool)
	dirty := make(chan struct{}, 1)
	isBusy := func(vm string) bool {
		busyMu.Lock()
		defer busyMu.Unlock()
		return busy[vm]
	}
	fireDirty := func() {
		select {
		case dirty <- struct{}{}:
		default:
		}
	}

	waitControl := func(name string) error {
		m, err := vm.LoadMeta(filepath.Join(qlvmRoot, "vms", name))
		if err != nil {
			return err
		}
		return projection.WaitControl(ctx, m.IP, projection.ConnectSSH, time.Second, 60*time.Second)
	}
	runApp := func(vmName, execName string) error {
		m, err := vm.LoadMeta(filepath.Join(qlvmRoot, "vms", vmName))
		if err != nil {
			return fmt.Errorf("run %s: %w", vmName, err)
		}
		return projection.Run(ctx, projection.Deps{Waypipe: runWaypipe},
			m, projection.ConnectSSH, "", execName, os.Stdin, os.Stdout, os.Stderr)
	}

	notify := func(msg string) { systray.SetTooltip("qlvm: " + msg) }
	actions := tray.Actions{
		StartVM: func(name string) error { return cl.StartVM(name) },
		StopVM:  func(name string) error { return cl.StopVM(name) },
		SyncVM:  func(name string) (int, error) { return tray.SyncVMFor(running, cl.SyncApps, name) },
		SyncAll: func() error {
			mu.Lock()
			defer mu.Unlock()
			var firstErr error
			for _, s := range lastStates {
				if s.State != "running" {
					continue
				}
				if _, err := cl.SyncApps(s.Name); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			return firstErr
		},
		LaunchApp: func(vmName, execName string) error {
			return tray.LaunchApp(running, cl.StartVM, waitControl, runApp, notify, vmName, execName)
		},
		Quit:   systray.Quit,
		Notify: notify,
	}

	// Dispatch wraps the base ID→callback mapping with per-VM busy gating.
	base := tray.Dispatch(actions)
	dispatch := func(id string) {
		v, hasVM := tray.VMOfID(id)
		if !hasVM {
			go base(id)
			return
		}
		busyMu.Lock()
		if busy[v] {
			busyMu.Unlock()
			notify("already working on " + v)
			return
		}
		busy[v] = true
		busyMu.Unlock()
		go func() {
			defer func() {
				busyMu.Lock()
				delete(busy, v)
				busyMu.Unlock()
				fireDirty()
			}()
			base(id)
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		systray.Quit()
	}()

	renderer := tray.NewRenderer(dispatch)
	systray.Run(func() {
		systray.SetIcon(iconPNG)
		systray.SetTooltip("qlvm")
		go poll(ctx, cl, *cacheDir, renderer, &mu, &lastStates, isBusy, dirty)
	}, func() {
		cancel()
	})
	return nil
}

// poll refreshes VM states on a ticker (and immediately when an action
// finishes, via dirty) and re-renders the menu only when the model's
// signature changes. A failed ListVMs (daemon down) renders a single
// disabled "unreachable" item.
func poll(ctx context.Context, cl *agent.Client, cacheDir string,
	renderer *tray.Renderer, mu *sync.Mutex, lastStates *[]tray.VMState,
	isBusy func(string) bool, dirty <-chan struct{}) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	lastSig := ""
	for {
		infos, err := cl.ListVMs()
		if err != nil {
			if lastSig != "unreachable" {
				renderer.RenderUnreachable("qlvm agent unreachable: " + err.Error())
				lastSig = "unreachable"
			}
		} else {
			states := toVMStates(infos)
			mu.Lock()
			*lastStates = states
			mu.Unlock()

			tree := tray.ComputeMenu(states, func(vmName string) ([]apps.AppEntry, error) {
				return apps.VisibleApps(cacheDir, vmName)
			})
			tree = tray.WithBusy(tree, isBusy)
			if sig := tree.Signature(); sig != lastSig {
				renderer.Render(tree)
				lastSig = sig
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-dirty:
		}
	}
}

// toVMStates maps daemon VMInfo rows to the tray's per-VM view.
func toVMStates(infos []agent.VMInfo) []tray.VMState {
	out := make([]tray.VMState, 0, len(infos))
	for _, i := range infos {
		out = append(out, tray.VMState{Name: i.Name, State: i.State})
	}
	return out
}
