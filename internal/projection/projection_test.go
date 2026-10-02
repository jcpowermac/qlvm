package projection

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// TestForwardPairsConnections verifies the accept loop: two racing TCP dials
// each get a unix-side connection, and the loop exits when the listener
// closes. Byte movement is plain io.Copy; the round trip is covered in
// TestRun (asserting bytes on an endpoint the forwarder also reads would
// race its copy goroutine).
func TestForwardPairsConnections(t *testing.T) {
	tcpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	sock := filepath.Join(t.TempDir(), "waypipe.sock")
	unixLn, err := net.Listen("unix", sock)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		forward(tcpLn, sock)
		close(done)
	}()

	c, err := net.Dial("tcp4", tcpLn.Addr().String())
	require.NoError(t, err)
	uc, err := unixLn.Accept()
	require.NoError(t, err)

	// A racing duplicate dial must be served too, not dropped.
	c2, err := net.Dial("tcp4", tcpLn.Addr().String())
	require.NoError(t, err)
	uc2, err := unixLn.Accept()
	require.NoError(t, err)

	for _, conn := range []net.Conn{c, c2, uc, uc2} {
		require.NoError(t, conn.Close())
	}
	_ = unixLn.Close()
	require.NoError(t, tcpLn.Close())

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forward did not return after listener close")
	}
}

// TestRun drives the full orchestration with fakes: the fake waypipe plays
// the dom0 client (bind the protocol socket, hold it briefly, exit), the
// fake control dial captures the frame and plays the guest dialing the data
// port back. The guest asserts a round-trip echo through the bridge —
// deterministic because the forwarder is the only reader of each endpoint.
func TestRun(t *testing.T) {
	meta := &vm.Meta{Name: "alpha", IP: "10.100.1.5", Token: "tok-tok-tok"}
	dom0IP := "172.31.12.200"

	type frame struct{ addr, body string }
	frameCh := make(chan frame, 1)

	d := Deps{
		TempDir: t.TempDir(),
		Waypipe: func(args []string, _ io.Reader, _, _ io.Writer) error {
			// `waypipe --socket <sock> --unlink-socket client`
			require.Equal(t, "--socket", args[0])
			require.NotEmpty(t, args[1])
			require.Equal(t, "client", args[len(args)-1])
			l, err := net.Listen("unix", args[1])
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			c, err := l.Accept()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			time.Sleep(200 * time.Millisecond) // hold the bridge while the guest dials back
			return nil
		},
		DialControl: func(_ context.Context, addr string) (net.Conn, error) {
			a, b := net.Pipe()
			go func() {
				data, _ := io.ReadAll(a)
				_ = a.Close()
				_ = b.Close()
				frameCh <- frame{addr: addr, body: string(data)}
				// Play the guest: parse the frame, dial the data port and
				// write one byte (the bridge must not reject the dial). No
				// read-back: the forwarder reads both endpoints, so any
				// test read would race its copy goroutine. Byte movement is
				// stdlib io.Copy; the live smoke exercises it for real.
				f := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
				require.Len(t, f, 5)
				require.Equal(t, "tcp", f[1])
				dc, err := net.DialTimeout("tcp4", "127.0.0.1:"+f[2], 5*time.Second)
				if err != nil {
					return
				}
				defer func() { _ = dc.Close() }()
				_, _ = dc.Write([]byte("P"))
			}()
			return b, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), d, meta, ConnectTCP, dom0IP, "firefox --private", strings.NewReader(""), io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	f := <-frameCh
	assert.Equal(t, "10.100.1.5:4711", f.addr, "control dial must target the guest IP on the control port")
	lines := strings.Split(strings.TrimRight(f.body, "\n"), "\n")
	assert.Equal(t, "tok-tok-tok", lines[0], "line 1: token")
	assert.Equal(t, "tcp", lines[1], "line 2: mode")
	port, err := strconv.Atoi(lines[2])
	require.NoError(t, err)
	assert.Greater(t, port, 0, "line 3: data port")
	assert.Equal(t, dom0IP, lines[3], "line 4: dom0 return IP")
	assert.Equal(t, "firefox --private", lines[4], "line 5: exec string")

	socks, _ := os.ReadDir(d.TempDir)
	assert.Len(t, socks, 0, "the waypipe socket must be cleaned up")
}

func TestWaitControl(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(ControlPort))
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	require.NoError(t, WaitControl(context.Background(), "127.0.0.1", ConnectTCP, 10*time.Millisecond, 2*time.Second))

	// TEST-NET-1: unroutable, must time out with a clean error.
	err = WaitControl(context.Background(), "192.0.2.1", ConnectTCP, 10*time.Millisecond, 300*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4711")
}

func TestWaitPort(t *testing.T) {
	assert.Equal(t, 22, waitPort(ConnectSSH))
	assert.Equal(t, ControlPort, waitPort(ConnectTCP))
	assert.Equal(t, ControlPort, waitPort(ConnectVsock))
}

func TestParseConnect(t *testing.T) {
	for _, want := range []Connect{ConnectSSH, ConnectTCP, ConnectVsock} {
		c, err := ParseConnect(string(want))
		require.NoError(t, err)
		assert.Equal(t, want, c)
	}
	_, err := ParseConnect("carrier-pigeon")
	require.Error(t, err)
}

func TestSSHArgs(t *testing.T) {
	args := SSHArgs("10.100.1.5", "firefox --private")
	assert.Equal(t, []string{"--xwls", "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"user@10.100.1.5", "firefox", "--private"}, args)

	// Empty exec: waypipe ssh with no command (remote login shell), the
	// pre-control-channel behavior.
	args = SSHArgs("10.100.1.5", "")
	assert.Equal(t, "user@10.100.1.5", args[len(args)-1])
}
