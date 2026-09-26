// Package config loads and validates the qlvm TOML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level qlvm configuration (spec §4).
type Config struct {
	Network    Network    `toml:"network"`
	Domains    []Domain   `toml:"domain"`
	VM         VMDefaults `toml:"vm"`
	Disposable VMDefaults `toml:"disposable"`
	Firewall   Firewall   `toml:"firewall"`
}

// Network describes the dom0 physical LAN side of the config.
type Network struct {
	NIC           string   `toml:"nic"`
	NICConnection string   `toml:"nic_connection"`
	Gateway       string   `toml:"gateway"`
	RouterIP      string   `toml:"router_ip"`
	DNS           []string `toml:"dns"`
}

// Domain is one isolation domain; Subnet holds the first three octets (e.g. "10.100.1").
type Domain struct {
	Name    string `toml:"name"`
	Subnet  string `toml:"subnet"`
	Gateway string `toml:"gateway"`
}

// VMDefaults holds shared VM defaults; Disposable omits User.
type VMDefaults struct {
	User     string `toml:"user,omitempty"`
	MemoryMB int    `toml:"memory_mb"`
	VCPUs    int    `toml:"vcpus"`
}

// Firewall holds the dom0-egress policy.
type Firewall struct {
	Egress Egress `toml:"egress"`
}

// Egress is the set of dom0 egress allow toggles and extra rich rules.
type Egress struct {
	AllowDNS      bool     `toml:"allow_dns"`
	AllowHTTPS    bool     `toml:"allow_https"`
	AllowSSHToVMs bool     `toml:"allow_ssh_to_vms"`
	AllowICMP     bool     `toml:"allow_icmp"`
	ExtraRules    []string `toml:"extra_rules"`
}

// Load reads and parses the TOML config at path. It does not validate.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided config location
	if err != nil {
		return nil, err
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config to path as TOML.
func (c *Config) Save(path string) error {
	f, err := os.Create(path) // #nosec G304 -- path is the caller-provided config location
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
		if d.MemoryMB <= 0 {
			problems = append(problems, label+".memory_mb must be > 0")
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
