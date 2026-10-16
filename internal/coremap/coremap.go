// Package coremap tracks CPU topology and per-VM core assignments for
// hybrid (P/E core) CPUs. It provides auto-placement (E-first) and manual
// override for guest VM CPU pinning.
package coremap

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// cpuinfoPath is the path to /proc/cpuinfo; var so tests can override it
// via the COREMAP_CPUINFO environment variable.
var cpuinfoPath = "/proc/cpuinfo"

// knownModels maps CPU model strings to their P/E core counts.
// Linux enumerates P cores first (cpu0, cpu1, ...) then E cores, so
// the P count determines the split.
var knownModels = map[string]struct{ p, e int }{
	"Intel(R) Core(TM) Ultra 5 245K":        {p: 6, e: 8},
	"Intel(R) Core(TM) Ultra 7 265K":        {p: 8, e: 12},
	"Intel(R) Core(TM) Ultra 9 285K":        {p: 14, e: 8},
	"Intel(R) Core(TM) i9-13900K":           {p: 8, e: 16},
	"Intel(R) Core(TM) i7-13700K":           {p: 8, e: 12},
	"Intel(R) Core(TM) i5-13600K":           {p: 6, e: 8},
	"Intel(R) Core(TM) i9-12900K":           {p: 8, e: 8},
	"Intel(R) Core(TM) i7-12700K":           {p: 8, e: 4},
}

// Topology describes the CPU layout: total cores and the P/E split.
type Topology struct {
	TotalCores int   `yaml:"total_cores"`
	PCores     []int `yaml:"p_cores"`
	ECores     []int `yaml:"e_cores"`
}

// Assignment is one VM's core allocation.
type Assignment struct {
	VM    string `yaml:"vm"`
	Cores []int  `yaml:"cores"`
}

// Coremap is the persisted state of CPU assignments.
type Coremap struct {
	Topology    Topology     `yaml:"topology"`
	Dom0Cores   []int        `yaml:"dom0_cores"`
	Assignments []Assignment `yaml:"assignments"`
}

// DetectTopology reads /proc/cpuinfo to determine the P/E core split.
// It uses the CPU model to look up the known P count; if unknown, it
// falls back to assuming the first half of cores are P.
func DetectTopology() (Topology, error) {
	// Allow tests to override the cpuinfo path via environment variable.
	if env := os.Getenv("COREMAP_CPUINFO"); env != "" {
		return detectFromContent(env)
	}

	f, err := os.Open(cpuinfoPath)
	if err != nil {
		return Topology{}, fmt.Errorf("open %s: %w", cpuinfoPath, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("close %s: %v", cpuinfoPath, err)
		}
	}()

	var content strings.Builder
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		content.WriteString(scanner.Text())
		content.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return Topology{}, fmt.Errorf("read %s: %w", cpuinfoPath, err)
	}
	return detectFromContent(content.String())
}

func detectFromContent(content string) (Topology, error) {
	// Parse processor count and model name.
	procs := 0
	model := ""
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "processor") {
			procs++
		}
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				model = strings.TrimSpace(parts[1])
			}
		}
	}
	if procs == 0 {
		return Topology{}, fmt.Errorf("no processors found in cpuinfo")
	}

	// Look up the model's P/E split.
	pCount := procs / 2 // fallback: half are P
	if m, ok := knownModels[model]; ok {
		pCount = m.p
	}
	// Clamp pCount to the actual number of processors.
	if pCount > procs {
		pCount = procs
	}

	pCores := make([]int, pCount)
	for i := 0; i < pCount; i++ {
		pCores[i] = i
	}
	eCores := make([]int, procs-pCount)
	for i := 0; i < procs-pCount; i++ {
		eCores[i] = pCount + i
	}

	return Topology{
		TotalCores: procs,
		PCores:     pCores,
		ECores:     eCores,
	}, nil
}

// Load reads a coremap from path. Returns os.ErrNotExist if the file
// is missing.
func Load(path string) (*Coremap, error) {
	data, err := os.ReadFile(path) //nolint:gosec,G304
	if err != nil {
		return nil, err
	}
	var cm Coremap
	if err := yaml.Unmarshal(data, &cm); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cm, nil
}

// Save writes the coremap to path atomically (temp file + rename).
func (cm *Coremap) Save(path string) error {
	data, err := yaml.Marshal(cm)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Allocate finds a contiguous block of cores for a VM with the given
// vcpu count. E cores are tried first, then P cores (excluding dom0).
// The assignment is recorded in cm.Assignments.
func (cm *Coremap) Allocate(vcpus int) ([]int, error) {
	free := cm.FreeCores()
	// Try E cores first.
	if cores := findContiguous(cm.Topology.ECores, free, vcpus); cores != nil {
		cm.Assignments = append(cm.Assignments, Assignment{VM: "", Cores: cores})
		return cores, nil
	}
	// Fall back to P cores (excluding dom0).
	pAvail := cm.Topology.PCores
	for _, dc := range cm.Dom0Cores {
		pAvail = removeInt(pAvail, dc)
	}
	if cores := findContiguous(pAvail, free, vcpus); cores != nil {
		cm.Assignments = append(cm.Assignments, Assignment{VM: "", Cores: cores})
		return cores, nil
	}
	return nil, fmt.Errorf("no contiguous block of %d free cores", vcpus)
}

// SetVMName sets the VM name on the most recent assignment (used after
// Allocate to record which VM got the cores).
func (cm *Coremap) SetVMName(name string) {
	if len(cm.Assignments) > 0 {
		cm.Assignments[len(cm.Assignments)-1].VM = name
	}
}

// Free removes a VM's core assignment.
func (cm *Coremap) Free(vmName string) {
	for i, a := range cm.Assignments {
		if a.VM == vmName {
			cm.Assignments = append(cm.Assignments[:i], cm.Assignments[i+1:]...)
			return
		}
	}
}

// FreeCores returns a set of cores that are not assigned to any VM and
// not used by dom0.
func (cm *Coremap) FreeCores() map[int]bool {
	used := make(map[int]bool)
	for _, c := range cm.Dom0Cores {
		used[c] = true
	}
	for _, a := range cm.Assignments {
		for _, c := range a.Cores {
			used[c] = true
		}
	}
	free := make(map[int]bool)
	for c := 0; c < cm.Topology.TotalCores; c++ {
		if !used[c] {
			free[c] = true
		}
	}
	return free
}

// Rebuild reconstructs a Coremap from the topology, dom0 vcpu count, and
// existing VM metas. Stale assignments (VMs that don't exist in metas)
// are dropped.
func Rebuild(topo Topology, dom0VCPUs int, vmCores map[string][]int) *Coremap {
	dom0Cores := make([]int, dom0VCPUs)
	for i := 0; i < dom0VCPUs; i++ {
		dom0Cores[i] = i
	}
	assignments := make([]Assignment, 0, len(vmCores))
	for name, cores := range vmCores {
		if len(cores) > 0 {
			assignments = append(assignments, Assignment{VM: name, Cores: cores})
		}
	}
	return &Coremap{
		Topology:    topo,
		Dom0Cores:   dom0Cores,
		Assignments: assignments,
	}
}

// findContiguous finds a contiguous block of n cores from candidates that
// are all free. Returns nil if no such block exists.
func findContiguous(candidates []int, free map[int]bool, n int) []int {
	for i := 0; i <= len(candidates)-n; i++ {
		block := candidates[i : i+n]
		ok := true
		for _, c := range block {
			if !free[c] {
				ok = false
				break
			}
		}
		if ok {
			return block
		}
	}
	return nil
}

// removeInt removes a single value from a slice.
func removeInt(slice []int, v int) []int {
	for i, s := range slice {
		if s == v {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// DefaultPath is the default location of coremap.yaml.
const DefaultPath = "/var/lib/qlvm/coremap.yaml"

// Ensure returns a Coremap for the given path: loads it if it exists,
// otherwise detects the topology and builds a fresh one (dom0 gets the
// first N P cores). metas is used to restore existing VM assignments.
func Ensure(path string, dom0VCPUs int, vmCores map[string][]int) (*Coremap, error) {
	cm, err := Load(path)
	if err == nil {
		return cm, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	topo, err := DetectTopology()
	if err != nil {
		return nil, fmt.Errorf("detect topology: %w", err)
	}
	cm = Rebuild(topo, dom0VCPUs, vmCores)
	if err := cm.Save(path); err != nil {
		// Non-fatal: the coremap will be rebuilt next time.
		return cm, nil
	}
	return cm, nil
}

// VMDir returns the VM state directory path.
func VMDir(root, name string) string {
	return filepath.Join(root, "vms", name)
}
