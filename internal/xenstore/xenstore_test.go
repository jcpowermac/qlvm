package xenstore

import (
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reply sends one xsd_sockmsg-framed reply: header + payload.
func reply(conn net.Conn, typ, reqID uint32, val string) error {
	payload := []byte(val)
	if typ == msgError {
		// The socket protocol reports errors as NUL-terminated name
		// strings, not numeric errnos.
		payload = []byte("ENOENT\x00")
	}
	hdr := xsdSockmsg{Type: typ, ReqID: reqID, Len: uint32(len(payload))} //nolint:gosec // small test payload
	if _, err := conn.Write(append(hdr.marshal(), payload...)); err != nil {
		return err
	}
	return nil
}

// fakeXs runs a one-connection in-memory xenstored speaking the binary
// wire protocol on a temp unix socket. The handler maps a path to a value
// and returns false for paths that should not exist (errno 2).
func fakeXs(t *testing.T, handle func(path string) (string, bool)) *Client {
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
		r := conn
		for {
			hb := make([]byte, 16)
			if _, err := io.ReadFull(r, hb); err != nil {
				return
			}
			var hdr xsdSockmsg
			hdr.unmarshal(hb)
			body := make([]byte, hdr.Len)
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			path := string(body[:len(body)-1])
			val, ok := handle(path)
			if !ok {
				if err := reply(conn, msgError, hdr.ReqID, ""); err != nil {
					return
				}
				continue
			}
			payload := val
			if payload == "" {
				payload = "\x00"
			}
			if err := reply(conn, msgRead, hdr.ReqID, payload); err != nil {
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
	c := fakeXs(t, func(path string) (string, bool) {
		if path == "/local/domain/1/vm/name" {
			return "myvm", true
		}
		return "", false
	})

	v, err := c.Read("/local/domain/1/vm/name")
	require.NoError(t, err)
	assert.Equal(t, "myvm", v)

	_, err = c.Read("/local/domain/1/vm/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enoent")
}

// TestXenstoreReadValueWithSpaces pins the binary payload contract:
// values may contain spaces and are delivered as-is, minus the trailing
// NUL.
func TestXenstoreReadValueWithSpaces(t *testing.T) {
	c := fakeXs(t, func(string) (string, bool) { return "aa bb cc", true })
	v, err := c.Read("/some/node")
	require.NoError(t, err)
	assert.Equal(t, "aa bb cc", v)
}

// TestXenstoreReadEmptyValue covers the empty-value node (payload is a
// single NUL byte).
func TestXenstoreReadEmptyValue(t *testing.T) {
	c := fakeXs(t, func(string) (string, bool) { return "", true })
	v, err := c.Read("/some/empty")
	require.NoError(t, err)
	assert.Equal(t, "", v)
}

func TestSocketPathEnvOverride(t *testing.T) {
	if got := SocketPath(); got != DefaultSocket {
		t.Fatalf("no env: got %q, want %q", got, DefaultSocket)
	}
	t.Setenv("XENSTORE_SOCKET_PATH", "/run/xenstored/socket")
	if got := SocketPath(); got != "/run/xenstored/socket" {
		t.Fatalf("env set: got %q", got)
	}
}
