package vm

import (
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
)

func TestHostIPMAC(t *testing.T) {
	d := config.Domain{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"}
	cases := []struct {
		maxHostnum      int
		wantIP, wantMAC string
	}{
		{0, "10.100.1.10", "02:00:00:00:00:0a"},
		// nothing at .10 yet: max(9) + 1 still starts the domain at .10.
		{9, "10.100.1.10", "02:00:00:00:00:0a"},
		{10, "10.100.1.11", "02:00:00:00:00:0b"},
		{12, "10.100.1.13", "02:00:00:00:00:0d"},
		// hostnum 216 = 0x00d8; the old count+10 vector was an arithmetic typo (supervisor ruling 2026-09-26).
		{215, "10.100.1.216", "02:00:00:00:00:d8"},
	}
	for _, c := range cases {
		ip, mac := HostIPMAC(d, c.maxHostnum)
		if ip != c.wantIP {
			t.Errorf("HostIPMAC(maxHostnum=%d) ip = %q, want %q", c.maxHostnum, ip, c.wantIP)
		}
		if mac != c.wantMAC {
			t.Errorf("HostIPMAC(maxHostnum=%d) mac = %q, want %q", c.maxHostnum, mac, c.wantMAC)
		}
	}
}

// TestHostIPMACDeleteThenCreate: VMs at .10 .11 .12; .11 is deleted, leaving
// max hostnum 12. The next allocation must be .13 (max+1) — a count-based
// rule hands .12 back out to a live VM's IP/MAC, which OVN port-security
// turns into a silent network failure.
func TestHostIPMACDeleteThenCreate(t *testing.T) {
	d := config.Domain{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"}
	ip, _ := HostIPMAC(d, 12)
	if ip != "10.100.1.13" {
		t.Errorf("hostnum after .11 delete = %s, want 10.100.1.13 (max+1, not count+10)", ip)
	}
}
