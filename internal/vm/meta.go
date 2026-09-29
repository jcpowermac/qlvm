package vm

import (
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Meta is the per-VM state persisted as meta.toml in the VM's state dir
// (spec §6.5: create prepares, start boots from meta — Xen has no stopped
// domains).
type Meta struct {
	Name     string    `toml:"name"`
	Type     string    `toml:"type"`
	Image    string    `toml:"image"`
	Digest   string    `toml:"digest"`
	Domain   string    `toml:"domain"`
	IP       string    `toml:"ip"`
	MAC      string    `toml:"mac"`
	MemoryMB int       `toml:"memory_mb"`
	VCPUs    int       `toml:"vcpus"`
	Mounts   []Mount   `toml:"mounts"`
	UUID     string    `toml:"uuid"`
	Created  time.Time `toml:"created"`
}

// Mount is one p9 share: Host path on dom0, Guest tag inside the VM.
type Mount struct {
	Host  string `toml:"host"`
	Guest string `toml:"guest"`
}

const metaFile = "meta.toml"

// LoadMeta reads <vmDir>/meta.toml.
func LoadMeta(vmDir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(vmDir, metaFile)) // #nosec G304 -- vmDir is the VM dir under the qlvm state root
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save writes the meta to <vmDir>/meta.toml (vmDir must exist).
func (m *Meta) Save(vmDir string) error {
	data, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	// 0644: read by `qlvm run` in the user's session (no secrets in Meta).
	return os.WriteFile(filepath.Join(vmDir, metaFile), data, 0o644) //nolint:gosec // G306: see comment
}
