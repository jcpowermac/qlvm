package ostree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	testDigest = "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456"
	testSlug   = "ns-os-bolt"
	partUUID   = "1f2a3b4c-5d6e-7f80-91a2-b3c4d5e6f708"
)

// fakeFS is a recording FS fake. Mount-target-based paths are mapped to
// content roots via partRoots so surgery runs without real loop devices;
// host paths (/sys, /proc/self/mountinfo) are served from files literally.
type fakeFS struct {
	loops     []string
	parts     map[string][]string
	partRoots map[string]string
	dirs      map[string][]string
	files     map[string][]byte
	fstype    string

	mountErr error
	copyErr  error
	writeErr error

	targets  map[string]string
	mounts   []mountCall
	umounted []string
	detached []string
	attached []string
	copied   []copyCall
	wrote    []writeCall
	links    []linkRec // symlinks snapshotted at umount (targets are real temp dirs)
}

type linkRec struct{ path, target string }

type mountCall struct {
	dev, target string
	ro          bool
}

type copyCall struct{ src, dst string }

type writeCall struct {
	path string
	data []byte
	mode os.FileMode
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		parts:     map[string][]string{},
		partRoots: map[string]string{},
		dirs:      map[string][]string{},
		files:     map[string][]byte{},
		fstype:    "xfs",
	}
}

// key maps a real (mount-target-based) or literal host path to a content path.
func (f *fakeFS) key(p string) (string, bool) {
	if _, ok := f.dirs[p]; ok {
		return p, true
	}
	if _, ok := f.files[p]; ok {
		return p, true
	}
	for dev, target := range f.targets {
		if rel, ok := strings.CutPrefix(p, target); ok {
			if root, ok := f.partRoots[strings.TrimPrefix(dev, "/dev/")]; ok {
				return root + rel, true
			}
		}
	}
	return "", false
}

func (f *fakeFS) LoopAttach(path string) (string, error) {
	if len(f.loops) == 0 {
		return "", errors.New("fakeFS: no loop configured")
	}
	loop := f.loops[0]
	f.loops = f.loops[1:]
	f.attached = append(f.attached, path)
	return loop, nil
}

func (f *fakeFS) LoopDetach(dev string) error {
	f.detached = append(f.detached, dev)
	return nil
}

func (f *fakeFS) Mount(dev, target string, ro bool) error {
	f.mounts = append(f.mounts, mountCall{dev: dev, target: target, ro: ro})
	if f.targets == nil {
		f.targets = map[string]string{}
	}
	f.targets[dev] = target
	return f.mountErr
}

func (f *fakeFS) Umount(target string) error {
	f.umounted = append(f.umounted, target)
	// The target is a real temp dir: snapshot its symlinks before the
	// product removes it on cleanup.
	_ = filepath.Walk(target, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(p); err == nil {
				f.links = append(f.links, linkRec{path: strings.TrimPrefix(p, target+"/"), target: t})
			}
		}
		return nil
	})
	return nil
}

func (f *fakeFS) Partitions(loop string) []string { return f.parts[loop] }

func (f *fakeFS) ReadDir(p string) ([]string, error) {
	c, ok := f.key(p)
	if !ok {
		return nil, &os.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	}
	return f.dirs[c], nil
}

func (f *fakeFS) ReadFile(p string) ([]byte, error) {
	if p == "/proc/self/mountinfo" {
		var b strings.Builder
		for dev, target := range f.targets {
			fmt.Fprintf(&b, "10 9 0:31 / %s rw - %s %s rw\n", target, f.fstype, dev)
		}
		return []byte(b.String()), nil
	}
	c, ok := f.key(p)
	if !ok {
		return nil, &os.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	data, ok := f.files[c]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return data, nil
}

func (f *fakeFS) WriteFile(p string, data []byte, mode os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if c, ok := f.key(p); ok {
		p = c
	}
	f.wrote = append(f.wrote, writeCall{path: p, data: append([]byte(nil), data...), mode: mode})
	return nil
}

func (f *fakeFS) CopyFile(src, dst string) (int, error) {
	if f.copyErr != nil {
		return 0, f.copyErr
	}
	f.copied = append(f.copied, copyCall{src: src, dst: dst})
	if c, ok := f.key(src); ok {
		if b, ok := f.files[c]; ok {
			return len(b), nil
		}
	}
	return 0, nil
}

func TestNetworkdFile(t *testing.T) {
	got := NetworkdFile("10.100.0.5", "10.100.0.1", "aa:bb:cc:dd:ee:ff", "1.1.1.1")
	want := `[Match]
MACAddress=aa:bb:cc:dd:ee:ff

[Network]
Address=10.100.0.5/24
Gateway=10.100.0.1
DNS=1.1.1.1
Domains=~/
`
	assert.Equal(t, want, got)
}

func TestIdentityLines(t *testing.T) {
	passwd, group, shadow := IdentityLines("user", 1000)
	assert.Equal(t, "user:x:1000:1000::/home/user:/bin/bash", passwd)
	assert.Equal(t, "user:x:1000:", group)
	assert.Equal(t, "user:!:19000:0:99999:7:::", shadow)
}

func TestOstreePathComputation(t *testing.T) {
	t.Run("picks lowest boot dir", func(t *testing.T) {
		f := newFakeFS()
		f.dirs = map[string][]string{
			"/r/ostree":            {"boot.1", "boot.2", "deploy", "repo"},
			"/r/ostree/boot.1":     {"os1"},
			"/r/ostree/boot.1/os1": {"abc123"},
			"/r/ostree/boot.2":     {"os1"},
			"/r/ostree/boot.2/os1": {"def456"},
		}
		path, osid, commit, err := ostreePath("/r/ostree", f)
		require.NoError(t, err)
		assert.Equal(t, "/ostree/boot.1/os1/abc123/0", path)
		assert.Equal(t, "os1", osid)
		assert.Equal(t, "abc123", commit)
	})
	t.Run("version sort not lexicographic", func(t *testing.T) {
		f := newFakeFS()
		f.dirs = map[string][]string{
			"/r/ostree":             {"boot.10", "boot.2", "deploy", "repo"},
			"/r/ostree/boot.2":      {"os1"},
			"/r/ostree/boot.2/os1":  {"abc123"},
			"/r/ostree/boot.10":     {"os1"},
			"/r/ostree/boot.10/os1": {"def456"},
		}
		path, _, _, err := ostreePath("/r/ostree", f)
		require.NoError(t, err)
		assert.Equal(t, "/ostree/boot.2/os1/abc123/0", path)
	})
	t.Run("errors without deployment", func(t *testing.T) {
		f := newFakeFS()
		f.dirs = map[string][]string{"/r/ostree": {"deploy", "repo"}}
		_, _, _, err := ostreePath("/r/ostree", f)
		require.Error(t, err)
	})
}

func TestLessV(t *testing.T) {
	in := []string{"boot.10", "boot.2", "boot.1"}
	sort.SliceStable(in, func(i, j int) bool { return lessV(in[i], in[j]) })
	assert.Equal(t, []string{"boot.1", "boot.2", "boot.10"}, in)
}

// bakeFS fakes a template disk: loop0p1 is /boot, loop0p2 is the ostree root.
func bakeFS(fstype string) *fakeFS {
	f := newFakeFS()
	f.loops = []string{"/dev/loop0"}
	f.parts["/dev/loop0"] = []string{"loop0p1", "loop0p2"}
	f.partRoots = map[string]string{"loop0p1": "b", "loop0p2": "r"}
	f.fstype = fstype
	f.dirs = map[string][]string{
		"b":                          {"ostree"},
		"b/ostree":                   {"os1"},
		"b/ostree/os1":               {"initramfs-6.1.0", "vmlinuz-6.1.0"},
		"r":                          {"etc", "ostree"},
		"r/ostree":                   {"boot.1", "boot.2", "deploy", "repo"},
		"r/ostree/boot.1":            {"os1"},
		"r/ostree/boot.1/os1":        {"abc123"},
		"r/ostree/boot.2":            {"os1"},
		"r/ostree/boot.2/os1":        {"def456"},
		"r/ostree/deploy":            {"os1"},
		"r/ostree/deploy/os1":        {"deploy"},
		"r/ostree/deploy/os1/deploy": {"abc123.0"},
	}
	f.files = map[string][]byte{
		"/sys/class/block/loop0/loop0p2/uuid":            []byte(partUUID + "\n"),
		"b/ostree/os1/vmlinuz-6.1.0":                     []byte("VMLINUX"),
		"b/ostree/os1/initramfs-6.1.0":                   []byte("INITRAMFS"),
		"r/ostree/deploy/os1/deploy/abc123.0/etc/passwd": []byte("root:x:0:0:root:/root:/bin/bash\n"),
	}
	return f
}

func TestBakeTemplateFindsParts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fstype    string
		rootFlags string
	}{
		{name: "xfs", fstype: "xfs"},
		{name: "btrfs", fstype: "btrfs", rootFlags: "subvol=root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := template.DirFor(t.TempDir(), testSlug, testDigest)
			require.NoError(t, os.MkdirAll(dir, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "template.raw"), []byte("raw"), 0o600))
			f := bakeFS(tc.fstype)
			require.NoError(t, BakeTemplate(context.Background(), f, dir))

			tpl, err := template.LoadMeta(dir)
			require.NoError(t, err)
			assert.Equal(t, "6.1.0", tpl.KernelVer)
			assert.Equal(t, "UUID="+partUUID, tpl.RootDev)
			assert.Equal(t, tc.rootFlags, tpl.RootFlags)
			assert.Equal(t, "/ostree/boot.1/os1/abc123/0", tpl.OstreePath)
			assert.Equal(t, testDigest, tpl.Digest)
			assert.Equal(t, dir, tpl.Dir)

			require.Len(t, f.copied, 2)
			assert.True(t, strings.HasSuffix(f.copied[0].src, "/ostree/os1/vmlinuz-6.1.0"))
			assert.Equal(t, filepath.Join(dir, "vmlinuz"), f.copied[0].dst)
			assert.True(t, strings.HasSuffix(f.copied[1].src, "/ostree/os1/initramfs-6.1.0"))
			assert.Equal(t, filepath.Join(dir, "initramfs"), f.copied[1].dst)

			var sawShadow bool
			for _, w := range f.wrote {
				switch {
				case strings.HasSuffix(w.path, "/etc/passwd"):
					assert.Equal(t, "root:x:0:0:root:/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n", string(w.data))
					assert.Equal(t, os.FileMode(0o644), w.mode)
				case strings.HasSuffix(w.path, "/etc/group"):
					assert.Equal(t, "user:x:1000:\n", string(w.data))
					assert.Equal(t, os.FileMode(0o644), w.mode)
				case strings.HasSuffix(w.path, "/etc/shadow"):
					assert.Equal(t, "user:!:19000:0:99999:7:::\n", string(w.data))
					assert.Equal(t, os.FileMode(0o600), w.mode)
					sawShadow = true
				}
			}
			assert.True(t, sawShadow, "shadow entry must be written")

			var sawMask bool
			for _, l := range f.links {
				if l.path == "ostree/deploy/os1/deploy/abc123.0/etc/systemd/system/systemd-resolved.service" {
					assert.Equal(t, "/dev/null", l.target, "resolved mask must point at /dev/null")
					sawMask = true
				}
			}
			assert.True(t, sawMask, "resolved mask symlink must exist in the deployment etc overlay")

			require.Len(t, f.mounts, 2)
			for _, m := range f.mounts {
				assert.True(t, m.ro, "template bake mounts partitions read-only")
			}
			assert.Equal(t, []string{"/dev/loop0"}, f.detached, "loop must be detached")
			assert.Len(t, f.umounted, 2, "every mount must be umounted")
		})
	}
}

func TestBakeTemplateCleanupOnFailure(t *testing.T) {
	dir := template.DirFor(t.TempDir(), testSlug, testDigest)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "template.raw"), []byte("raw"), 0o600))
	f := bakeFS("xfs")
	f.copyErr = errors.New("no space left on device")
	err := BakeTemplate(context.Background(), f, dir)
	require.Error(t, err)
	assert.Equal(t, []string{"/dev/loop0"}, f.detached, "loop must be detached on failure")
	assert.Equal(t, len(f.mounts), len(f.umounted), "every mount must be umounted on failure")
	_, err = template.LoadMeta(dir)
	assert.Error(t, err, "a failed bake must not persist META")
}

func networkdFS() *fakeFS {
	f := newFakeFS()
	f.loops = []string{"/dev/loop3"}
	f.parts["/dev/loop3"] = []string{"loop3p1"}
	f.partRoots = map[string]string{"loop3p1": "r"}
	f.dirs = map[string][]string{
		"r":                   {"ostree"},
		"r/ostree":            {"boot.1", "deploy", "repo"},
		"r/ostree/boot.1":     {"os1"},
		"r/ostree/boot.1/os1": {"abc123"},
	}
	return f
}

func TestBakeNetworkd(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk.img")
	require.NoError(t, os.WriteFile(disk, []byte("raw"), 0o600))
	f := networkdFS()
	require.NoError(t, BakeNetworkd(context.Background(), f, disk,
		"10.100.0.5", "10.100.0.1", "aa:bb:cc:dd:ee:ff", "1.1.1.1"))

	assert.Equal(t, []string{disk}, f.attached, "the VM disk image must be the loop backing file")
	require.Len(t, f.mounts, 1)
	assert.Equal(t, "/dev/loop3p1", f.mounts[0].dev)
	assert.False(t, f.mounts[0].ro, "networkd bake mounts the root rw")
	require.Len(t, f.wrote, 1)
	assert.Equal(t, "r/ostree/deploy/os1/deploy/abc123.0/etc/systemd/network/10-bolt.network", f.wrote[0].path)
	assert.Equal(t, NetworkdFile("10.100.0.5", "10.100.0.1", "aa:bb:cc:dd:ee:ff", "1.1.1.1"), string(f.wrote[0].data))
	assert.Equal(t, os.FileMode(0o644), f.wrote[0].mode)
	assert.Equal(t, []string{"/dev/loop3"}, f.detached)
	assert.Len(t, f.umounted, 1)
}

func TestBakeNetworkdMountFailure(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk.img")
	require.NoError(t, os.WriteFile(disk, []byte("raw"), 0o600))
	f := networkdFS()
	f.mountErr = &os.SyscallError{Syscall: "mount", Err: unix.EBUSY}
	err := BakeNetworkd(context.Background(), f, disk,
		"10.100.0.5", "10.100.0.1", "aa:bb:cc:dd:ee:ff", "1.1.1.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "xfs_repair -L", "mount failure must point at repair")
	assert.Contains(t, err.Error(), "/dev/loop3p1", "mount failure must name the device")
	assert.Equal(t, []string{"/dev/loop3"}, f.detached, "loop must be detached even when the mount fails")
	assert.Empty(t, f.wrote)
}
