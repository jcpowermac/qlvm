package ostree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// FS is the narrow file/loop/mount surface used by the surgery. The default
// implementation (NewFS) is x/sys/unix loop ioctls + mount(2) + sysfs; tests
// substitute a recording fake. ReadFile/WriteFile were added beyond the
// original brief sketch (supervisor ruling 2026-09-26): the surgery needs
// sysfs partition UUIDs and must write into the deployment /etc overlay.
// Mount takes an fstype (mount(2) EINVALs on an empty fstype; the wiring
// layer knows the image's fs from image-builder, no superblock probing).
type FS interface {
	LoopAttach(path string) (loop string, err error)
	LoopDetach(dev string) error
	Mount(dev, target, fstype string, ro bool) error
	Umount(target string) error
	Partitions(ctx context.Context, loop string) []string
	ReadDir(p string) ([]string, error)
	ReadFile(p string) ([]byte, error)
	WriteFile(p string, data []byte, mode os.FileMode) error
	CopyFile(src, dst string) (n int, err error)
}

// Mounter is the mount(2) surface of sys, injectable so the rw ("dirty log")
// mount path stays unit-testable.
type Mounter func(dev, target, fstype string, ro bool) error

// NewFS returns the real FS. A nil mounter uses unix.Mount.
func NewFS(mount Mounter) FS {
	if mount == nil {
		mount = unixMount
	}
	return &sys{mount: mount}
}

func unixMount(dev, target, fstype string, ro bool) error {
	flags := uintptr(0)
	if ro {
		flags |= unix.MS_RDONLY
	}
	return unix.Mount(dev, target, fstype, flags, "")
}

type sys struct {
	mount Mounter
}

const loopControl = "/dev/loop-control"

// LoopAttach binds path to a free loop device: LOOP_CTL_GET_FREE on
// /dev/loop-control, then LOOP_SET_STATUS64 with the backing file inode,
// zero offset/sizelimit, and autoclear (+ partscan so GPT partition nodes
// are created).
func (s *sys) LoopAttach(path string) (string, error) {
	ctl, err := os.OpenFile(loopControl, os.O_RDWR, 0) // #nosec G304 -- fixed kernel device
	if err != nil {
		return "", err
	}
	defer func() { _ = ctl.Close() }()
	idx, err := unix.IoctlRetInt(int(ctl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", fmt.Errorf("LOOP_CTL_GET_FREE: %w", err)
	}
	backing, err := os.OpenFile(path, os.O_RDONLY, 0) // #nosec G304 -- caller-owned template/disk path
	if err != nil {
		return "", err
	}
	defer func() { _ = backing.Close() }()
	st, err := backing.Stat()
	if err != nil {
		return "", err
	}
	dev := fmt.Sprintf("/dev/loop%d", idx)
	lo, err := os.OpenFile(dev, os.O_RDWR, 0) // #nosec G304 -- kernel-generated loop path
	if err != nil {
		return "", err
	}
	defer func() { _ = lo.Close() }()
	stSys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("%s: unexpected stat type", path)
	}
	info := unix.LoopInfo64{
		Inode: stSys.Ino,
		Flags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN,
	}
	copy(info.File_name[:], path)
	if err := unix.IoctlLoopSetStatus64(int(lo.Fd()), &info); err != nil {
		return "", fmt.Errorf("LOOP_SET_STATUS64 %s: %w", dev, err)
	}
	return dev, nil
}

// LoopDetach clears the backing file (LOOP_CLR_FD); autoclear also detaches
// when the fd count drops to zero.
func (s *sys) LoopDetach(dev string) error {
	lo, err := os.OpenFile(dev, os.O_RDWR, 0) // #nosec G304 -- kernel-generated loop path
	if err != nil {
		return err
	}
	defer func() { _ = lo.Close() }()
	if _, err := unix.IoctlRetInt(int(lo.Fd()), unix.LOOP_CLR_FD); err != nil {
		return fmt.Errorf("LOOP_CLR_FD %s: %w", dev, err)
	}
	return nil
}

func (s *sys) Mount(dev, target, fstype string, ro bool) error {
	return s.mount(dev, target, fstype, ro)
}

func (s *sys) Umount(target string) error { return unix.Unmount(target, 0) }

// Partitions lists the partition nodes under /sys/class/block/<loop>,
// polling (<=5s) as the udev-settle equivalent: the kernel creates GPT
// partition nodes asynchronously after loop setup.
//
// ponytail: 50ms sysfs poll; fine at template-bake cadence, no udev socket
// watch unless bakes become interactive.
func (s *sys) Partitions(ctx context.Context, loop string) []string {
	name := strings.TrimPrefix(loop, "/dev/")
	base := filepath.Join("/sys", "class", "block", name)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil // caller cancelled: stop polling, let the error propagate
		}
		entries, err := os.ReadDir(base) // #nosec G304 -- kernel-generated sysfs path
		if err == nil {
			var names []string
			for _, e := range entries {
				n := e.Name()
				if strings.HasPrefix(n, name+"p") && isDigits(n[len(name)+1:]) {
					names = append(names, n)
				}
			}
			if len(names) > 0 {
				return names
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func (s *sys) ReadDir(p string) ([]string, error) {
	entries, err := os.ReadDir(p) // #nosec G304 -- mount targets and sysfs paths
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func (s *sys) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(p) // #nosec G304 -- sysfs/proc and mount targets
}

func (s *sys) WriteFile(p string, data []byte, mode os.FileMode) error {
	return os.WriteFile(p, data, mode) // #nosec G304 -- mount targets
}

func (s *sys) CopyFile(src, dst string) (int, error) {
	in, err := os.Open(src) // #nosec G304 -- mount targets
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm()) // #nosec G304
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()
	n, err := io.Copy(out, in)
	return int(n), err
}
