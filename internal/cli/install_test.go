package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
)

func TestWriteDom0Params(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOM0_PARAMS_PATH", filepath.Join(dir, "xen-boot-params.conf"))

	cfg := &config.Config{
		Dom0: config.Dom0{
			MemoryMB: 32768,
			VCPUs:    2,
			Pin:      nil, // default true
		},
	}
	if err := writeDom0Params(cfg); err != nil {
		t.Fatalf("writeDom0Params: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "xen-boot-params.conf"))
	if err != nil {
		t.Fatalf("read params: %v", err)
	}
	want := "dom0_mem=32768M dom0_max_vcpus=2 dom0_vcpus_pin=2\n"
	if string(data) != want {
		t.Errorf("params = %q, want %q", string(data), want)
	}
}

func TestWriteDom0ParamsPinDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOM0_PARAMS_PATH", filepath.Join(dir, "xen-boot-params.conf"))

	pin := false
	cfg := &config.Config{
		Dom0: config.Dom0{
			MemoryMB: 16384,
			VCPUs:    4,
			Pin:      &pin,
		},
	}
	if err := writeDom0Params(cfg); err != nil {
		t.Fatalf("writeDom0Params: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "xen-boot-params.conf"))
	if err != nil {
		t.Fatalf("read params: %v", err)
	}
	want := "dom0_mem=16384M dom0_max_vcpus=4\n"
	if string(data) != want {
		t.Errorf("params = %q, want %q", string(data), want)
	}
}
