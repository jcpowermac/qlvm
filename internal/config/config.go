// Package config loads and validates the qlvm YAML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MinMemoryMB is the floor for any memory_mb value (config defaults and
// `vm create --memory`). Below this the guest cannot boot (Xen's PVH
// memory layout alone needs a 16 MB aligned segment), and small values are
// almost always a gigabyte-vs-megabyte typo (memory_mb is megabytes: 4 GB
// is 4096, not 4).
const MinMemoryMB = 64

// Config is the top-level qlvm configuration (spec §4).
type Config struct {
	Network    Network    `yaml:"network"`
	Domains    []Domain   `yaml:"domain"`
	VM         VMDefaults `yaml:"vm"`
	Disposable VMDefaults `yaml:"disposable"`
	Firewall   Firewall   `yaml:"firewall"`
}

// Network describes the dom0 physical LAN side of the config.
type Network struct {
	NIC           string `yaml:"nic"`

	Gateway       string `yaml:"gateway"`
	RouterIP      string `yaml:"router_ip"`
	// Dom0IP is the dom0's uplink address (e.g. br-ex) that VMs dial
	// back to for the waypipe TCP data channel. RouterIP is NOT this:
	// it is the OVN gateway router IP, which does not forward to dom0
	// host ports.
	Dom0IP string   `yaml:"dom0_ip"`
	DNS    []string `yaml:"dns"`
}

// Domain is one isolation domain; Subnet holds the first three octets (e.g. "10.100.1").
type Domain struct {
	Name    string `yaml:"name"`
	Subnet  string `yaml:"subnet"`
	Gateway string `yaml:"gateway"`
}

// VMDefaults holds shared VM defaults; Disposable omits User.
type VMDefaults struct {
	User     string `yaml:"user,omitempty"`
	MemoryMB int    `yaml:"memory_mb"`
	VCPUs    int    `yaml:"vcpus"`
}

// Firewall holds the dom0-egress policy.
type Firewall struct {
	Egress Egress `yaml:"egress"`
}

// Egress is the set of dom0 egress allow toggles and extra rich rules.
type Egress struct {
	AllowDNS      bool     `yaml:"allow_dns"`
	AllowHTTPS    bool     `yaml:"allow_https"`
	AllowSSHToVMs bool     `yaml:"allow_ssh_to_vms"`
	AllowICMP     bool     `yaml:"allow_icmp"`
	ExtraRules    []string `yaml:"extra_rules"`
}

// Load reads and parses the YAML config at path. It does not validate.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided config location
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config to path as YAML.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) // #nosec G304,G306 -- path is the caller-provided config location
}

var subnetRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Validate checks invariants required before any control plane runs.
func (c *Config) Validate() error {
	var problems []string
	if c.Network.NIC == "" {
		problems = append(problems, "network.nic is empty")
	}
	if c.Network.Gateway == "" {
		problems = append(problems, "network.gateway is empty")
	} else if net.ParseIP(c.Network.Gateway) == nil {
		problems = append(problems, fmt.Sprintf("network.gateway %q is not a valid IP", c.Network.Gateway))
	}
	if c.Network.RouterIP == "" {
		problems = append(problems, "network.router_ip is empty")
	} else if net.ParseIP(c.Network.RouterIP) == nil {
		problems = append(problems, fmt.Sprintf("network.router_ip %q is not a valid IP", c.Network.RouterIP))
	}
	if c.Network.Dom0IP != "" && net.ParseIP(c.Network.Dom0IP) == nil {
		problems = append(problems, fmt.Sprintf("network.dom0_ip %q is not a valid IP", c.Network.Dom0IP))
	}
	if len(c.Network.DNS) == 0 {
		problems = append(problems, "network.dns is empty")
	}
	if len(c.Domains) < 1 {
		problems = append(problems, "at least one [[domain]] is required")
	}
	seen := make(map[string]bool, len(c.Domains))
	for i, d := range c.Domains {
		if seen[d.Name] {
			problems = append(problems, fmt.Sprintf("duplicate domain name %q", d.Name))
		}
		seen[d.Name] = true
		if !subnetRe.MatchString(d.Subnet) {
			problems = append(problems, fmt.Sprintf("domain[%d] subnet %q must be three octets (e.g. \"10.100.1\")", i, d.Subnet))
		}
		if net.ParseIP(d.Gateway) == nil {
			problems = append(problems, fmt.Sprintf("domain[%d] gateway %q is not a valid IP", i, d.Gateway))
		}
	}
	checkDefaults := func(label string, d VMDefaults) {
		if d.MemoryMB < MinMemoryMB {
			problems = append(problems, fmt.Sprintf("%s.memory_mb %d: below %d MB minimum (memory_mb is megabytes — 4 GB is 4096, not 4)", label, d.MemoryMB, MinMemoryMB))
		}
		if d.VCPUs <= 0 {
			problems = append(problems, label+".vcpus must be > 0")
		}
	}
	checkDefaults("vm", c.VM)
	checkDefaults("disposable", c.Disposable)
	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	return nil
}

// Domain returns the domain with the given name.
func (c *Config) Domain(name string) (Domain, bool) {
	for _, d := range c.Domains {
		if d.Name == name {
			return d, true
		}
	}
	return Domain{}, false
}

// VMSupernet returns the /16 covering all domains, derived from the first
// domain's subnet (e.g. "10.100.1" -> "10.100.0.0/16").
func (c *Config) VMSupernet() string {
	if len(c.Domains) == 0 {
		return ""
	}
	s := c.Domains[0].Subnet
	idx := strings.LastIndex(s, ".")
	if idx < 0 {
		return ""
	}
	return s[:idx] + ".0.0/16"
}
