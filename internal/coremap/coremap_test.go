package coremap

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDetectTopologyKnownModel(t *testing.T) {
	// Mock /proc/cpuinfo with a known model
	t.Setenv("COREMAP_CPUINFO", `processor	: 0
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 1
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 2
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 3
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 4
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 5
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 6
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 7
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 8
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 9
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 10
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 11
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 12
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 13
model name	: Intel(R) Core(TM) Ultra 5 245K
`)
	topo, err := DetectTopology()
	if err != nil {
		t.Fatalf("DetectTopology: %v", err)
	}
	if topo.TotalCores != 14 {
		t.Errorf("TotalCores = %d, want 14", topo.TotalCores)
	}
	wantP := []int{0, 1, 2, 3, 4, 5}
	if !reflect.DeepEqual(topo.PCores, wantP) {
		t.Errorf("PCores = %v, want %v", topo.PCores, wantP)
	}
	wantE := []int{6, 7, 8, 9, 10, 11, 12, 13}
	if !reflect.DeepEqual(topo.ECores, wantE) {
		t.Errorf("ECores = %v, want %v", topo.ECores, wantE)
	}
}

func TestDetectTopologyUnknownModelFallback(t *testing.T) {
	t.Setenv("COREMAP_CPUINFO", `processor	: 0
model name	: Unknown CPU Model

processor	: 1
model name	: Unknown CPU Model

processor	: 2
model name	: Unknown CPU Model

processor	: 3
model name	: Unknown CPU Model
`)
	topo, err := DetectTopology()
	if err != nil {
		t.Fatalf("DetectTopology: %v", err)
	}
	if topo.TotalCores != 4 {
		t.Errorf("TotalCores = %d, want 4", topo.TotalCores)
	}
	// Fallback: first half are P, second half are E
	wantP := []int{0, 1}
	if !reflect.DeepEqual(topo.PCores, wantP) {
		t.Errorf("PCores = %v, want %v", topo.PCores, wantP)
	}
	wantE := []int{2, 3}
	if !reflect.DeepEqual(topo.ECores, wantE) {
		t.Errorf("ECores = %v, want %v", topo.ECores, wantE)
	}
}

// TestDetectTopologyHypervisorCpus: when Xen caps dom0 (dom0_max_vcpus),
// dom0's own /proc/cpuinfo only lists dom0's vCPUs; the machine topology
// must come from the hypervisor's CPU count, not the cpuinfo processor list.
func TestDetectTopologyHypervisorCpus(t *testing.T) {
	t.Setenv("COREMAP_CPUINFO", `processor	: 0
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 1
model name	: Intel(R) Core(TM) Ultra 5 245K
`)
	topo, err := DetectTopologyWith(14)
	if err != nil {
		t.Fatalf("DetectTopologyWith: %v", err)
	}
	if topo.TotalCores != 14 {
		t.Errorf("TotalCores = %d, want 14", topo.TotalCores)
	}
	wantP := []int{0, 1, 2, 3, 4, 5}
	if !reflect.DeepEqual(topo.PCores, wantP) {
		t.Errorf("PCores = %v, want %v", topo.PCores, wantP)
	}
	wantE := []int{6, 7, 8, 9, 10, 11, 12, 13}
	if !reflect.DeepEqual(topo.ECores, wantE) {
		t.Errorf("ECores = %v, want %v", topo.ECores, wantE)
	}
}

// TestDetectTopologyUnknownModelHypervisorCpus: unknown model with a
// hypervisor count takes the half-P fallback against the hypervisor count.
func TestDetectTopologyUnknownModelHypervisorCpus(t *testing.T) {
	t.Setenv("COREMAP_CPUINFO", `processor	: 0
model name	: Unknown CPU Model

processor	: 1
model name	: Unknown CPU Model
`)
	topo, err := DetectTopologyWith(14)
	if err != nil {
		t.Fatalf("DetectTopologyWith: %v", err)
	}
	if topo.TotalCores != 14 {
		t.Errorf("TotalCores = %d, want 14", topo.TotalCores)
	}
	if len(topo.PCores) != 7 || len(topo.ECores) != 7 {
		t.Errorf("P/E split = %d/%d, want 7/7", len(topo.PCores), len(topo.ECores))
	}
}

// TestEnsureUsesHypervisorCpus: a fresh coremap built with a hypervisor
// count tracks the full machine, not just dom0's visible CPUs.
func TestEnsureUsesHypervisorCpus(t *testing.T) {
	t.Setenv("COREMAP_CPUINFO", `processor	: 0
model name	: Intel(R) Core(TM) Ultra 5 245K

processor	: 1
model name	: Intel(R) Core(TM) Ultra 5 245K
`)
	path := filepath.Join(t.TempDir(), "coremap.yaml")
	cm, err := Ensure(path, 2, 14, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if cm.Topology.TotalCores != 14 {
		t.Errorf("TotalCores = %d, want 14", cm.Topology.TotalCores)
	}
	if !reflect.DeepEqual(cm.Dom0Cores, []int{0, 1}) {
		t.Errorf("Dom0Cores = %v, want [0 1]", cm.Dom0Cores)
	}
	// 14 - 2 dom0 = 12 free cores.
	if got := len(cm.FreeCores()); got != 12 {
		t.Errorf("FreeCores = %d, want 12", got)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coremap.yaml")
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores: []int{0, 1},
		Assignments: []Assignment{
			{VM: "work", Cores: []int{6, 7}},
			{VM: "browser", Cores: []int{8, 9}},
		},
	}
	if err := cm.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, cm) {
		t.Errorf("round trip mismatch:\ngot  %+v\nwant %+v", got, cm)
	}
}

func TestAllocateECoresFirst(t *testing.T) {
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores: []int{0, 1},
	}
	cores, err := cm.Allocate(2)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	want := []int{6, 7}
	if !reflect.DeepEqual(cores, want) {
		t.Errorf("Allocate = %v, want %v", cores, want)
	}
	// Assignment should be recorded
	if len(cm.Assignments) != 1 {
		t.Fatalf("Assignments = %d, want 1", len(cm.Assignments))
	}
}

func TestAllocatePFallback(t *testing.T) {
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores: []int{0, 1},
		Assignments: []Assignment{
			{VM: "work", Cores: []int{6, 7, 8, 9, 10, 11, 12, 13}}, // all E cores taken
		},
	}
	cores, err := cm.Allocate(2)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// Should fall back to P cores (excluding dom0)
	want := []int{2, 3}
	if !reflect.DeepEqual(cores, want) {
		t.Errorf("Allocate = %v, want %v", cores, want)
	}
}

func TestAllocateNoCoresAvailable(t *testing.T) {
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores: []int{0, 1},
		Assignments: []Assignment{
			{VM: "work", Cores: []int{2, 3, 4, 5}},           // all P cores taken
			{VM: "browser", Cores: []int{6, 7, 8, 9, 10, 11, 12, 13}}, // all E cores taken
		},
	}
	_, err := cm.Allocate(1)
	if err == nil {
		t.Fatal("Allocate: expected error, got nil")
	}
}

func TestFree(t *testing.T) {
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores:   []int{0, 1},
		Assignments: []Assignment{{VM: "work", Cores: []int{6, 7}}},
	}
	cm.Free("work")
	if len(cm.Assignments) != 0 {
		t.Errorf("Assignments = %d, want 0", len(cm.Assignments))
	}
	// Free cores should now include 6,7
	free := cm.FreeCores()
	if !free[6] || !free[7] {
		t.Errorf("FreeCores missing 6 or 7: %v", free)
	}
}

func TestFreeUnknownVM(t *testing.T) {
	cm := &Coremap{
		Topology: Topology{
			TotalCores: 14,
			PCores:     []int{0, 1, 2, 3, 4, 5},
			ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
		},
		Dom0Cores:   []int{0, 1},
		Assignments: []Assignment{{VM: "work", Cores: []int{6, 7}}},
	}
	// Freeing an unknown VM should be a no-op
	cm.Free("nonexistent")
	if len(cm.Assignments) != 1 {
		t.Errorf("Assignments = %d, want 1", len(cm.Assignments))
	}
}

func TestRebuild(t *testing.T) {
	topo := Topology{
		TotalCores: 14,
		PCores:     []int{0, 1, 2, 3, 4, 5},
		ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
	}
	vmCores := map[string][]int{
		"work":    {6, 7},
		"browser": {8, 9},
	}
	cm := Rebuild(topo, 2, vmCores)
	if len(cm.Dom0Cores) != 2 {
		t.Errorf("Dom0Cores = %d, want 2", len(cm.Dom0Cores))
	}
	if !reflect.DeepEqual(cm.Dom0Cores, []int{0, 1}) {
		t.Errorf("Dom0Cores = %v, want [0 1]", cm.Dom0Cores)
	}
	if len(cm.Assignments) != 2 {
		t.Errorf("Assignments = %d, want 2", len(cm.Assignments))
	}
}

func TestRebuildRemovesStale(t *testing.T) {
	topo := Topology{
		TotalCores: 14,
		PCores:     []int{0, 1, 2, 3, 4, 5},
		ECores:     []int{6, 7, 8, 9, 10, 11, 12, 13},
	}
	// Only "work" exists; "browser" is stale
	vmCores := map[string][]int{
		"work": {6, 7},
	}
	cm := Rebuild(topo, 2, vmCores)
	if len(cm.Assignments) != 1 {
		t.Errorf("Assignments = %d, want 1", len(cm.Assignments))
	}
	if cm.Assignments[0].VM != "work" {
		t.Errorf("Assignment VM = %q, want %q", cm.Assignments[0].VM, "work")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Load: expected IsNotExist, got %v", err)
	}
}
