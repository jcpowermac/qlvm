//go:build libxl

// This file is the real xenlight-backed Xen implementation. It compiles
// only with -tags libxl (libxl-devel required); on this host it is written
// but NOT compile-verified — the default build uses xenctl_stub.go.
package xenctl

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"xenbits.xenproject.org/git-http/xen.git/tools/golang/xenlight"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// libxlXen implements Xen on one xenlight.Context per process (Close
// releases it; the CLI defers it).
type libxlXen struct {
	*xenlight.Context
}

// New opens the libxl context.
func New() (Xen, error) {
	c, err := xenlight.NewContext()
	if err != nil {
		return nil, fmt.Errorf("libxl context: %w", err)
	}
	return &libxlXen{c}, nil
}

func (x *libxlXen) Close() error { return x.Context.Close() }

func (x *libxlXen) CreateDomain(spec *vm.DomainSpec) error {
	// The domain config is assembled in C (xenctl_cgo.go); the binding's
	// generated toC cannot express initialized C device structs (see that
	// file's header).
	return cgoCreateDomain(spec)
}

func (x *libxlXen) Destroy(name string) error {
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if err := x.DomainDestroy(d); err != nil {
		return fmt.Errorf("destroy %s: %w", name, err)
	}
	return nil
}

func (x *libxlXen) Shutdown(name string) error {
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if err := x.DomainShutdown(d); err != nil {
		return fmt.Errorf("shutdown %s: %w", name, err)
	}
	return nil
}

func (x *libxlXen) List() ([]DomainInfo, error) {
	doms := x.ListDomain()
	out := make([]DomainInfo, 0, len(doms))
	for _, d := range doms {
		if d.Domid == 0 {
			continue // dom0 is not a VM
		}
		out = append(out, DomainInfo{
			Name:  x.DomidToName(d.Domid),
			ID:    uint32(d.Domid),
			MemMB: d.CurrentMemkb / 1024,
			VCPUs: uint8(d.VcpuOnline),
			State: stateOf(d),
		})
	}
	return out, nil
}

// MachineCpus returns the hypervisor's CPU count (libxl physinfo nr_cpus).
func (x *libxlXen) MachineCpus() (int, error) {
	pi, err := x.GetPhysinfo()
	if err != nil {
		return 0, err
	}
	return int(pi.NrCpus), nil
}

func (x *libxlXen) Running(name string) (bool, error) {
	if _, err := x.NameToDomid(name); err != nil {
		// An absent domain is not running; a broken context will
		// surface in every other call with its own error.
		return false, nil
	}
	return true, nil
}

func stateOf(d xenlight.Dominfo) string {
	return domainState(d.Dying, d.Running, d.Paused, d.Blocked)
}

// Console attaches to a VM's primary console (TTY). The console is
// available for any existing domain (running or blocked), not just
// running ones — xl console works on blocked domains too.
// Uses xenlight PrimaryConsoleGetTty to find the TTY device, then
// does bidirectional I/O between stdin/stdout and the TTY.
func (x *libxlXen) Console(name string) error {
	running, err := x.Running(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if !running {
		return fmt.Errorf("domain %s is not running", name)
	}
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	ttyPath, err := x.PrimaryConsoleGetTty(uint32(d))
	if err != nil {
		return fmt.Errorf("get console tty: %w", err)
	}
	return connectConsole(ttyPath)
}

// consoleEscape is the detach key: Ctrl+] (SO, 0x1D), the telnet
// convention. In raw mode Ctrl+C goes to the VM, so this is the only
// way to return to the local shell.
const consoleEscape = 0x1D

// connectConsole does bidirectional I/O between stdin/stdout and the VM
// console TTY. Puts the local terminal in raw mode so input is passed
// through unmodified (no signal generation, no local echo, no line
// editing) and escape sequences reach the VM console intact. The terminal
// is restored on exit.
// Blocks until the TTY closes (VM stopped), the user presses Ctrl+] to
// detach, or an error occurs.
func connectConsole(ttyPath string) error {
	fd, err := syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open console tty %s: %w", ttyPath, err)
	}
	defer syscall.Close(fd)

	stdinFd := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		oldState, err = term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("make raw terminal: %w", err)
		}
		defer term.Restore(stdinFd, oldState)

		// Match the guest TTY size to the local terminal so full-screen
		// apps and long output (e.g. the pi agent) use the full window.
		if err := syncWinsize(stdinFd, fd); err != nil {
			// Non-fatal: the guest may not support it, or the size
			// query may fail on unusual terminals.
			fmt.Fprintf(os.Stderr, "warning: could not set guest terminal size: %v\n", err)
		}

		// Keep the guest TTY in sync when the local terminal is resized.
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				if err := syncWinsize(stdinFd, fd); err != nil {
					// Ignore resize failures (the console may be closing).
				}
			}
		}()
		defer signal.Stop(winch)
	}

	stdoutFd := int(os.Stdout.Fd())
	errCh := make(chan error, 2)
	// shutdown signals both I/O goroutines to stop. Closing the console
	// fd from the stdin goroutine while the stdout goroutine is blocked
	// in syscall.Read on it is undefined (the read can hang); use a
	// channel instead and let the deferred Close at the top of the
	// function unblock the read.
	var stopOnce sync.Once
	shutdown := make(chan struct{})
	stop := func() { stopOnce.Do(func() { close(shutdown) }) }
	// stdin -> console; Ctrl+] (0x1D) detaches.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := syscall.Read(stdinFd, buf)
			if err != nil || n == 0 {
				stop()
				errCh <- err
				return
			}
			// Filter out the escape byte before forwarding.
			var out []byte
			for _, b := range buf[:n] {
				if b == consoleEscape {
					// Detach: signal the read side to stop, then return
					// nil (clean exit). The deferred syscall.Close(fd)
					// unblocks the stdout goroutine's read.
					stop()
					errCh <- nil
					return
				}
				out = append(out, b)
			}
			if len(out) > 0 {
				if _, err := syscall.Write(fd, out); err != nil {
					stop()
					errCh <- err
					return
				}
			}
		}
	}()
	// console -> stdout
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-shutdown:
				return
			default:
			}
			n, err := syscall.Read(fd, buf)
			if err != nil || n == 0 {
				stop()
				errCh <- err
				return
			}
			if _, err := syscall.Write(stdoutFd, buf[:n]); err != nil {
				stop()
				errCh <- err
				return
			}
		}
	}()
	return <-errCh
}

// parseUUID parses a RFC 4122 string into xenlight.Uuid (no exported
// parser in the generated API; the type is [16]byte).
func parseUUID(s string) (xenlight.Uuid, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil {
		return xenlight.Uuid{}, fmt.Errorf("uuid %q: %v", s, err)
	}
	if len(b) != 16 {
		return xenlight.Uuid{}, fmt.Errorf("uuid %q: wrong length", s)
	}
	var u xenlight.Uuid
	copy(u[:], b)
	return u, nil
}

// guestSizeMarker formats the in-band terminal-size marker the guest's
// console bridge (conmux, pi-in-xen) watches for: DCS 8 ; rows ; cols ST.
// The Xen console ring is a pure byte stream with no winsize channel
// (struct xencons_interface has no size fields, the console daemon never
// forwards the pty winsize, and hvc_xen.c never calls __hvc_resize), so
// TIOCSWINSZ on the dom0 console pty is invisible to the guest. conmux
// applies this marker to the pty pi's tmux session runs on.
func guestSizeMarker(ws *unix.Winsize) []byte {
	return []byte(fmt.Sprintf("\x1bP8;%d;%d\x1b\\", ws.Row, ws.Col))
}

// syncWinsize copies the terminal size (rows, cols) from localFd to
// targetFd. The guest console TTY needs this so that full-screen apps
// and wrapped text match the user's local terminal dimensions. In
// addition to TIOCSWINSZ (which covers the dom0-side console pty), it
// pushes the size in-band to the guest (see guestSizeMarker).
func syncWinsize(localFd, targetFd int) error {
	ws, err := unix.IoctlGetWinsize(localFd, unix.TIOCGWINSZ)
	if err != nil {
		return fmt.Errorf("get local terminal size: %w", err)
	}
	if ws.Row == 0 || ws.Col == 0 {
		// Zero size (detached terminal, pager, etc.): nothing to sync.
		return nil
	}
	if err := unix.IoctlSetWinsize(targetFd, unix.TIOCSWINSZ, ws); err != nil {
		return fmt.Errorf("set guest terminal size: %w", err)
	}
	if _, err := syscall.Write(targetFd, guestSizeMarker(ws)); err != nil {
		return fmt.Errorf("write guest size marker: %w", err)
	}
	return nil
}

// parseMAC parses a colon-separated MAC into xenlight.Mac ([6]byte).
func parseMAC(s string) (xenlight.Mac, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, ":", ""))
	if err != nil {
		return xenlight.Mac{}, fmt.Errorf("mac %q: %v", s, err)
	}
	if len(b) != 6 {
		return xenlight.Mac{}, fmt.Errorf("mac %q: wrong length", s)
	}
	var m xenlight.Mac
	copy(m[:], b)
	return m, nil
}
