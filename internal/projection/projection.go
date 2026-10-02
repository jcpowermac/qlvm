// Package projection carries a guest app's Wayland GUI to the dom0's
// Wayland session over one of three channels (--connect):
//
//   - ssh (default): `waypipe ssh` — the app's GUI rides a socket
//     forwarded over an ordinary SSH session; no guest-side setup beyond
//     sshd (which every template ships).
//   - tcp: a per-run ephemeral TCP data port bridged to waypipe's unix
//     protocol socket, plus a token-authenticated control dial to the
//     guest's qvm-ctl relay (baked at create, tcp:4711).
//   - vsock: waypipe --vsock (AF_VSOCK) — the relay runs a waypipe server
//     that dials the dom0's vsock port. Needs the xen-vsock transport in
//     both dom0 and guest kernels (stock Fedora 44 has none).
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
	"strings"
	"sync"
	"time"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// ControlPort is the guest-side qvm-ctl relay socket (baked at vm create).
const ControlPort = 4711

// Connect selects the channel Run carries the projection over.
type Connect string

const (
	ConnectSSH   Connect = "ssh"
	ConnectTCP   Connect = "tcp"
	ConnectVsock Connect = "vsock"
)

// ParseConnect validates a --connect flag value.
func ParseConnect(s string) (Connect, error) {
	switch Connect(s) {
	case ConnectSSH, ConnectTCP, ConnectVsock:
		return Connect(s), nil
	default:
		return "", fmt.Errorf("unknown connect mode %q (want ssh|tcp|vsock)", s)
	}
}

// waitPort is the readiness-gate port per channel: ssh waits for sshd,
// the relay channels wait for the qvm-ctl relay.
func waitPort(c Connect) int {
	if c == ConnectSSH {
		return 22
	}
	return ControlPort
}

// SSHArgs is the waypipe ssh invocation (the default channel). Waypipe
// options go BEFORE the mode: its ssh parser treats the first bare word
// after `ssh` as the destination. Known-hosts bypass: VM generations
// reuse IPs with fresh host keys. The -o keepalives: the dom0→VM path is
// multi-hop (br-ex uplink → OVN gateway → netfront) and idle TCP flows
// blackhole there, so fail in ~45s if the peer died instead of hanging
// until ssh's default ~15min give-up.
func SSHArgs(ip, exec string) []string {
	args := []string{"--xwls", "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		vm.SSHUser + "@" + ip}
	if exec != "" {
		// One joined string in, fields out: waypipe ssh forwards each
		// field as a separate remote argv word (the relay's
		// `server -- $exec` splits on whitespace the same way).
		args = append(args, strings.Fields(exec)...)
	}
	return args
}

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

// Run projects the guest-side exec onto the dom0 Wayland session over the
// chosen channel:
//
//	ssh:   exec `waypipe ssh` and wait (waypipe runs both ends over the
//	       forwarded SSH session) — no token, no listener, no relay.
//	tcp:   listen on an ephemeral TCP port (the guest's waypipe server dials
//	       here once the app starts), launch `waypipe client` on a unix
//	       socket and wait for it to bind, forward the accepted TCP
//	       connection to that socket, then send the 5-line control frame
//	       (token, mode, data port, dom0 IP, exec) to the guest's qvm-ctl
//	       relay and wait for the client to exit (app exit), then clean up.
//	vsock: launch `waypipe --vsock -s <port> client` (the guest relay runs
//	       a waypipe server that dials this port back) and send the
//	       control frame with mode=vsock.
func Run(ctx context.Context, d Deps, m *vm.Meta, c Connect, dom0IP, exec string, stdin io.Reader, out, errW io.Writer) error {
	d.fill()
	switch c {
	case ConnectSSH:
		return d.Waypipe(SSHArgs(m.IP, exec), stdin, out, errW)
	case ConnectVsock:
		return runVsock(ctx, d, m, exec, stdin, out, errW)
	default: // ConnectTCP
	}

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
	frame := m.Token + "\ntcp\n" + strconv.Itoa(port) + "\n" + dom0IP + "\n" + exec + "\n"
	if _, err := conn.Write([]byte(frame)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send control frame: %w", err)
	}
	_ = conn.Close()

	return <-done
}

// runVsock projects over AF_VSOCK: the dom0 waypipe client listens on a
// vsock port; the guest relay runs `waypipe --vsock -s <port> server`,
// which dials the dom0 (guest→host needs no CID). The vsock port is
// pid-derived. ponytail: fixed 1s settle for the client to bind its vsock
// port before the frame is sent — if it ever races, poll the port.
func runVsock(ctx context.Context, d Deps, m *vm.Meta, exec string, stdin io.Reader, out, errW io.Writer) error {
	if _, err := os.Stat("/dev/vsock"); err != nil {
		return errors.New("vsock unavailable on the dom0 (no /dev/vsock): use --connect ssh or tcp")
	}
	port := 49152 + os.Getpid()%32768
	done := make(chan error, 1)
	go func() {
		done <- d.Waypipe([]string{"--vsock", "-s", strconv.Itoa(port), "client"}, stdin, out, errW)
	}()
	time.Sleep(time.Second)

	conn, err := d.DialControl(ctx, net.JoinHostPort(m.IP, strconv.Itoa(ControlPort)))
	if err != nil {
		return fmt.Errorf("control dial %s:%d: %w (VM running? template re-baked with the control channel?)", m.IP, ControlPort, err)
	}
	frame := m.Token + "\nvsock\n" + strconv.Itoa(port) + "\n\n" + exec + "\n"
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

// WaitControl is the guest-readiness gate: ssh waits for sshd (port 22),
// the relay channels wait for the qvm-ctl relay (port 4711).
func WaitControl(ctx context.Context, ip string, c Connect, interval, timeout time.Duration) error {
	addr := net.JoinHostPort(ip, strconv.Itoa(waitPort(c)))
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
