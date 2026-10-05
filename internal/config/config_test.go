package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const baseTOML = `
[network]
nic = "enp1s0"
gateway = "192.168.1.1"
router_ip = "192.168.1.200"
dns = ["1.1.1.1", "1.0.0.1"]

[[domain]]
name = "work"
subnet = "10.100.1"
gateway = "10.100.1.1"

[[domain]]
name = "personal"
subnet = "10.100.2"
gateway = "10.100.2.1"

[vm]
user = "user"
memory_mb = 4096
vcpus = 2

[disposable]
memory_mb = 4096
vcpus = 2

[firewall.egress]
allow_dns = true
allow_https = true
allow_ssh_to_vms = true
allow_icmp = true
extra_rules = []
`

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "qlvm.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFixture(t *testing.T) {
	cfg, err := Load("testdata/fixture.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	wantNet := Network{
		NIC:     "enp1s0",
		Gateway:       "192.168.1.1",
		RouterIP:      "192.168.1.200",
		DNS:           []string{"1.1.1.1", "1.0.0.1"},
	}
	if !reflect.DeepEqual(cfg.Network, wantNet) {
		t.Errorf("Network = %+v, want %+v", cfg.Network, wantNet)
	}

	wantDomains := []Domain{
		{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"},
		{Name: "personal", Subnet: "10.100.2", Gateway: "10.100.2.1"},
	}
	if !reflect.DeepEqual(cfg.Domains, wantDomains) {
		t.Errorf("Domains = %+v, want %+v", cfg.Domains, wantDomains)
	}

	wantVM := VMDefaults{User: "user", MemoryMB: 4096, VCPUs: 2}
	if cfg.VM != wantVM {
		t.Errorf("VM = %+v, want %+v", cfg.VM, wantVM)
	}
	wantDisp := VMDefaults{MemoryMB: 4096, VCPUs: 2}
	if cfg.Disposable != wantDisp {
		t.Errorf("Disposable = %+v, want %+v", cfg.Disposable, wantDisp)
	}

	e := cfg.Firewall.Egress
	if !e.AllowDNS || !e.AllowHTTPS || !e.AllowSSHToVMs || !e.AllowICMP {
		t.Errorf("Egress booleans = %+v, want all true", e)
	}
	if e.ExtraRules == nil || len(e.ExtraRules) != 0 {
		t.Errorf("ExtraRules = %v, want empty", e.ExtraRules)
	}
}

func TestLoadValidateErrors(t *testing.T) {
	cases := map[string]string{
		"empty NIC":      strings.Replace(baseTOML, `nic = "enp1s0"`, `nic = ""`, 1),
		"duplicate name": strings.Replace(baseTOML, `name = "personal"`, `name = "work"`, 1),
		"4-octet subnet": strings.Replace(baseTOML, `subnet = "10.100.1"`, `subnet = "10.100.1.1"`, 1),
		"MemoryMB=0":     strings.Replace(baseTOML, `memory_mb = 4096`, `memory_mb = 0`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeTOML(t, body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate: expected error, got nil")
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	cfg, err := Load("testdata/fixture.toml")
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.toml")
	if err := cfg.Save(out); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("round trip mismatch:\ngot  %+v\nwant %+v", got, cfg)
	}
}

func TestDomainLookup(t *testing.T) {
	cfg, err := Load("testdata/fixture.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, ok := cfg.Domain("work")
	if !ok {
		t.Fatal("Domain(work): expected found")
	}
	if d != (Domain{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"}) {
		t.Errorf("Domain(work) = %+v", d)
	}
	if _, ok := cfg.Domain("nope"); ok {
		t.Error("Domain(nope): expected not found")
	}
}

func TestVMSupernet(t *testing.T) {
	cfg, err := Load("testdata/fixture.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.VMSupernet(); got != "10.100.0.0/16" {
		t.Errorf("VMSupernet() = %q, want %q", got, "10.100.0.0/16")
	}
}
