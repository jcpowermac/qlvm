// Command qlvm-vif is the Xen vif hotplug script, installed by
// `qlvm install` at /etc/xen/scripts/vif-ovn (spec §10). libxl (Xen 4.21)
// execs it as
//
//	qlvm-vif <online|offline> type_if=vif
//
// and passes the device name in the `vif` environment variable (e.g.
// vif51.0) and the device's backend xenstore directory in XENBUS_PATH
// (backend/vif/<domid>/<devid>); the frontend-id, domain name/uuid and
// MAC are read from xenstore underneath XENBUS_PATH. On online it adds
// the OVS port to br-int via libovsdb, then brings the internal netdev
// up with a raw netlink request. Exit 0/1 per the Xen vif-script
// convention.
package main

import (
	"context"
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
	// XENBUS_PATH is backend/vif/<domid>/<devid>; the domid and devid are
	// in the path. (The device's frontend-id node is the guest-side
	// device number, not the domain id.) Xen 4.21 xenstore layout:
	//   /local/domain/<domid>/name                     domain name
	//   /local/domain/<domid>/vm      -> /vm/<uuid>    symlink to the vm tree
	//   /local/domain/<domid>/device/vif/<devid>/mac   the vif MAC
	parts := strings.Split(xbus, "/")
	if len(parts) != 4 {
		return fmt.Errorf("unexpected XENBUS_PATH %q", xbus)
	}
	dom := "/local/domain/" + parts[2]
	name, err := xs.Read(dom + "/name")
	if err != nil {
		return fmt.Errorf("read domain name: %w", err)
	}
	// There is no <domid>/uuid node in modern Xen; the vm symlink target
	// is /vm/<uuid>.
	vmLink, err := xs.Read(dom + "/vm")
	if err != nil {
		return fmt.Errorf("read vm symlink: %w", err)
	}
	uuid := strings.TrimPrefix(vmLink, "/vm/")
	mac, err := xs.Read(fmt.Sprintf("%s/device/vif/%s/mac", dom, parts[3]))
	if err != nil {
		return fmt.Errorf("read vif mac: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	if err := vif.AddVifPort(ctx, dev, name, uuid, mac); err != nil {
		return fmt.Errorf("add vif port %s: %w", dev, err)
	}
	return bringUp(ctx, dev, linkUp)
}

// bringUp sets IFF_UP on dev and polls the kernel until the flag is set.
// The retry covers ovs-vswitchd binding the netdev asynchronously after
// the OVSDB commit (the flag can be cleared between our set and the bind
// completing); without it the port wedges down and the guest is
// unreachable until a hand "ip link set up". Retries stop at the hotplug
// context deadline so a broken device fails the bring-up (libxl sees exit
// 1) instead of hanging it. Success is the IFF_UP flag, not operstate:
// a fresh-boot vif has no carrier until the guest's netfront comes up,
// which is past the hotplug deadline.
func bringUp(ctx context.Context, dev string, linkUp func(string) error) error {
	var lastErr error
	for {
		if err := linkUp(dev); err != nil {
			lastErr = err
		} else if flagsUpFn(dev) {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("link up %s: %w", dev, lastErr)
			}
			return fmt.Errorf("link up %s: still down at deadline", dev)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// flagsUpFn is bringUp's kernel link-state check; tests replace it.
var flagsUpFn = flagsUp

// flagsUp reports whether the kernel's IFF_UP flag is set on dev. This is
// the right success signal for the hotplug: operstate additionally needs
// carrier, which for a vif only appears once the (still-booting) guest's
// netfront comes up — well past the hotplug deadline.
func flagsUp(dev string) bool {
	// #nosec G304,G703 -- dev comes from libxl's vif argv (a bare
	// "vifN.M" name, never a path separator).
	b, err := os.ReadFile("/sys/class/net/" + dev + "/flags")
	if err != nil {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 0, 32) // #nosec G115 -- sysfs prints hex
	if err != nil {
		return false
	}
	return n&int64(unix.IFF_UP) != 0
}

// wire builds the real planes. Tests replace it.
var wire = func() (XsReader, ovs.VifPorter, func(string) error, error) {
	vif, err := ovs.NewLive(context.Background())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ovsdb: %w", err)
	}
	xs, err := xenstore.Dial(xenstore.SocketPath())
	if err != nil {
		return nil, nil, nil, err
	}
	return xs, vif, ioctlSetUp, nil
}

// run is the testable entry point; it maps a VifHandle failure to exit 1
// per the vif-script convention. libxl passes only the command (plus the
// "type_if=vif" flag) as argv; the device name comes from the `vif`
// environment variable.
func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: qlvm-vif <online|offline> [type_if=vif]")
		return 1
	}
	command := args[0]
	dev := os.Getenv("vif")
	// -emu (HVM guard) is a no-op and must not require a live control
	// plane, so it exits before wiring.
	if strings.HasSuffix(dev, "-emu") {
		return 0
	}
	xs, vif, linkUp, err := wire()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qlvm-vif:", err)
		// bash parity (do_without_error): teardown of a dying domain
		// must not fail when the control plane is unreachable (e.g. OVS
		// restarted after the VM started).
		if command == "remove" || command == "offline" {
			return 0
		}
		return 1
	}
	if err := VifHandle(command, dev, xs, vif, linkUp); err != nil {
		fmt.Fprintln(os.Stderr, "qlvm-vif:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}


// ioctlSetUp brings an OVS-managed vif netdev up with
// ioctl(SIOCSIFFLAGS): read the kernel's current flag set from sysfs, OR
// in IFF_UP, and write it back. SIOCSIFFLAGS replaces the whole flag word,
// so the read-modify-write is required to keep BROADCAST/MULTICAST/PROMISC.
//
// (The original raw RTM_NEWLINK here put IFF_UP in ifinfomsg.ifi_flags:
// the kernel only honors that field when the device is CREATED, acks the
// request with success for an existing one, and silently leaves the flags
// alone — so every restarted VM came up with its vif wedged down. The
// ioctl is the same call ifconfig/ip use, via x/sys's Ifreq helpers.)
func ioctlSetUp(dev string) error {
	// #nosec G304,G703 -- dev comes from libxl's vif argv (a bare
	// "vifN.M" name, never a path separator).
	raw, err := os.ReadFile("/sys/class/net/" + dev + "/flags")
	if err != nil {
		return err
	}
	// sysfs prints the word in hex ("0x1102"); base 0 parses the 0x prefix.
	cur, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 0, 32) // #nosec G115 -- 32-bit sysfs word
	if err != nil {
		return fmt.Errorf("flags for %s: %w", dev, err)
	}
	// SIOCSIFFLAGS speaks the 16-bit if-flag word; the sysfs read is the
	// same word zero-extended, so mask it back down before the OR.
	flags := uint16(cur) | uint16(unix.IFF_UP) // #nosec G115 -- if-flags are 16-bit by kernel contract

	ifr, err := unix.NewIfreq(dev)
	if err != nil {
		return fmt.Errorf("ifreq for %s: %w", dev, err)
	}
	ifr.SetUint16(flags)

	// Any fd will do for SIOCSIFFLAGS; a dummy socket is the conventional
	// choice and costs nothing.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}
