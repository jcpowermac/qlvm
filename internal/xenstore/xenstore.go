// Package xenstore is a minimal client for xenstored's binary socket
// protocol on the dom0. The vif hotplug script only ever reads nodes, so
// only READ is implemented; the rest of the protocol is out of scope.
package xenstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

)

// DefaultSocket is where xenstored listens on the dom0 when the
// XENSTORE_SOCKET_PATH env var (set by libxl for vif scripts) is absent.
// Fedora hosts use /run/xenstored/socket, not the classic /var/run/xenstore.
// The path may be an abstract unix address (leading NUL) when libxl set it.
const DefaultSocket = "/run/xenstored/socket"

// SocketPath resolves the xenstore socket: XENSTORE_SOCKET_PATH first
// (libxl sets it in the vif script environment), then DefaultSocket.
func SocketPath() string {
	if p := os.Getenv("XENSTORE_SOCKET_PATH"); p != "" {
		return p
	}
	return DefaultSocket
}

// Message types from io/xs_wire.h / docs/misc/xenstore.txt.
const (
	msgRead  = 2
	msgError = 16
)

// xsdSockmsg is the 16-byte socket header from io/xs_wire.h:
// type, req_id, tx_id, len. Fields are native-endian uint32s; the client
// and xenstored share the dom0 CPU, so little-endian (x86) is correct.
type xsdSockmsg struct {
	Type  uint32
	ReqID uint32
	TxID  uint32
	Len   uint32
}

func (h xsdSockmsg) marshal() []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:4], h.Type)
	binary.LittleEndian.PutUint32(b[4:8], h.ReqID)
	binary.LittleEndian.PutUint32(b[8:12], h.TxID)
	binary.LittleEndian.PutUint32(b[12:16], h.Len)
	return b
}

func (h *xsdSockmsg) unmarshal(b []byte) {
	h.Type = binary.LittleEndian.Uint32(b[0:4])
	h.ReqID = binary.LittleEndian.Uint32(b[4:8])
	h.TxID = binary.LittleEndian.Uint32(b[8:12])
	h.Len = binary.LittleEndian.Uint32(b[12:16])
}

// Client is a single connection to xenstored.
type Client struct {
	conn  net.Conn
	r     *bufio.Reader
	reqID uint32
}

// Dial opens a connection to the xenstored socket at path (a filesystem
// path or, with a leading NUL byte, an abstract namespace name).
func Dial(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("xenstore: dial %s: %w", path, err)
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

// Read returns the value stored at the xenstore node path.
//
// Request: xsdSockmsg{type:READ, req_id, tx_id:0, len:len(path)+1}
// followed by the path and a NUL byte. Reply: the same header shape with
// type READ and a nul-terminated value payload, or type ERROR with a
// 4-byte little-endian errno (e.g. 2 for a missing node).
func (c *Client) Read(path string) (string, error) {
	c.reqID++
	payload := append([]byte(path), 0)
	if len(payload) > 1<<32-1 {
		return "", fmt.Errorf("xenstore: %s: path too long", path)
	}
	req := append(xsdSockmsg{Type: msgRead, ReqID: c.reqID, Len: uint32(len(payload))}.marshal(), payload...) //nolint:gosec // bounded by the length guard above
	if _, err := c.conn.Write(req); err != nil {
		return "", fmt.Errorf("xenstore: %s: %w", path, err)
	}
	hdrb := make([]byte, 16)
	if err := c.readFull(hdrb); err != nil {
		return "", fmt.Errorf("xenstore: %s: %w", path, err)
	}
	var hdr xsdSockmsg
	hdr.unmarshal(hdrb)
	body := make([]byte, hdr.Len)
	if err := c.readFull(body); err != nil {
		return "", fmt.Errorf("xenstore: %s: %w", path, err)
	}
	switch hdr.Type {
	case msgRead:
		// xenstored sends non-empty values without a trailing NUL and
		// empty values as a single NUL byte; strip it only if present.
		if len(body) > 0 && body[len(body)-1] == 0 {
			body = body[:len(body)-1]
		}
		return string(body), nil
	case msgError:
		// The socket protocol reports errors as a NUL-terminated name
		// string (e.g. "ENOENT"), not a numeric errno.
		name := string(body[:len(body)-1])
		return "", fmt.Errorf("xenstore: %s: %s", path, strings.ToLower(name))
	default:
		return "", fmt.Errorf("xenstore: %s: unexpected reply type %d", path, hdr.Type)
	}
}

func (c *Client) readFull(b []byte) error {
	if _, err := io.ReadFull(c.r, b); err != nil {
		return err
	}
	return nil
}

// Close terminates the connection.
func (c *Client) Close() error { return c.conn.Close() }
