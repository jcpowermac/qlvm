package vm

import (
	"strings"
	"testing"

	"github.com/jcpowermac/qlvm/internal/template"
)

func TestDomainConfig_Agent(t *testing.T) {
	m := &Meta{
		Name:     "test-agent",
		Type:     "agent",
		UUID:     "12345678-1234-5678-1234-567812345678",
		IP:       "10.100.0.5",
		MAC:      "aa:bb:cc:dd:ee:ff",
		MemoryMB: 2048,
		VCPUs:    2,
	}
	tpl := &template.Template{
		Type: "agent",
		Dir:  "/var/lib/qlvm/templates/test",
	}
	spec := DomainConfig("/var/lib/qlvm/vms/test-agent", m, tpl)

	if spec.Type != "PVH" {
		t.Errorf("expected PVH, got %s", spec.Type)
	}
	if spec.Kernel != "/var/lib/qlvm/vms/test-agent/vmlinuz" {
		t.Errorf("unexpected kernel path: %s", spec.Kernel)
	}
	if spec.Ramdisk != "/var/lib/qlvm/vms/test-agent/initramfs" {
		t.Errorf("unexpected ramdisk path: %s", spec.Ramdisk)
	}
	// Check boot params
	foundConsole, foundIP := false, false
	for _, e := range spec.Extra {
		if e == "console=hvc0" {
			foundConsole = true
		}
		if e == "ip=10.100.0.5" {
			foundIP = true
		}
		if strings.Contains(e, "ostree=") {
			t.Error("agent VM should not have ostree= param")
		}
		if strings.Contains(e, "root=") {
			t.Error("agent VM should not have root= param")
		}
	}
	if !foundConsole || !foundIP {
		t.Error("missing console or ip boot params")
	}
	if len(spec.Disks) != 0 {
		t.Errorf("agent VM should have no disks, got %d", len(spec.Disks))
	}
}

func TestDomainConfig_AgentWithWorkspace(t *testing.T) {
	m := &Meta{
		Name:            "test-agent",
		Type:            "agent",
		UUID:            "12345678-1234-5678-1234-567812345678",
		IP:              "10.100.0.5",
		MAC:             "aa:bb:cc:dd:ee:ff",
		MemoryMB:        2048,
		VCPUs:           2,
		WorkspaceSizeMB: 4096,
	}
	tpl := &template.Template{
		Type: "agent",
		Dir:  "/var/lib/qlvm/templates/test",
	}
	spec := DomainConfig("/var/lib/qlvm/vms/test-agent", m, tpl)

	if len(spec.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(spec.Disks))
	}
	if spec.Disks[0].PdevPath != "/var/lib/qlvm/vms/test-agent/workspace.img" {
		t.Errorf("unexpected disk path: %s", spec.Disks[0].PdevPath)
	}
	if spec.Disks[0].Vdev != "xvdb" {
		t.Errorf("expected xvdb, got %s", spec.Disks[0].Vdev)
	}
}
