package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/template"
)

type fakeOVN struct {
	addedPorts map[string]bool
	deleted    map[string]bool
}

func (f *fakeOVN) AddLSPort(_ context.Context, _ string, name, mac, ip string) error {
	if f.addedPorts == nil {
		f.addedPorts = make(map[string]bool)
	}
	f.addedPorts[name] = true
	return nil
}

func (f *fakeOVN) DelLSPort(_ context.Context, name string) error {
	if f.deleted == nil {
		f.deleted = make(map[string]bool)
	}
	f.deleted[name] = true
	return nil
}

func TestCreate_AgentVM(t *testing.T) {
	root := t.TempDir()
	tplDir := filepath.Join(root, "templates", "test-template")
	if err := os.MkdirAll(tplDir, 0o755); err != nil { //nolint:gosec,G301
		t.Fatal(err)
	}
	// Create fake kernel/initramfs in template dir
	if err := os.WriteFile(filepath.Join(tplDir, "vmlinuz"), []byte("fake-kernel"), 0o644); err != nil { //nolint:gosec,G306
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, "initramfs"), []byte("fake-initramfs"), 0o644); err != nil { //nolint:gosec,G306
		t.Fatal(err)
	}

	cfg := &config.Config{
		Network: config.Network{
			DNS: []string{"1.1.1.1"},
		},
		Domains: []config.Domain{
			{Name: "work", Subnet: "10.100.0", Gateway: "10.100.0.1"},
		},
		VM: config.VMDefaults{
			MemoryMB: 2048,
			VCPUs:    2,
		},
	}

	d := CreateDeps{
		OVN: &fakeOVN{},
		Tpl: &template.Template{
			Dir:   tplDir,
			Type:  "agent",
			Image: "test-image",
			Digest: "sha256:abcd1234",
		},
		Root: root,
	}

	spec := Spec{
		Name:   "test-agent",
		Domain: "work",
		Type:   "agent",
		Image:  "test-image",
	}

	m, err := Create(context.Background(), d, cfg, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Type != "agent" {
		t.Errorf("expected type agent, got %s", m.Type)
	}

	vmDir := filepath.Join(root, "vms", "test-agent")
	// Verify kernel/initramfs copied
	if _, err := os.Stat(filepath.Join(vmDir, "vmlinuz")); err != nil {
		t.Errorf("vmlinuz not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vmDir, "initramfs")); err != nil {
		t.Errorf("initramfs not copied: %v", err)
	}
	// Verify no disk.img
	if _, err := os.Stat(filepath.Join(vmDir, "disk.img")); err == nil {
		t.Error("disk.img should not exist for agent VM")
	}
	// Verify workspace disk created (default 4GB)
	if _, err := os.Stat(filepath.Join(vmDir, "workspace.img")); err != nil {
		t.Errorf("workspace.img not created: %v", err)
	}
	// Verify meta.yaml saved with workspace size
	meta, err := LoadMeta(vmDir)
	if err != nil {
		t.Errorf("meta.yaml not saved: %v", err)
	}
	if meta.WorkspaceSizeMB != 4096 {
		t.Errorf("expected workspace size 4096, got %d", meta.WorkspaceSizeMB)
	}
}
