package vm

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/stretchr/testify/require"
)

// recOVN records OVN port calls into the shared event log.
type recOVN struct {
	events *[]string
}

func (r *recOVN) AddLSPort(_ context.Context, _, name, _, _ string) error {
	*r.events = append(*r.events, "ovn-add:"+name)
	return nil
}

func (r *recOVN) DelLSPort(_ context.Context, name string) error {
	*r.events = append(*r.events, "ovn-del:"+name)
	return nil
}

// recBakeFS is a minimal ostree.FS that satisfies UniqueXFS and
// BakeNetworkd: it presents two fake partitions (loop9p1 /boot, loop9p2 the
// ostree root) and records LoopAttach into the shared event log so tests can
// observe the per-VM XFS UUID step's position in the create ordering.
type recBakeFS struct {
	events *[]string
}

func (f *recBakeFS) PartUUID(_, _ string) (string, error) { return "test-partuuid", nil }
func (f *recBakeFS) LoopAttach(_ string) (string, error) {
	*f.events = append(*f.events, "loopattach")
	return "loop9", nil
}
func (f *recBakeFS) LoopDetach(_ string) error { return nil }
func (f *recBakeFS) Mount(dev, target, _ string, _ bool) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	// probePart walks the real mount target, so lay down the minimal
	// per-partition tree there (boot: an osid with a vmlinuz; root: repo +
	// the deployment tree ostreePath/deploymentDir resolve).
	switch dev {
	case "/dev/loop9p1":
		_ = os.MkdirAll(filepath.Join(target, "ostree", "os1"), 0o750)
		_ = os.WriteFile(filepath.Join(target, "ostree", "os1", "vmlinuz-6.1.0"), []byte("x"), 0o644) // #nosec G306 -- test fixture
	case "/dev/loop9p2":
		_ = os.MkdirAll(filepath.Join(target, "ostree", "repo"), 0o750)
		_ = os.MkdirAll(filepath.Join(target, "ostree", "boot.1", "os1", "abc123"), 0o750)
		_ = os.MkdirAll(filepath.Join(target, "ostree", "deploy", "os1", "deploy", "c0ffee00.0"), 0o750)
	}
	return nil
}
func (f *recBakeFS) Umount(_ string) error { return nil }
func (f *recBakeFS) MkdirAll(_ string, _ os.FileMode) error {
	*f.events = append(*f.events, "mkdirall")
	return nil
}
func (f *recBakeFS) Partitions(_ context.Context, _ string) []string {
	return []string{"loop9p1", "loop9p2"}
}

func (f *recBakeFS) ReadDir(p string) ([]string, error) {
	// The mount targets are real temp dirs (see Mount); the virtual
	// suffix switch is only the fallback for unmapped paths.
	if entries, err := os.ReadDir(p); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names, nil
	}
	switch {
	case strings.HasSuffix(p, "/ostree/boot.loader/fedora"):
		return []string{"c0ffee00"}, nil
	case strings.HasSuffix(p, "/ostree/boot.loader"):
		return []string{"fedora"}, nil
	case strings.HasSuffix(p, "/ostree/deploy/fedora/deploy"):
		return []string{"c0ffee00.0"}, nil
	case strings.HasSuffix(p, "/ostree"):
		return []string{"repo", "boot.loader"}, nil
	default:
		return []string{"ostree"}, nil
	}
}

func (f *recBakeFS) ReadFile(_ string) ([]byte, error) { return nil, os.ErrNotExist }

func (f *recBakeFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	switch {
	case strings.HasSuffix(p, "10-bolt.network"):
		*f.events = append(*f.events, "bake:"+string(data))
	case strings.HasSuffix(p, ".mount"):
		*f.events = append(*f.events, "mountbake:"+path.Base(p)+":"+string(data))
	}
	return nil
}

func (f *recBakeFS) CopyFile(_, _ string) (int, error) { return 0, nil }

func testCfg() *config.Config {
	return &config.Config{
		Network:    config.Network{DNS: []string{"10.100.0.1"}},
		Domains:    []config.Domain{{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"}},
		VM:         config.VMDefaults{User: "user", MemoryMB: 4096, VCPUs: 2},
		Disposable: config.VMDefaults{MemoryMB: 1024, VCPUs: 1},
	}
}

// stubXFSAdmin installs a PATH-prefixed fake xfs_admin that records its args
// (one line per call) to logPath and exits 0, so the real ostree.xfsAdminFn
// seam (a package var the vm tests cannot replace) succeeds in the unit
// environment. xfs_admin is a filesystem utility with no Go binding.
func stubXFSAdmin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xfs_admin.args")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "xfs_admin"), []byte(script), 0o755)) // #nosec G306 -- script must be executable
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func testDeps(t *testing.T, root string, events *[]string, reflinkErr error) (CreateDeps, string) {
	t.Helper()
	return CreateDeps{
		OVN: &recOVN{events: events},
		Tpl: &template.Template{Dir: "/var/lib/qvm/templates/os-abc", Digest: "sha256:abc"},
		FS:  &recBakeFS{events: events},
		Reflink: func(dst, src string) error {
			*events = append(*events, "reflink:"+dst+":"+src)
			return reflinkErr
		},
		Root:   root,
		FSType: "xfs",
	}, stubXFSAdmin(t)
}

func TestCreateHappyPath(t *testing.T) {
	t.Run("disposable: defaults from cfg.Disposable, no ssh entry", func(t *testing.T) {
		root, home := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		var events []string
		sshCalls := 0
		old := sshConfigFn
		sshConfigFn = func(_ string, _ *Meta) error { sshCalls++; return nil }
		t.Cleanup(func() { sshConfigFn = old })

		d, xfsLog := testDeps(t, root, &events, nil)
		m, err := Create(context.Background(), d, testCfg(),
			Spec{Name: "vm1", Domain: "work", Type: "disposable"})
		require.NoError(t, err)
		require.Equal(t, "10.100.1.10", m.IP)
		require.Equal(t, "02:00:00:00:00:0a", m.MAC)
		require.Equal(t, 1024, m.MemoryMB)
		require.Equal(t, 1, m.VCPUs)
		require.NotEmpty(t, m.UUID)
		require.Equal(t, "sha256:abc", m.Digest)

		disk := filepath.Join(root, "vms/vm1/disk.img")
		bake := "bake:" + ostree.NetworkdFile("10.100.1.10", "10.100.1.1", "02:00:00:00:00:0a", "10.100.0.1")
		want := []string{"ovn-add:vm1", "reflink:" + disk + ":/var/lib/qvm/templates/os-abc/template.raw", "loopattach", "loopattach", bake, "loopattach"}
		require.Equal(t, want, events, "order: OVN port -> reflink -> UniqueXFS (loopattach) -> networkd bake (loopattach) -> control bake (loopattach) -> (meta save, file on disk)")
		// The per-VM XFS UUID step must have invoked xfs_admin -U generate on
		// the root partition only — the image fstab pins the /boot XFS UUID,
		// so the boot partition must be left untouched.
		args, err := os.ReadFile(xfsLog) // #nosec G304 -- t.TempDir path
		require.NoError(t, err)
		var xfsLines []string
		for _, l := range strings.Split(strings.TrimSpace(string(args)), "\n") {
			if l != "" {
				xfsLines = append(xfsLines, l)
			}
		}
		require.Equal(t, []string{"-U generate /dev/loop9p2"}, xfsLines,
			"xfs_admin -U generate runs exactly once, on the root partition only")
		require.FileExists(t, filepath.Join(root, "vms/vm1/meta.toml"))
		require.Equal(t, 0, sshCalls, "disposable writes no ssh-config entry")
		require.NoFileExists(t, filepath.Join(home, ".ssh", "config"))
	})

	t.Run("app: defaults from cfg.VM, ssh entry last", func(t *testing.T) {
		root, home := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		var events []string
		sshMeta := &Meta{}
		old := sshConfigFn
		sshConfigFn = func(home string, m *Meta) error {
			sshMeta = m
			events = append(events, "ssh")
			return old(home, m) // the real AddSSHConfig, to hit disk
		}
		t.Cleanup(func() { sshConfigFn = old })

		d, _ := testDeps(t, root, &events, nil)
		m, err := Create(context.Background(), d, testCfg(),
			Spec{Name: "vm2", Domain: "work", Type: "app"})
		require.NoError(t, err)
		require.Equal(t, 4096, m.MemoryMB)
		require.Equal(t, 2, m.VCPUs)

		disk := filepath.Join(root, "vms/vm2/disk.img")
		bake := "bake:" + ostree.NetworkdFile("10.100.1.10", "10.100.1.1", "02:00:00:00:00:0a", "10.100.0.1")
		want := []string{"ovn-add:vm2", "reflink:" + disk + ":/var/lib/qvm/templates/os-abc/template.raw", "loopattach", "loopattach", bake, "loopattach", "ssh"}
		require.Equal(t, want, events, "order: OVN -> reflink -> UniqueXFS (loopattach) -> bake (loopattach) -> control bake (loopattach) -> meta save -> ssh config")
		require.Equal(t, m.Name, sshMeta.Name)
		got, err := os.ReadFile(filepath.Join(home, ".ssh", "config")) // #nosec G304 -- t.TempDir path

		require.NoError(t, err)
		require.Contains(t, string(got), "Host vm2")
	})

	t.Run("host allocation counts existing metas in the domain", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		cfg := testCfg()
		d, _ := testDeps(t, root, new([]string), nil)
		m1, err := Create(context.Background(), d, cfg, Spec{Name: "first", Domain: "work", Type: "app"})
		require.NoError(t, err)
		require.Equal(t, "10.100.1.10", m1.IP)
		m2, err := Create(context.Background(), d, cfg, Spec{Name: "second", Domain: "work", Type: "app"})
		require.NoError(t, err)
		require.Equal(t, "10.100.1.11", m2.IP)
	})

	t.Run("delete-then-create does not reuse a live hostnum", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		d, _ := testDeps(t, root, new([]string), nil)
		for _, name := range []string{"a", "b", "c"} {
			if _, err := Create(context.Background(), d, testCfg(),
				Spec{Name: name, Domain: "work", Type: "disposable"}); err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
		}
		// Delete b (.11); the live VMs are .10 and .12, so max hostnum is 12.
		require.NoError(t, os.RemoveAll(filepath.Join(root, "vms", "b")))
		m, err := Create(context.Background(), d, testCfg(),
			Spec{Name: "d", Domain: "work", Type: "disposable"})
		require.NoError(t, err)
		require.Equal(t, "10.100.1.13", m.IP, "next hostnum is max+1, never a live VM's .12")
	})

	t.Run("mounts bake per-mount units with unique tags", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		var events []string
		d, _ := testDeps(t, root, &events, nil)
		_, err := Create(context.Background(), d, testCfg(), Spec{
			Name: "vm1", Domain: "work", Type: "disposable",
			Mounts: []Mount{{Host: "/srv/data", Guest: "data"}, {Host: "/home/user/projects", Guest: "/var/lib/qvm/projects"}},
		})
		require.NoError(t, err)
		var unit0, unit1 bool
		for _, e := range events {
			switch {
			case strings.HasPrefix(e, "mountbake:data-0.mount:"):
				require.Contains(t, e, "What=vm1-0", "unit 0 must carry the unique 9p tag")
				require.Contains(t, e, "Where=/data", "relative guest path mounts at /<guest>")
				require.Contains(t, e, "Type=9p")
				require.Contains(t, e, "WantedBy=multi-user.target")
				unit0 = true
			case strings.HasPrefix(e, "mountbake:var-lib-qvm-projects-1.mount:"):
				require.Contains(t, e, "What=vm1-1", "unit 1 must carry the unique 9p tag")
				require.Contains(t, e, "Where=/var/lib/qvm/projects", "absolute guest path used verbatim")
				unit1 = true
			}
		}
		require.True(t, unit0, "first mount unit must be baked")
		require.True(t, unit1, "second mount unit must be baked")
		for _, e := range events {
			require.NotContains(t, e, "9pstore", "no fabricated kernel options")
		}
	})

	t.Run("orphan dir without meta does not fail create", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		var events []string
		d, _ := testDeps(t, root, &events, nil)
		// Simulate a create that crashed between MkdirAll and meta save.
		require.NoError(t, os.MkdirAll(filepath.Join(root, "vms", "crashed"), 0o750))
		m, err := Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "work", Type: "app"})
		require.NoError(t, err)
		require.Equal(t, "10.100.1.10", m.IP, "orphan dir must not count as an existing VM")
	})

	t.Run("existing VM is refused", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := t.TempDir()
		var events []string
		d, _ := testDeps(t, root, &events, nil)
		_, err := Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "work", Type: "app"})
		require.NoError(t, err)
		_, err = Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "work", Type: "app"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
	})

	t.Run("unknown domain is refused", func(t *testing.T) {
		root := t.TempDir()
		var events []string
		d, _ := testDeps(t, root, &events, nil)
		_, err := Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "nope", Type: "app"})
		require.Error(t, err)
		require.Empty(t, events, "no OVN side effects before domain lookup")
	})
}

func TestCreateReflinkFailureLeavesNoOrphanPort(t *testing.T) {
	root := t.TempDir()
	var events []string
	// Mirror the real Reflink contract: EXDEV/EOPNOTSUPP surfaces as an error
	// containing "same filesystem".
	d, _ := testDeps(t, root, &events, errors.New("clone: same filesystem required (EXDEV)"))
	_, err := Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "work", Type: "app"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "same filesystem")
	require.Equal(t, []string{"ovn-add:vm1", "reflink:" + filepath.Join(root, "vms/vm1/disk.img") + ":/var/lib/qvm/templates/os-abc/template.raw", "ovn-del:vm1"},
		events, "failed create must delete the OVN port")
	require.NoFileExists(t, filepath.Join(root, "vms/vm1/meta.toml"))
	require.NoFileExists(t, filepath.Join(root, "vms/vm1/disk.img"))
}

func TestCreateXFSAdminFailureLeavesNoOrphan(t *testing.T) {
	root := t.TempDir()
	var events []string
	d, _ := testDeps(t, root, &events, nil)
	// A PATH-prefixed xfs_admin that fails mid-create (testDeps's success
	// stub is shadowed: this dir comes first on PATH).
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "xfs_admin"),
		[]byte("#!/bin/sh\nexit 1\n"), 0o755)) // #nosec G306 -- script must be executable
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Create(context.Background(), d, testCfg(), Spec{Name: "vm1", Domain: "work", Type: "app"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unique xfs uuids")
	require.Equal(t, []string{"ovn-add:vm1", "reflink:" + filepath.Join(root, "vms/vm1/disk.img") + ":/var/lib/qvm/templates/os-abc/template.raw", "loopattach", "ovn-del:vm1"},
		events, "failed create must delete the OVN port")
	require.NoDirExists(t, filepath.Join(root, "vms/vm1"), "failed create must remove the VM dir (no orphan disk.img)")
}

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := &Meta{
		Name:     "vm1",
		Type:     "app",
		Image:    "os",
		Digest:   "sha256:abc",
		Domain:   "work",
		IP:       "10.100.1.10",
		MAC:      "02:00:00:00:00:0a",
		MemoryMB: 2048,
		VCPUs:    2,
		Mounts:   []Mount{{Host: "/srv/data", Guest: "data"}},
		UUID:     "12345678-1234-5678-1234-567812345678",
		Created:  time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, m.Save(dir))
	got, err := LoadMeta(dir)
	require.NoError(t, err)
	require.Equal(t, m, got)
	_, err = LoadMeta(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

var goldenTpl = &template.Template{
	Dir:        "/var/lib/qvm/templates/os-abc",
	Image:      "os",
	Digest:     "sha256:abc",
	KernelVer:  "6.12.0",
	RootDev:    "PARTUUID=1234abcd-0000-0000-0000-000000000001",
	RootFlags:  "",
	OstreePath: "/ostree/boot.loader/fedora/c0ffee00/0",
}

var goldenMeta = &Meta{
	Name:     "vm1",
	Type:     "disposable",
	Image:    "os",
	Digest:   "sha256:abc",
	Domain:   "work",
	IP:       "10.100.1.10",
	MAC:      "02:00:00:00:00:0a",
	MemoryMB: 2048,
	VCPUs:    2,
	Mounts: []Mount{
		{Host: "/srv/data", Guest: "data"},
		{Host: "/home/user/projects", Guest: "projects"},
	},
	UUID:    "12345678-1234-5678-1234-567812345678",
	Created: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
}

func TestDomainConfigGolden(t *testing.T) {
	vmDir := "/var/lib/qvm/vms/vm1"
	got := DomainConfig(vmDir, goldenMeta, goldenTpl)
	require.Equal(t, "PVH", got.Type)
	require.Equal(t, "vm1", got.Name)
	require.Equal(t, goldenMeta.UUID, got.UUID)
	require.Equal(t, "/var/lib/qvm/vms/vm1/vmlinuz", got.Kernel)
	require.Equal(t, "/var/lib/qvm/vms/vm1/initramfs", got.Ramdisk)
	require.Equal(t,
		"root=PARTUUID=1234abcd-0000-0000-0000-000000000001 ostree=/ostree/boot.loader/fedora/c0ffee00/0 "+
			"systemd.default-target=multi-user.target console=hvc0",
		strings.Join(got.Extra, " "), "extra string byte-exact, no 9pstore kernel options")
	require.Equal(t, 2, got.MaxVcpus)
	require.Equal(t, 2048*1024, got.TargetMemkb)
	require.Equal(t, []DomainDisk{{PdevPath: vmDir + "/disk.img", Vdev: "xvda", Format: "raw", Readwrite: 1}}, got.Disks)
	require.Equal(t, []DomainNic{{Mac: "02:00:00:00:00:0a", Script: "vif-ovn", Nictype: "vif"}}, got.Nics)
	require.Equal(t, []DomainP9{
		{Tag: "vm1-0", Guest: "data", Path: "/srv/data", SecurityModel: "none", Type: "xen9pfsd"},
		{Tag: "vm1-1", Guest: "projects", Path: "/home/user/projects", SecurityModel: "none", Type: "xen9pfsd"},
	}, got.P9S, "tags must be unique per mount")

	t.Run("btrfs root flags land between root= and ostree=", func(t *testing.T) {
		tpl := *goldenTpl
		tpl.RootFlags = "subvol=root"
		got := DomainConfig(vmDir, goldenMeta, &tpl)
		require.Equal(t,
			"root=PARTUUID=1234abcd-0000-0000-0000-000000000001 subvol=root ostree=/ostree/boot.loader/fedora/c0ffee00/0 "+
				"systemd.default-target=multi-user.target console=hvc0",
			strings.Join(got.Extra, " "))
	})
}
