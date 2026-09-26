// Package vm implements QEMU VM lifecycle operations.
package vm

import (
	"fmt"

	"github.com/jcpowermac/qlvm/internal/config"
)

// HostIPMAC derives the IP and MAC for a new host in a domain.
// hostnum = existing+10; ip = <subnet>.<hostnum>; mac = 02:00:00:00:<hi>:<lo>.
func HostIPMAC(d config.Domain, existing int) (string, string) {
	hostnum := existing + 10
	ip := fmt.Sprintf("%s.%d", d.Subnet, hostnum)
	mac := fmt.Sprintf("02:00:00:00:%02x:%02x", hostnum/256%256, hostnum%256)
	return ip, mac
}
