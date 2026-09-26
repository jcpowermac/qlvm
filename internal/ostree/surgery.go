// Package ostree bakes bootc ostree templates (Task 8): loop-attach the
// adopted raw image, locate the ostree root and /boot partitions, copy the
// boot kernel + initramfs, write the VM identity and unit mask into the
// deployment /etc overlay, and persist the enriched template META.
package ostree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jcpowermac/qlvm/internal/template"
)

const (
	// defaultVMUser is the identity every template is baked with (supervisor
	// ruling 2026-09-26): locked password, host-side auth.
	defaultVMUser = "user"
	defaultVMUID  = 1000
)

// NetworkdFile renders the per-VM systemd-networkd unit written by
// BakeNetworkd: a static /24 address, the unit is the default route config
// (Domains=~/).
func NetworkdFile(ip, gw, mac, dns string) string {
	return "[Match]\nMACAddress=" + mac + "\n\n" +
		"[Network]\nAddress=" + ip + "/24\nGateway=" + gw + "\nDNS=" + dns + "\nDomains=~/\n"
}

// IdentityLines returns the passwd/group/shadow entries for the baked VM
// user. The shadow entry locks the password (!).
func IdentityLines(user string, uid int) (passwd, group, shadow string) {
	uidS := strconv.Itoa(uid)
	passwd = user + ":x:" + uidS + ":" + uidS + "::/home/" + user + ":/bin/bash"
	group = user + ":x:" + uidS + ":"
	shadow = user + ":!:19000:0:99999:7:::"
	return
}

// lessV is a version-aware string compare (numeric runs compare numerically)
// used to pick the lowest ostree boot.* deployment directory.
func lessV(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if isDigit(ca) && isDigit(cb) {
			na, ai2 := numFrom(a, ai)
			nb, bi2 := numFrom(b, bi)
			ai, bi = ai2, bi2
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		ai++
		bi++
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func numFrom(s string, i int) (int, int) {
	n := 0
	for i < len(s) && isDigit(s[i]) {
		n = n*10 + int(s[i]-'0')
		i++
	}
	return n, i
}

// ostreePath computes the template boot path: the lowest (version-sorted)
// boot.* deployment directory, its osid, its commit, "/0".
func ostreePath(ostreeDir string, fs FS) (path, osid, commit string, err error) {
	entries, err := fs.ReadDir(ostreeDir)
	if err != nil {
		return "", "", "", fmt.Errorf("list %s: %w", ostreeDir, err)
	}
	var boots []string
	for _, e := range entries {
		if strings.HasPrefix(e, "boot.") {
			boots = append(boots, e)
		}
	}
	if len(boots) == 0 {
		return "", "", "", fmt.Errorf("%s: no boot.* deployment", ostreeDir)
	}
	sort.Slice(boots, func(i, j int) bool { return lessV(boots[i], boots[j]) })
	boot := boots[0]
	osids, err := fs.ReadDir(filepath.Join(ostreeDir, boot))
	if err != nil {
		return "", "", "", err
	}
	if len(osids) != 1 {
		return "", "", "", fmt.Errorf("%s: want exactly one osid, got %v", filepath.Join(ostreeDir, boot), osids)
	}
	osid = osids[0]
	commits, err := fs.ReadDir(filepath.Join(ostreeDir, boot, osid))
	if err != nil {
		return "", "", "", err
	}
	if len(commits) != 1 {
		return "", "", "", fmt.Errorf("%s: want exactly one commit, got %v", filepath.Join(ostreeDir, boot, osid), commits)
	}
	commit = commits[0]
	return "/ostree/" + boot + "/" + osid + "/" + commit + "/0", osid, commit, nil
}

func mountTarget() (string, func(), error) {
	dir, err := os.MkdirTemp("", "qlvm-ostree-") // #nosec G304 -- internal mount point under the system temp dir
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// probePart classifies a mounted partition: "root" holds ostree/repo, "boot"
// holds ostree/<osid>/vmlinuz-*, anything else is "".
func probePart(target string, fs FS) string {
	entries, err := fs.ReadDir(target)
	if err != nil || !contains(entries, "ostree") {
		return ""
	}
	osids, err := fs.ReadDir(filepath.Join(target, "ostree"))
	if err != nil {
		return ""
	}
	if contains(osids, "repo") {
		return "root"
	}
	for _, e := range osids {
		if e == "deploy" || strings.HasPrefix(e, "boot.") {
			continue
		}
		files, err := fs.ReadDir(filepath.Join(target, "ostree", e))
		if err != nil {
			continue
		}
		for _, f := range files {
			if strings.HasPrefix(f, "vmlinuz-") {
				return "boot"
			}
		}
	}
	return ""
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

type partMount struct {
	target  string
	cleanup func()
}

// findParts mounts each partition (ro as given), classifies it, and returns
// the kept mounts. Non-matching partitions are umounted as they are probed;
// when wantBoot is false the scan stops once the root is found. Every mount
// is umounted before a failure is returned.
func findParts(ctx context.Context, fs FS, loop string, ro, wantBoot bool, tag string) (root, boot partMount, rootPart, bootPart string, err error) {
	var kept []partMount
	release := func() {
		for _, m := range kept {
			_ = fs.Umount(m.target)
			m.cleanup()
		}
		kept = nil
	}
	for _, part := range fs.Partitions(loop) {
		if err := ctx.Err(); err != nil {
			release()
			return partMount{}, partMount{}, "", "", err
		}
		target, cleanup, err := mountTarget()
		if err != nil {
			release()
			return partMount{}, partMount{}, "", "", err
		}
		dev := "/dev/" + part
		if err := fs.Mount(dev, target, ro); err != nil {
			cleanup()
			release()
			return partMount{}, partMount{}, "", "", fmt.Errorf("%s %s: %w", tag, dev, err)
		}
		switch probePart(target, fs) {
		case "root":
			root, rootPart = partMount{target, cleanup}, part
			kept = append(kept, root)
		case "boot":
			boot, bootPart = partMount{target, cleanup}, part
			kept = append(kept, boot)
		default:
			_ = fs.Umount(target)
			cleanup()
		}
		if rootPart != "" && (!wantBoot || bootPart != "") {
			return root, boot, rootPart, bootPart, nil
		}
	}
	release()
	return partMount{}, partMount{}, "", "", fmt.Errorf("%s: template disk %s has no ostree root (and /boot) partition pair", tag, loop)
}

// bootKernel finds the deployment kernel and initramfs on the /boot
// partition: ostree/<osid>/vmlinuz-<ver>, ostree/<osid>/initramfs-<ver>.
// It returns the kernel version and ostree-relative file names.
func bootKernel(bootTarget string, fs FS) (ver, kernelRel, initramfsRel string, err error) {
	osids, err := fs.ReadDir(filepath.Join(bootTarget, "ostree"))
	if err != nil {
		return "", "", "", err
	}
	var osidDirs []string
	for _, e := range osids {
		if e != "deploy" && !strings.HasPrefix(e, "boot.") {
			osidDirs = append(osidDirs, e)
		}
	}
	if len(osidDirs) != 1 {
		return "", "", "", fmt.Errorf("%s: want exactly one osid dir, got %v", filepath.Join(bootTarget, "ostree"), osids)
	}
	files, err := fs.ReadDir(filepath.Join(bootTarget, "ostree", osidDirs[0]))
	if err != nil {
		return "", "", "", err
	}
	for _, f := range files {
		if strings.HasPrefix(f, "vmlinuz-") {
			ver = f[len("vmlinuz-"):]
			kernelRel = filepath.Join("ostree", osidDirs[0], f)
		}
	}
	if ver == "" {
		return "", "", "", fmt.Errorf("%s: no vmlinuz-*", osidDirs[0])
	}
	want := "initramfs-" + ver
	if !contains(files, want) {
		return "", "", "", fmt.Errorf("%s: no %s", osidDirs[0], want)
	}
	return ver, kernelRel, filepath.Join("ostree", osidDirs[0], want), nil
}

// mountedFSType reports the template RootFlags for a mounted root: btrfs
// needs subvol=root (template.Template contract), anything else is "".
func mountedFSType(target string, fs FS) (string, error) {
	data, err := fs.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read mountinfo: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[4] != target {
			continue
		}
		for i := 5; i+3 <= len(f); i++ {
			if f[i] == "-" {
				if f[i+1] == "btrfs" {
					return "subvol=root", nil
				}
				return "", nil
			}
		}
	}
	return "", fmt.Errorf("mountinfo: no entry for %s", target)
}

// loopOf maps a partition node name (loop0p2) to its loop node name (loop0).
func loopOf(part string) string {
	return part[:strings.LastIndex(part, "p")]
}

// writeIdentity appends (or creates) the passwd/group/shadow entries of the
// baked VM user in the deployment /etc overlay.
func writeIdentity(fs FS, etc string) error {
	passwd, group, shadow := IdentityLines(defaultVMUser, defaultVMUID)
	if err := writeLine(fs, filepath.Join(etc, "passwd"), passwd, 0o644); err != nil {
		return err
	}
	if err := writeLine(fs, filepath.Join(etc, "group"), group, 0o644); err != nil {
		return err
	}
	return writeLine(fs, filepath.Join(etc, "shadow"), shadow, 0o600)
}

func writeLine(fs FS, path, line string, mode os.FileMode) error {
	old, err := fs.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		old = nil
	}
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		old = append(old, '\n')
	}
	return fs.WriteFile(path, append(old, []byte(line+"\n")...), mode)
}

// maskResolvedUnit masks systemd-resolved in the deployment /etc overlay —
// the one "unit" the brief leaves unnamed (supervisor ruling 2026-09-26):
// the per-VM 10-bolt.network owns the static address and DNS.
func maskResolvedUnit(etc string) error {
	dir := filepath.Join(etc, "systemd", "system")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	link := filepath.Join(dir, "systemd-resolved.service")
	_ = os.Remove(link)
	return os.Symlink("/dev/null", link)
}

// dirDigest recovers the image digest from the DirFor dir-name layout
// (<slug>-<digest>): the suffix after the last dash, which itself carries no
// dash. A malformed suffix fails the bake rather than saving a
// wrong-digest META (supervisor ruling 2026-09-26).
func dirDigest(dir string) (string, error) {
	base := filepath.Base(dir)
	i := strings.LastIndex(base, "-")
	if i < 0 {
		return "", fmt.Errorf("%s: not a <slug>-<digest> template dir", base)
	}
	d := base[i+1:]
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("%s: %q is not a sha256 digest", base, d)
	}
	return d, nil
}

// saveMeta persists the enriched template META in dir. The digest comes from
// the DirFor dir name (see dirDigest); a pre-existing META contributes Image,
// which the DirFor name cannot carry.
func saveMeta(dir, kernelVer, rootDev, rootFlags, opath string) error {
	digest, err := dirDigest(dir)
	if err != nil {
		return err
	}
	t := &template.Template{
		Dir:        dir,
		Digest:     digest,
		KernelVer:  kernelVer,
		RootDev:    rootDev,
		RootFlags:  rootFlags,
		OstreePath: opath,
	}
	if old, err := template.LoadMeta(dir); err == nil {
		t.Image = old.Image
	}
	return t.SaveMeta(dir)
}

// BakeTemplate prepares the adopted template raw image in dir (Task 7
// EnsureOpts.Bake): loop-attach, find the ostree root and /boot partitions
// (mounted read-only), copy vmlinuz+initramfs into dir, write the VM
// identity and the systemd-resolved mask into the deployment /etc overlay,
// and persist the enriched template META. Every loop device and mount is
// released on every exit path.
func BakeTemplate(ctx context.Context, fs FS, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	disk := filepath.Join(dir, "template.raw")
	loop, err := fs.LoopAttach(disk)
	if err != nil {
		return fmt.Errorf("attach %s: %w", disk, err)
	}
	defer func() { _ = fs.LoopDetach(loop) }()

	root, boot, rootPart, _, err := findParts(ctx, fs, loop, true, true, "mount")
	if err != nil {
		return err
	}
	defer func() {
		_ = fs.Umount(root.target)
		root.cleanup()
		_ = fs.Umount(boot.target)
		boot.cleanup()
	}()

	ver, kernelRel, initramfsRel, err := bootKernel(boot.target, fs)
	if err != nil {
		return err
	}
	if _, err := fs.CopyFile(filepath.Join(boot.target, kernelRel), filepath.Join(dir, "vmlinuz")); err != nil {
		return fmt.Errorf("copy %s: %w", kernelRel, err)
	}
	if _, err := fs.CopyFile(filepath.Join(boot.target, initramfsRel), filepath.Join(dir, "initramfs")); err != nil {
		return fmt.Errorf("copy %s: %w", initramfsRel, err)
	}

	uuid, err := fs.ReadFile("/sys/class/block/" + loopOf(rootPart) + "/" + rootPart + "/uuid")
	if err != nil {
		return fmt.Errorf("root partuuid of %s: %w", rootPart, err)
	}
	rootFlags, err := mountedFSType(root.target, fs)
	if err != nil {
		return err
	}
	opath, osid, commit, err := ostreePath(filepath.Join(root.target, "ostree"), fs)
	if err != nil {
		return err
	}

	etc := filepath.Join(root.target, "ostree", "deploy", osid, "deploy", commit+".0", "etc")
	if err := writeIdentity(fs, etc); err != nil {
		return err
	}
	if err := maskResolvedUnit(etc); err != nil {
		return err
	}
	return saveMeta(dir, ver, "UUID="+strings.TrimSpace(string(uuid)), rootFlags, opath)
}

// BakeNetworkd writes the per-VM systemd-networkd config into the deployment
// /etc overlay of a template copy at diskPath: loop-attach, mount the root
// partition rw (via the FS mounter), write 10-bolt.network, umount, detach.
// An rw mount failure on a shared template image risks a dirty journal, so
// the error points at xfs_repair -L.
func BakeNetworkd(ctx context.Context, fs FS, diskPath, ip, gw, mac, dns string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	loop, err := fs.LoopAttach(diskPath)
	if err != nil {
		return fmt.Errorf("attach %s: %w", diskPath, err)
	}
	defer func() { _ = fs.LoopDetach(loop) }()

	root, _, _, _, err := findParts(ctx, fs, loop, false, false,
		"mount rw (if the filesystem is damaged run xfs_repair -L on a copy of the image and retry)")
	if err != nil {
		return err
	}
	defer func() {
		_ = fs.Umount(root.target)
		root.cleanup()
	}()

	_, osid, commit, err := ostreePath(filepath.Join(root.target, "ostree"), fs)
	if err != nil {
		return err
	}
	dir := filepath.Join(root.target, "ostree", "deploy", osid, "deploy", commit+".0", "etc", "systemd", "network")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return fs.WriteFile(filepath.Join(dir, "10-bolt.network"), []byte(NetworkdFile(ip, gw, mac, dns)), 0o644)
}
