package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Meta is the per-VM state persisted as meta.yaml in the VM's state dir
// (spec §6.5: create prepares, start boots from meta — Xen has no stopped
// domains).
type Meta struct {
	Name     string    `yaml:"name"`
	Type     string    `yaml:"type"`
	Image    string    `yaml:"image"`
	Digest   string    `yaml:"digest"`
	Domain   string    `yaml:"domain"`
	IP       string    `yaml:"ip"`
	MAC      string    `yaml:"mac"`
	MemoryMB int       `yaml:"memory_mb"`
	VCPUs    int       `yaml:"vcpus"`
	Mounts   []Mount   `yaml:"mounts"`
	UUID     string    `yaml:"uuid"`
	Created  time.Time `yaml:"created"`
	// Token authenticates the waypipe control channel (guest :4711); the
	// same secret is baked into the guest's /etc/qvm/waypipe-token.
	Token string `yaml:"token"`
}

// Mount is one p9 share: Host path on dom0, Guest tag inside the VM.
type Mount struct {
	Host  string `yaml:"host"`
	Guest string `yaml:"guest"`
}

const metaFile = "meta.yaml"

// LoadMeta reads <vmDir>/meta.yaml.
func LoadMeta(vmDir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(vmDir, metaFile)) // #nosec G304 -- vmDir is the VM dir under the qlvm state root
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, legacy := os.Stat(filepath.Join(vmDir, "meta.toml")); legacy == nil {
				return nil, fmt.Errorf("%s: legacy meta.toml found — convert it to meta.yaml (YAML; same fields) and retry", vmDir)
			}
		}
		return nil, err
	}
	var m Meta
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save writes the meta to <vmDir>/meta.yaml (vmDir must exist).
func (m *Meta) Save(vmDir string) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	// 0644: read by `qlvm vm run` in the user's session (no secrets in Meta).
	return os.WriteFile(filepath.Join(vmDir, metaFile), data, 0o644) //nolint:gosec // G306: see comment
}
