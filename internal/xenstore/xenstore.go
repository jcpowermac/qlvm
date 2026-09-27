// Package xenstore is a minimal client for xenstored's text protocol on
// the dom0 unix socket. The vif hotplug script only ever reads nodes, so
// only R (read) is implemented; the rest of the protocol is out of scope.
package xenstore

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// DefaultSocket is where xenstored listens on the dom0.
const DefaultSocket = "/var/run/xenstore/socket"

// Client is a single connection to xenstored.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial opens a connection to the xenstored socket at path.
func Dial(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("xenstore: dial %s: %w", path, err)
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

// Read returns the value stored at the xenstore node path.
//
// Protocol: the request is "R <path>\n"; the reply is "r <value>\n"
// (the value is the rest of the line and may contain spaces) or
// "e <errcode>\n" (a xenstored errno, e.g. 2 for a missing node).
func (c *Client) Read(path string) (string, error) {
	if _, err := c.conn.Write([]byte("R " + path + "\n")); err != nil {
		return "", err
	}
	resp, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	resp = strings.TrimSuffix(resp, "\n")
	switch {
	case strings.HasPrefix(resp, "r "):
		return strings.TrimPrefix(resp, "r "), nil
	case strings.HasPrefix(resp, "e "):
		code, err := strconv.Atoi(strings.TrimPrefix(resp, "e "))
		if err != nil {
			return "", fmt.Errorf("xenstore: bad error reply %q", resp)
		}
		return "", fmt.Errorf("xenstore: %s: %s", path, errnoName(code))
	default:
		return "", fmt.Errorf("xenstore: unexpected reply %q", resp)
	}
}

// Close terminates the connection.
func (c *Client) Close() error { return c.conn.Close() }

// errnoName maps a xenstored errno to its lowercase name (e.g. 2 →
// "enoent"), the spelling the Xen tooling uses.
func errnoName(code int) string {
	if code <= 0 || code > 0x7fff {
		return "errno " + strconv.Itoa(code)
	}
	return strings.ToLower(unix.ErrnoName(unix.Errno(code)))
}
