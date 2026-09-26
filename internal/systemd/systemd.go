// Package systemd manages dom0 units over D-Bus. Production code dials the
// session bus directly (the user manager starts the qlvm units); tests
// supply a fake Conn recording the exact method calls.
package systemd

import (
	"context"
	"strings"

	"github.com/godbus/dbus/v5"
)

// systemd D-Bus API constants.
const (
	sdService  = "org.freedesktop.systemd1"
	sdObject   = "/org/freedesktop/systemd1"
	propsIface = "org.freedesktop.DBus.Properties"
)

// Conn is the narrow systemd surface Manager uses. Method names follow the
// systemd D-Bus calls they wrap.
type Conn interface {
	// UnitActive returns the unit's ActiveState (e.g. "active", "inactive").
	UnitActive(unit string) (string, error)
	// UnitFileState returns the unit's UnitFileState (e.g. "enabled",
	// "static", "disabled").
	UnitFileState(unit string) (string, error)
	StartUnit(unit string) error
	EnableUnit(unit string) error
}

// Manager drives units toward started-and-enabled.
type Manager struct {
	conn Conn
}

// New wraps a systemd Conn.
func New(c Conn) *Manager { return &Manager{conn: c} }

// NewSession wires Manager to the live systemd session bus.
func NewSession() (*Manager, error) {
	bus, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}
	return New(&sessionConn{bus: bus}), nil
}

// EnableStart starts the unit if not active and enables it if not enabled.
func (m *Manager) EnableStart(ctx context.Context, unit string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	active, err := m.conn.UnitActive(unit)
	if err != nil {
		return err
	}
	fileState, err := m.conn.UnitFileState(unit)
	if err != nil {
		return err
	}
	if active != "active" {
		if err := m.conn.StartUnit(unit); err != nil {
			return err
		}
	}
	if fileState != "enabled" && fileState != "static" {
		return m.conn.EnableUnit(unit)
	}
	return nil
}

// sessionConn is the godbus-backed Conn for the live session bus.
type sessionConn struct {
	bus *dbus.Conn
}

// unitPath encodes a unit name per systemd: each '.' becomes "_2d".
func unitPath(unit string) dbus.ObjectPath {
	return dbus.ObjectPath(sdObject + "/" + strings.ReplaceAll(unit, ".", "_2d"))
}

func (s *sessionConn) UnitActive(unit string) (string, error) {
	var state string
	err := s.bus.Object(sdService, dbus.ObjectPath(sdObject)).
		Call(sdService+".Manager.GetUnitActiveState", 0, unit).Store(&state)
	return state, err
}

func (s *sessionConn) UnitFileState(unit string) (string, error) {
	var state string
	err := s.bus.Object(propsIface, unitPath(unit)).
		Call(propsIface+".Get", 0, sdService+".Unit", "UnitFileState").Store(&state)
	return state, err
}

func (s *sessionConn) StartUnit(unit string) error {
	var p dbus.ObjectPath
	err := s.bus.Object(sdService, dbus.ObjectPath(sdObject)).
		Call(sdService+".Manager.StartUnit", 0, unit, "replace").Store(&p)
	return err
}

func (s *sessionConn) EnableUnit(unit string) error {
	var links []dbus.ObjectPath
	err := s.bus.Object(sdService, dbus.ObjectPath(sdObject)).
		Call(sdService+".Manager.EnableUnit", 0, []string{unit}, false).Store(&links)
	return err
}
