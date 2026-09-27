// Package vm implements Xen VM lifecycle operations.
package vm

import (
	"fmt"

	"github.com/jcpowermac/qlvm/internal/config"
)

// HostIPMAC derives the IP and MAC for a new host in a domain.
// hostnum = max(maxHostnum, 9) + 1, so the first VM lands at .10 and a
// delete-then-create never reuses a live VM's number (OVN port-security
// turns a reused IP/MAC into a silent network failure); ip =
// <subnet>.<hostnum>; mac = 02:00:00:00:<hi>:<lo>.
func HostIPMAC(d config.Domain, maxHostnum int) (string, string) {
	hostnum := max(maxHostnum, 9) + 1
	ip := fmt.Sprintf("%s.%d", d.Subnet, hostnum)
	mac := fmt.Sprintf("02:00:00:00:%02x:%02x", hostnum/256%256, hostnum%256)
	return ip, mac
}
