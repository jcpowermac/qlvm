package xenstore

import (
	"bufio"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXs runs a one-connection in-memory xenstored on a temp unix socket.
// The handler maps a request line ("R <path>") to a reply line
// ("r <value>" / "e <code>").
func fakeXs(t *testing.T, handle func(line string) string) *Client {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "xs.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			if _, werr := conn.Write([]byte(handle(sc.Text()) + "\n")); werr != nil {
				return
			}
		}
	}()

	c, err := Dial(sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestXenstoreRead(t *testing.T) {
	c := fakeXs(t, func(line string) string {
		if line == "R /local/domain/1/vm/name" {
			return "r myvm"
		}
		return "e 2"
	})

	v, err := c.Read("/local/domain/1/vm/name")
	require.NoError(t, err)
	assert.Equal(t, "myvm", v)

	_, err = c.Read("/local/domain/1/vm/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enoent")
}

// TestXenstoreReadValueWithSpaces pins the "rest of line is the value"
// contract: xenstored values may contain spaces.
func TestXenstoreReadValueWithSpaces(t *testing.T) {
	c := fakeXs(t, func(string) string {
		return "r aa bb cc"
	})
	v, err := c.Read("/some/node")
	require.NoError(t, err)
	assert.Equal(t, "aa bb cc", v)
}
