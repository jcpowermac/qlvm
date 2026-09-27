// Command qlvm-vif is the Xen vif hotplug script, installed by
// `qlvm install` at /etc/xen/scripts/vif-ovn (spec §10). libxl execs it
// as
//
//	qlvm-vif <command> <dev> <domid> <mac> <port>
//
// with command one of add|remove|online|offline. On add|online it reads
// the VM's identity from xenstore and adds the OVS port to br-int via
// libovsdb, then brings the internal netdev up with a raw netlink
// request. Exit 0/1 per the Xen vif-script convention.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/xenstore"
)

// XsReader is the xenstore surface the vif script needs; *xenstore.Client
// satisfies it.
type XsReader interface {
	Read(path string) (string, error)
}

// handleTimeout bounds one hotplug call so a wedged OVSDB connection
// cannot hang libxl's device bring-up forever.
const handleTimeout = 30 * time.Second

// int32Max bounds the sysfs ifindex before it is put into the netlink
// IfInfomsg (a 32-bit field).
const int32Max = int(^uint32(0) >> 1)

// VifHandle implements one hotplug invocation.
//
//   - a "<dev>-emu" device is an HVM guard: no-op, always success;
//   - add|online: frontend-id → domain name/uuid → MAC from $XENBUS_PATH
//     (set by libxl) → AddVifPort → link up. Any failure is returned so
//     main exits 1 and libxl retries;
//   - remove|offline: DelVifPort, errors swallowed (bash parity:
//     do_without_error) — a dying domain must never be wedged on vif
//     teardown. Link down is implicit: removing the OVS internal port
//     removes the netdev.
func VifHandle(command, dev string, xs XsReader, vif ovs.VifPorter, linkUp func(dev string) error) error {
	if strings.HasSuffix(dev, "-emu") {
		return nil
	}
	switch command {
	case "add", "online":
		return vifAdd(xs, vif, linkUp, dev)
	case "remove", "offline":
		ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
		defer cancel()
		_ = vif.DelVifPort(ctx, dev)
		return nil
	default:
		return fmt.Errorf("unknown vif command %q", command)
	}
}

func vifAdd(xs XsReader, vif ovs.VifPorter, linkUp func(string) error, dev string) error {
	// libxl sets XENBUS_PATH to this device's backend xenbus dir.
	xbus := os.Getenv("XENBUS_PATH")
	if xbus == "" {
		return fmt.Errorf("XENBUS_PATH not set")
	}
	frontendID, err := xs.Read(xbus + "/frontend-id")
	if err != nil {
		return fmt.Errorf("read frontend-id: %w", err)
	}
	dom := "/local/domain/" + frontendID
	name, err := xs.Read(dom + "/name")
	if err != nil {
		return fmt.Errorf("read domain name: %w", err)
	}
	uuid, err := xs.Read(dom + "/uuid")
	if err != nil {
		return fmt.Errorf("read domain uuid: %w", err)
	}
	mac, err := xs.Read(xbus + "/frontend/mac")
	if err != nil {
		return fmt.Errorf("read frontend mac: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	if err := vif.AddVifPort(ctx, dev, name, uuid, mac); err != nil {
		return fmt.Errorf("add vif port %s: %w", dev, err)
	}
	if err := linkUp(dev); err != nil {
		return fmt.Errorf("link up %s: %w", dev, err)
	}
	return nil
}

// wire builds the real planes. Tests replace it.
var wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
	vif, err := ovs.NewLive(context.Background())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ovsdb: %w", err)
	}
	xs, err := xenstore.Dial(xenstore.DefaultSocket)
	if err != nil {
		return nil, nil, nil, err
	}
	return xs, vif, netlinkSetUp, nil
}

// run is the testable entry point; it maps a VifHandle failure to exit 1
// per the vif-script convention.
func run(args []string) int {
	if len(args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: qlvm-vif <command> <dev> <domid> <mac> <port>")
		return 1
	}
	xs, vif, linkUp, err := wire()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qlvm-vif:", err)
		return 1
	}
	if err := VifHandle(args[0], args[1], xs, vif, linkUp); err != nil {
		fmt.Fprintln(os.Stderr, "qlvm-vif:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// netlinkSetUp brings an OVS internal netdev up with a raw RTM_NEWLINK
// request (IFF_UP). OVS creates internal ports administratively down and
// qlvm has no management-CLI shell-out, so the kernel is spoken to
// directly. The interface index comes from sysfs.
func netlinkSetUp(dev string) error {
	// #nosec G304,G703 -- dev comes from libxl's vif argv (a bare
	// "vifN.M" name, never a path separator).
	raw, err := os.ReadFile("/sys/class/net/" + dev + "/ifindex")
	if err != nil {
		return err
	}
	idx, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("ifindex for %s: %w", dev, err)
	}
	// ponytail: ifindex is 32-bit by kernel contract; a guard keeps the
	// uint32 conversion below overflow-free.
	if idx <= 0 || idx > int32Max {
		return fmt.Errorf("ifindex %d out of range for %s", idx, dev)
	}

	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}

	name := make([]byte, 16)
	copy(name, dev)
	payload := make([]byte, 12+len(name))
	payload[0] = unix.AF_UNSPEC
	binary.LittleEndian.PutUint32(payload[4:8], uint32(idx)) // IfInfomsg.Index (guarded above) // #nosec G115
	binary.LittleEndian.PutUint32(payload[8:12], uint32(unix.IFF_UP))
	copy(payload[12:], name)

	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr, 16+uint32(len(payload))) // #nosec G115 -- constant-size message
	binary.LittleEndian.PutUint16(hdr[4:], unix.RTM_NEWLINK)
	binary.LittleEndian.PutUint16(hdr[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.LittleEndian.PutUint32(hdr[8:], 1) // seq

	if err := unix.Sendto(fd, append(hdr, payload...), 0, nil); err != nil {
		return err
	}
	// With NLM_F_ACK the kernel answers exactly one NLMSG_ERROR.
	reply := make([]byte, 128)
	n, _, err := unix.Recvfrom(fd, reply, 0)
	if err != nil {
		return err
	}
	if n < 20 {
		return fmt.Errorf("short netlink reply for %s", dev)
	}
	// #nosec G115 -- errno is the 4-byte field of the kernel's NLMSG_ERROR reply.
	if errno := int32(binary.LittleEndian.Uint32(reply[16:20])); errno != 0 {
		return fmt.Errorf("netlink RTM_NEWLINK %s: %w", dev, unix.Errno(errno)) // #nosec G115 -- kernel errno
	}
	return nil
}
