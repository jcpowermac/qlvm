// Package projection carries a guest app's Wayland GUI to the dom0's
// Wayland session without SSH: a per-run ephemeral TCP data port bridged
// to waypipe's unix protocol sockets, plus a token-authenticated control
// dial to the guest's qvm-ctl relay (baked at create, tcp:4711).
package projection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// ControlPort is the guest-side qvm-ctl relay socket (baked at vm create).
const ControlPort = 4711

// Deps is the injectable surface of Run (tests fake waypipe and the
// control dial).
type Deps struct {
	// Waypipe execs the dom0-side `waypipe client`.
	Waypipe func(args []string, stdin io.Reader, out, errW io.Writer) error
	// DialControl reaches the guest's control relay.
	DialControl func(ctx context.Context, addr string) (net.Conn, error)
	// TempDir hosts the waypipe client socket (default os.TempDir()).
	TempDir string
}

func (d *Deps) fill() {
	if d.Waypipe == nil {
		d.Waypipe = execWaypipe
	}
	if d.DialControl == nil {
		d.DialControl = func(ctx context.Context, addr string) (net.Conn, error) {
			var dl net.Dialer
			dl.Timeout = 10 * time.Second
			return dl.DialContext(ctx, "tcp", addr)
		}
	}
	if d.TempDir == "" {
		d.TempDir = os.TempDir()
	}
}

func execWaypipe(args []string, stdin io.Reader, out, errW io.Writer) error {
	cmd := exec.Command("waypipe", args...) // #nosec G204 -- the single permitted local exec (design spec)
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = errW
	return cmd.Run()
}

// Run projects the guest-side exec onto the dom0 Wayland session:
//  1. listen on an ephemeral TCP port (the guest's waypipe server dials
//     here once the app starts),
//  2. launch `waypipe client` on a unix socket and wait for it to bind,
//  3. forward the accepted TCP connection to that socket,
//  4. send the 4-line control frame (token, data port, dom0 IP, exec) to
//     the guest's control relay,
//  5. wait for the client to exit (app exit), then clean up.
func Run(ctx context.Context, d Deps, m *vm.Meta, dom0IP, exec string, stdin io.Reader, out, errW io.Writer) error {
	d.fill()
	ln, err := net.Listen("tcp4", "0.0.0.0:0") // #nosec G102 -- ephemeral port; the guest dials in from the VM subnet
	if err != nil {
		return fmt.Errorf("listen data port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	sock := filepath.Join(d.TempDir, fmt.Sprintf("qlvm-waypipe-%s-%d.sock", m.Name, os.Getpid()))
	_ = os.Remove(sock)
	var wgDone sync.WaitGroup
	defer func() {
		_ = ln.Close()
		wgDone.Wait()
		_ = os.Remove(sock)
	}()
	wgDone.Add(1)
	go func() {
		defer wgDone.Done()
		forward(ln, sock)
	}()

	done := make(chan error, 1)
	go func() {
		done <- d.Waypipe([]string{"--socket", sock, "--unlink-socket", "client"}, stdin, out, errW)
	}()

	if err := waitForFile(ctx, sock, 10*time.Second); err != nil {
		return fmt.Errorf("waypipe client did not bind %s (waypipe installed on the dom0?): %w", sock, err)
	}

	conn, err := d.DialControl(ctx, net.JoinHostPort(m.IP, strconv.Itoa(ControlPort)))
	if err != nil {
		return fmt.Errorf("control dial %s:%d: %w (VM running? template re-baked with the control channel?)", m.IP, ControlPort, err)
	}
	frame := m.Token + "\n" + strconv.Itoa(port) + "\n" + dom0IP + "\n" + exec + "\n"
	if _, err := conn.Write([]byte(frame)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send control frame: %w", err)
	}
	_ = conn.Close()

	return <-done
}

// forward pipes each accepted TCP connection to the waypipe client's unix
// socket until the listener closes. A one-shot run opens exactly one
// connection; the accept loop keeps a racing retry from erroring the run.
func forward(ln net.Listener, sock string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			u, err := net.Dial("unix", sock)
			if err != nil {
				_ = c.Close()
				return
			}
			defer func() {
				_ = c.Close()
				_ = u.Close()
			}()
			go func() { _, _ = io.Copy(u, c) }()
			_, _ = io.Copy(c, u)
		}(c)
	}
}

// WaitControl is the guest-readiness gate for the projection path (replaces
// the SSH wait): the qvm-ctl socket unit answers once the guest is up.
func WaitControl(ctx context.Context, ip string, interval, timeout time.Duration) error {
	addr := net.JoinHostPort(ip, strconv.Itoa(ControlPort))
	var dl net.Dialer
	dl.Timeout = 3 * time.Second
	deadline := time.Now().Add(timeout)
	var last error
	for {
		var c net.Conn
		c, last = dl.DialContext(ctx, "tcp", addr)
		if last == nil {
			_ = c.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest control port %s did not answer within %s: %w", addr, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func waitForFile(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
