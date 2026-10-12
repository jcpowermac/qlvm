//go:build libxl

// This file is the real xenlight-backed Xen implementation. It compiles
// only with -tags libxl (libxl-devel required); on this host it is written
// but NOT compile-verified — the default build uses xenctl_stub.go.
package xenctl

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"syscall"

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


// Console attaches to a running VM's primary console (TTY).
// Uses xenlight PrimaryConsoleGetTty to find the TTY device, then
// does bidirectional I/O between stdin/stdout and the TTY.
func (x *libxlXen) Console(name string) error {
	d, err := x.NameToDomid(name)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	di, err := x.DomainInfo(d)
	if err != nil {
		return fmt.Errorf("domain %s: %w", name, err)
	}
	if !di.Running {
		return fmt.Errorf("domain %s is not running", name)
	}
	ttyPath, err := x.PrimaryConsoleGetTty(uint32(d))
	if err != nil {
		return fmt.Errorf("get console tty: %w", err)
	}
	return connectConsole(ttyPath)
}

// connectConsole does bidirectional I/O between stdin/stdout and the VM
// console TTY. Blocks until the TTY closes (VM stopped) or an error occurs.
func connectConsole(ttyPath string) error {
	fd, err := syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open console tty %s: %w", ttyPath, err)
	}
	defer syscall.Close(fd)
	errCh := make(chan error, 2)
	// stdin -> console
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				errCh <- err
				return
			}
			if _, err := syscall.Write(fd, buf[:n]); err != nil {
				errCh <- err
				return
			}
		}
	}()
	// console -> stdout
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := syscall.Read(fd, buf)
			if err != nil || n == 0 {
				errCh <- err
				return
			}
			if _, err := os.Stdout.Write(buf[:n]); err != nil {
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
