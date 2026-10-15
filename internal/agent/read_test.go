package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXenWithDomains is a Xen fake that returns configured domains.
type fakeXenWithDomains struct {
	domains []xenctl.DomainInfo
	listErr error
}

func (f *fakeXenWithDomains) CreateDomain(_ *vm.DomainSpec) error { return nil }
func (f *fakeXenWithDomains) Destroy(_ string) error              { return nil }
func (f *fakeXenWithDomains) Shutdown(_ string) error             { return nil }
func (f *fakeXenWithDomains) List() ([]xenctl.DomainInfo, error) {
	return f.domains, f.listErr
}
func (f *fakeXenWithDomains) Running(_ string) (bool, error) { return false, nil }
func (f *fakeXenWithDomains) Console(_ string) error         { return nil }
func (f *fakeXenWithDomains) Close() error                   { return nil }

var _ xenctl.Xen = &fakeXenWithDomains{}

// testRoot creates a temporary qlvm state root with VMs and templates.
func testRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeMeta writes a VM meta.yaml to <root>/vms/<name>/meta.yaml.
func writeMeta(t *testing.T, root string, m *vm.Meta) {
	t.Helper()
	dir := filepath.Join(root, "vms", m.Name)
	require.NoError(t, os.MkdirAll(dir, 0o755)) //nolint:gosec,G301
	require.NoError(t, m.Save(dir))
}

func TestListVMsEmpty(t *testing.T) {
	root := testRoot(t)
	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	vms, err := a.ListVMs()
	require.NoError(t, err)
	assert.Empty(t, vms)
}

func TestListVMsRunning(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "web",
		Type:     "app",
		Domain:   "alpha",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:abc123",
		MemoryMB: 4096,
		VCPUs:    2,
		Mounts:   []vm.Mount{{Host: "/data", Guest: "data"}},
		Created:  created,
	})

	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{
		{Name: "web", ID: 1, MemMB: 4096, VCPUs: 2, State: "running"},
	}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	vms, err := a.ListVMs()
	require.NoError(t, err)
	require.Len(t, vms, 1)

	assert.Equal(t, "web", vms[0].Name)
	assert.Equal(t, "app", vms[0].Type)
	assert.Equal(t, "running", vms[0].State)
	assert.Equal(t, uint64(4096), vms[0].MemoryMB)
	assert.Equal(t, uint8(2), vms[0].VCPUs)
	assert.Equal(t, "alpha", vms[0].Domain)
	assert.Equal(t, "quay.io/fedora/fedora:42", vms[0].Image)
	assert.Equal(t, "sha256:abc123", vms[0].Digest)
	assert.Equal(t, []string{"/data:data"}, vms[0].Mounts)
	assert.Equal(t, int64(1700000000), vms[0].CreatedAt)
}

func TestListVMsStopped(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "db",
		Type:     "disposable",
		Domain:   "alpha",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:def456",
		MemoryMB: 2048,
		VCPUs:    1,
		Created:  created,
	})

	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	vms, err := a.ListVMs()
	require.NoError(t, err)
	require.Len(t, vms, 1)

	assert.Equal(t, "db", vms[0].Name)
	assert.Equal(t, "disposable", vms[0].Type)
	assert.Equal(t, "stopped", vms[0].State)
	assert.Equal(t, uint64(2048), vms[0].MemoryMB)
	assert.Equal(t, uint8(1), vms[0].VCPUs)
}

func TestListVMsMixed(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "web",
		Type:     "app",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:abc123",
		MemoryMB: 4096,
		VCPUs:    2,
		Created:  created,
	})
	writeMeta(t, root, &vm.Meta{
		Name:     "db",
		Type:     "disposable",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:def456",
		MemoryMB: 2048,
		VCPUs:    1,
		Created:  created,
	})

	// web is running, db is stopped.
	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{
		{Name: "web", ID: 1, MemMB: 4096, VCPUs: 2, State: "running"},
	}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	vms, err := a.ListVMs()
	require.NoError(t, err)
	require.Len(t, vms, 2)

	// Sorted by name.
	assert.Equal(t, "db", vms[0].Name)
	assert.Equal(t, "stopped", vms[0].State)
	assert.Equal(t, "web", vms[1].Name)
	assert.Equal(t, "running", vms[1].State)
}

func TestListVMsXenError(t *testing.T) {
	root := testRoot(t)
	x := &fakeXenWithDomains{listErr: assert.AnError}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	_, err := a.ListVMs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list domains")
}

func TestListVMsSkipsBadMeta(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "good",
		Type:     "app",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:abc123",
		MemoryMB: 4096,
		VCPUs:    2,
		Created:  created,
	})

	// Create a VM dir without meta.yaml (crashed create).
	badDir := filepath.Join(root, "vms", "bad")
	require.NoError(t, os.MkdirAll(badDir, 0o755)) //nolint:gosec,G301

	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	vms, err := a.ListVMs()
	require.NoError(t, err)
	require.Len(t, vms, 1)
	assert.Equal(t, "good", vms[0].Name)
}

func TestListTemplatesEmpty(t *testing.T) {
	root := testRoot(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	tpls, err := a.ListTemplates()
	require.NoError(t, err)
	assert.Empty(t, tpls)
}

func TestListTemplates(t *testing.T) {
	root := testRoot(t)
	// DirOfRef for quay.io/fedora/fedora:42 with digest sha256:abc123.
	dirName := template.SlugFromRef("quay.io/fedora/fedora:42") + "-" + template.NormalizeDigest("sha256:abc123")
	tplDir := filepath.Join(root, "templates", dirName)
	require.NoError(t, os.MkdirAll(tplDir, 0o755)) //nolint:gosec,G301

	// Write META file using the template package's struct.
	tmpl := &template.Template{
		Dir:       tplDir,
		Image:     "quay.io/fedora/fedora:42",
		Digest:    "sha256:abc123",
		KernelVer: "6.10.0-1.fc42.x86_64",
		RootDev:   "/dev/sda",
		Type:      "ostree",
	}
	require.NoError(t, tmpl.SaveMeta(tplDir))

	// Write a template.raw file.
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "template.raw"), []byte("fake raw"), 0o644)) //nolint:gosec,G306

	// Create a VM that references this template.
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "web",
		Type:     "app",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:abc123",
		MemoryMB: 4096,
		VCPUs:    2,
		Created:  created,
	})

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	tpls, err := a.ListTemplates()
	require.NoError(t, err)
	require.Len(t, tpls, 1)

	assert.Equal(t, dirName, tpls[0].Dir)
	assert.Equal(t, "quay.io/fedora/fedora:42", tpls[0].Image)
	assert.Equal(t, "sha256:abc123", tpls[0].Digest)
	assert.Equal(t, "6.10.0-1.fc42.x86_64", tpls[0].Kernel)
	assert.Equal(t, uint64(8), tpls[0].SizeBytes) // len("fake raw")
	assert.Equal(t, 1, tpls[0].VMCount)
	assert.True(t, tpls[0].Complete)
}

func TestListTemplatesIncomplete(t *testing.T) {
	root := testRoot(t)
	// Use a valid dir name format.
	dirName := template.SlugFromRef("quay.io/fedora/fedora:42") + "-" + template.NormalizeDigest("sha256:abc123")
	tplDir := filepath.Join(root, "templates", dirName)
	require.NoError(t, os.MkdirAll(tplDir, 0o755)) //nolint:gosec,G301

	// No META file — incomplete.
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "template.raw"), []byte("fake raw"), 0o644)) //nolint:gosec,G306

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	tpls, err := a.ListTemplates()
	require.NoError(t, err)
	require.Len(t, tpls, 1)

	assert.Equal(t, dirName, tpls[0].Dir)
	assert.False(t, tpls[0].Complete)
	assert.Empty(t, tpls[0].Image)
	assert.Equal(t, uint64(8), tpls[0].SizeBytes)
	assert.Equal(t, 0, tpls[0].VMCount)
}

func TestListDomains(t *testing.T) {
	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{
		{Name: "dom0", ID: 0, MemMB: 32768, VCPUs: 2, State: "running"},
		{Name: "web", ID: 1, MemMB: 4096, VCPUs: 2, State: "running"},
	}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	domains, err := a.ListDomains()
	require.NoError(t, err)
	require.Len(t, domains, 2)

	assert.Equal(t, "dom0", domains[0].Name)
	assert.Equal(t, uint32(0), domains[0].ID)
	assert.Equal(t, uint64(32768), domains[0].MemMB)
	assert.Equal(t, uint8(2), domains[0].VCPUs)
	assert.Equal(t, "running", domains[0].State)

	assert.Equal(t, "web", domains[1].Name)
	assert.Equal(t, uint32(1), domains[1].ID)
	assert.Equal(t, "running", domains[1].State)
}

func TestListDomainsXenError(t *testing.T) {
	x := &fakeXenWithDomains{listErr: assert.AnError}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "", "")

	_, err := a.ListDomains()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list domains")
}

func TestGetVM(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "web",
		Type:     "app",
		Domain:   "alpha",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:abc123",
		MemoryMB: 4096,
		VCPUs:    2,
		Mounts:   []vm.Mount{{Host: "/data", Guest: "data"}},
		Created:  created,
	})

	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{
		{Name: "web", ID: 1, MemMB: 4096, VCPUs: 2, State: "running"},
	}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	info, err := a.GetVM("web")
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, "web", info.Name)
	assert.Equal(t, "app", info.Type)
	assert.Equal(t, "running", info.State)
	assert.Equal(t, uint64(4096), info.MemoryMB)
	assert.Equal(t, uint8(2), info.VCPUs)
	assert.Equal(t, "alpha", info.Domain)
	assert.Equal(t, "quay.io/fedora/fedora:42", info.Image)
	assert.Equal(t, []string{"/data:data"}, info.Mounts)
	assert.Equal(t, int64(1700000000), info.CreatedAt)
}

func TestGetVMNotFound(t *testing.T) {
	root := testRoot(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	_, err := a.GetVM("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetVMStopped(t *testing.T) {
	root := testRoot(t)
	created := time.Unix(1700000000, 0)
	writeMeta(t, root, &vm.Meta{
		Name:     "db",
		Type:     "disposable",
		Image:    "quay.io/fedora/fedora:42",
		Digest:   "sha256:def456",
		MemoryMB: 2048,
		VCPUs:    1,
		Created:  created,
	})

	x := &fakeXenWithDomains{domains: []xenctl.DomainInfo{}}
	a := New(x, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	info, err := a.GetVM("db")
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, "db", info.Name)
	assert.Equal(t, "stopped", info.State)
	assert.Equal(t, uint64(2048), info.MemoryMB)
	assert.Equal(t, uint8(1), info.VCPUs)
}

func TestGetTemplate(t *testing.T) {
	root := testRoot(t)
	// DirOfRef for quay.io/fedora/fedora:42 with digest sha256:abc123.
	dirName := template.SlugFromRef("quay.io/fedora/fedora:42") + "-" + template.NormalizeDigest("sha256:abc123")
	tplDir := filepath.Join(root, "templates", dirName)
	require.NoError(t, os.MkdirAll(tplDir, 0o755)) //nolint:gosec,G301

	// Write META file using the template package's struct.
	tmpl := &template.Template{
		Dir:       tplDir,
		Image:     "quay.io/fedora/fedora:42",
		Digest:    "sha256:abc123",
		KernelVer: "6.10.0-1.fc42.x86_64",
		Type:      "ostree",
	}
	require.NoError(t, tmpl.SaveMeta(tplDir))

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	info, err := a.GetTemplate(dirName)
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, dirName, info.Dir)
	assert.Equal(t, "quay.io/fedora/fedora:42", info.Image)
	assert.Equal(t, "sha256:abc123", info.Digest)
	assert.Equal(t, "6.10.0-1.fc42.x86_64", info.Kernel)
	assert.True(t, info.Complete)
}

func TestGetTemplateNotFound(t *testing.T) {
	root := testRoot(t)
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, filepath.Join(root, "coremap.yaml"), root)

	_, err := a.GetTemplate("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetConfig(t *testing.T) {
	cfg := &config.Config{
		Network: config.Network{
			NIC:      "eth0",
			Gateway:  "192.0.2.1",
			RouterIP: "10.100.1.1",
			Dom0IP:   "192.168.1.10",
			DNS:      []string{"1.1.1.1", "8.8.8.8"},
		},
		Domains: []config.Domain{
			{Name: "alpha", Subnet: "10.100.1", Gateway: "10.100.1.1"},
			{Name: "beta", Subnet: "10.100.2", Gateway: "10.100.2.1"},
		},
		VM:         config.VMDefaults{User: "jcallen", MemoryMB: 4096, VCPUs: 2},
		Disposable: config.VMDefaults{MemoryMB: 1024, VCPUs: 1},
		Agent:      config.VMDefaults{MemoryMB: 512, VCPUs: 1},
		Firewall: config.Firewall{
			Egress: config.Egress{
				AllowDNS:      true,
				AllowHTTPS:    true,
				AllowSSHToVMs: true,
				AllowICMP:     false,
				ExtraRules:    []string{"allow 443"},
			},
		},
		Dom0: config.Dom0{MemoryMB: 32768, VCPUs: 2, Pin: boolPtr(true)},
	}

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, cfg, "", "")

	info, err := a.GetConfig()
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, "eth0", info.Network.NIC)
	assert.Equal(t, "192.0.2.1", info.Network.Gateway)
	assert.Equal(t, "10.100.1.1", info.Network.RouterIP)
	assert.Equal(t, "192.168.1.10", info.Network.Dom0IP)
	assert.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, info.Network.DNS)

	require.Len(t, info.Domains, 2)
	assert.Equal(t, "alpha", info.Domains[0].Name)
	assert.Equal(t, "10.100.1", info.Domains[0].Subnet)
	assert.Equal(t, "beta", info.Domains[1].Name)

	assert.Equal(t, "jcallen", info.VM.User)
	assert.Equal(t, 4096, info.VM.MemoryMB)
	assert.Equal(t, 2, info.VM.VCPUs)
	assert.Equal(t, 1024, info.Disposable.MemoryMB)
	assert.Equal(t, 512, info.Agent.MemoryMB)

	assert.True(t, info.Firewall.Egress.AllowDNS)
	assert.True(t, info.Firewall.Egress.AllowHTTPS)
	assert.True(t, info.Firewall.Egress.AllowSSHToVMs)
	assert.False(t, info.Firewall.Egress.AllowICMP)
	assert.Equal(t, []string{"allow 443"}, info.Firewall.Egress.ExtraRules)

	assert.Equal(t, 32768, info.Dom0.MemoryMB)
	assert.Equal(t, 2, info.Dom0.VCPUs)
	require.NotNil(t, info.Dom0.Pin)
	assert.True(t, *info.Dom0.Pin)
}

func TestGetConfigNil(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, nil, "", "")

	_, err := a.GetConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not loaded")
}

func TestGetCoremap(t *testing.T) {
	root := testRoot(t)
	coremapPath := filepath.Join(root, "coremap.yaml")

	// Write a coremap file.
	cm := &coremap.Coremap{
		Topology: coremap.Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores:   []int{0, 1},
		Assignments: []coremap.Assignment{{VM: "web", Cores: []int{6, 7}}},
	}
	require.NoError(t, cm.Save(coremapPath))

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, coremapPath, root)

	info, err := a.GetCoremap()
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, 14, info.TotalCores)
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5}, info.PCores)
	assert.Equal(t, []int{6, 7, 8, 9, 10, 11, 12, 13}, info.ECores)
	assert.Equal(t, []int{0, 1}, info.Dom0Cores)
	require.Len(t, info.Assignments, 1)
	assert.Equal(t, "web", info.Assignments[0].VM)
	assert.Equal(t, []int{6, 7}, info.Assignments[0].Cores)
}

func TestGetCoremapNotFound(t *testing.T) {
	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, "/nonexistent/coremap.yaml", "")

	_, err := a.GetCoremap()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetCoremapInvalid(t *testing.T) {
	root := testRoot(t)
	coremapPath := filepath.Join(root, "coremap.yaml")
	require.NoError(t, os.WriteFile(coremapPath, []byte("not: valid: yaml: ["), 0o644)) //nolint:gosec,G306

	a := New(&fakeXen{}, &fakeOVNPorter{}, &fakeVifPorter{}, &config.Config{}, coremapPath, root)

	_, err := a.GetCoremap()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load coremap")
}

func boolPtr(b bool) *bool { return &b }
