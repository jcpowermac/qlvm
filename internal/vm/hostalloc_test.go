package vm

import (
	"testing"

	"github.com/jcpowermac/qlvm/internal/config"
)

func TestHostIPMAC(t *testing.T) {
	d := config.Domain{Name: "work", Subnet: "10.100.1", Gateway: "10.100.1.1"}
	cases := []struct {
		existing        int
		wantIP, wantMAC string
	}{
		{0, "10.100.1.10", "02:00:00:00:00:0a"},
		{9, "10.100.1.19", "02:00:00:00:00:13"},
		// hostnum 215 = 0x00d7; brief's 01:07 vector was an arithmetic typo (supervisor ruling 2026-09-26).
		{205, "10.100.1.215", "02:00:00:00:00:d7"},
	}
	for _, c := range cases {
		ip, mac := HostIPMAC(d, c.existing)
		if ip != c.wantIP {
			t.Errorf("HostIPMAC(%d) ip = %q, want %q", c.existing, ip, c.wantIP)
		}
		if mac != c.wantMAC {
			t.Errorf("HostIPMAC(%d) mac = %q, want %q", c.existing, mac, c.wantMAC)
		}
	}
}
