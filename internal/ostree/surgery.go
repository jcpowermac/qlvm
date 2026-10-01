// Package ostree bakes bootc ostree templates (Task 8): loop-attach the
// adopted raw image, locate the ostree root and /boot partitions, copy the
// boot kernel + initramfs, write the VM identity and the spec §6.1 headless
// units into the deployment /etc overlay, and persist the enriched template
// META. BakeMounts adds the per-VM 9p .mount units on a template copy.
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
	"golang.org/x/sys/unix"
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

// MountPrefix prefixes the bake scratch mount dirs under the system temp
// dir (cleared by qlvm template create's reap step).
const MountPrefix = "qlvm-ostree-"

func mountTarget() (string, func(), error) {
	dir, err := os.MkdirTemp("", MountPrefix) // #nosec G304 -- internal mount point under the system temp dir
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// chownForUser hands a baked path to the VM user. Unprivileged unit tests
// cannot chown (EPERM); the real bake always runs as root via sudo qlvm.
func chownForUser(path string, uid int) error {
	if os.Geteuid() != 0 {
		return nil
	}
	// The Lstat guard no-ops for virtual FS fakes (paths exist only on real
	// loop-mounted trees), mirroring setSELinuxContext.
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	return os.Chown(path, uid, uid) // #nosec G306 -- fixed uid for an internal path
}

// setSELinuxContext stamps security.selinux on a baked path. dom0 creates
// these files with no LSM in play, so the guest's enforcing policy sees
// them as unlabeled — sshd in particular will not read an unlabeled
// authorized_keys ("key is not allowed", no AVC visible when kauditd
// coalesces). Values mirror the Fedora policy for the standard paths. The
// Lstat guard no-ops for virtual FS fakes (paths only exist on real
// loop-mounted trees).
func setSELinuxContext(path, ctx string) error {
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	return unix.Lsetxattr(path, "security.selinux", append([]byte(ctx), 0), 0)
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

// findParts mounts each partition (ro as given, fstype from the caller),
// classifies it, and returns the kept mounts. Non-matching partitions are
// umounted as they are probed; when wantBoot is false the scan stops once
// the root is found and the /boot probe mount is released immediately
// (returning a zero boot partMount) — a caller that wants /boot sets
// wantBoot true. Live H4 defect (2026-09-30): the per-VM bakes
// (wantBoot=false) left the /boot probe mounted rw with its loop still
// attached; because the /boot UUID is shared with the template (the image
// fstab pins it), the next create then failed to mount its own /boot with
// EINVAL (duplicate XFS UUID). Every mount is umounted before a failure is
// returned and before a success return.
func findParts(ctx context.Context, fs FS, loop string, ro, wantBoot bool, fstype, tag string) (root, boot partMount, rootPart, bootPart string, err error) {
	if err := ctx.Err(); err != nil {
		return partMount{}, partMount{}, "", "", err
	}
	var kept []partMount
	var mountErr error
	release := func() {
		for _, m := range kept {
			_ = fs.Umount(m.target)
			m.cleanup()
		}
		kept = nil
	}
	for _, part := range fs.Partitions(ctx, loop) {
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
		// The disk carries partitions the fstype cannot mount (EFI is
		// vfat, swap is not a filesystem): skip them like the legacy
		// probe did, remembering the last error for the no-root report.
		if merr := fs.Mount(dev, target, fstype, ro); merr != nil {
			mountErr = fmt.Errorf("%s %s: %w", tag, dev, merr)
			_ = fs.Umount(target)
			cleanup()
			continue
		}
		switch probePart(target, fs) {
		case "root":
			root, rootPart = partMount{target, cleanup}, part
			kept = append(kept, root)
		case "boot":
			if wantBoot {
				boot, bootPart = partMount{target, cleanup}, part
				kept = append(kept, boot)
			} else {
				// Not wanted: release the probe mount now, not via release()
				// (kept is only for mounts the caller receives).
				_ = fs.Umount(target)
				cleanup()
			}
		default:
			_ = fs.Umount(target)
			cleanup()
		}
		if rootPart != "" && (!wantBoot || bootPart != "") {
			return root, boot, rootPart, bootPart, nil
		}
	}
	release()
	if mountErr != nil {
		return partMount{}, partMount{}, "", "", fmt.Errorf("%s: template disk %s has no ostree root (and /boot) partition pair (last mount failure: %v)", tag, loop, mountErr)
	}
	return partMount{}, partMount{}, "", "", fmt.Errorf("%s: template disk %s has no ostree root (and /boot) partition pair", tag, loop)
}

// deploymentDir returns the single *.0 deployment entry under
// ostree/deploy/<osid>/deploy/.
func deploymentDir(ostreeDir, osid string, fs FS) (string, error) {
	entries, err := fs.ReadDir(filepath.Join(ostreeDir, "deploy", osid, "deploy"))
	if err != nil {
		return "", err
	}
	var dep string
	for _, e := range entries {
		if strings.HasSuffix(e, ".0") {
			if dep != "" {
				return "", fmt.Errorf("%s: multiple deployments %v", ostreeDir, entries)
			}
			dep = e
		}
	}
	if dep == "" {
		return "", fmt.Errorf("%s/deploy/%s/deploy: no *.0 deployment", ostreeDir, osid)
	}
	return dep, nil
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
	// Bootc images name it initramfs-<ver>.img; match from the listing
	// rather than assume the suffix.
	var initRel string
	for _, f := range files {
		if strings.HasPrefix(f, "initramfs-"+ver) {
			initRel = filepath.Join("ostree", osidDirs[0], f)
		}
	}
	if initRel == "" {
		return "", "", "", fmt.Errorf("%s: no initramfs-%s*", osidDirs[0], ver)
	}
	return ver, kernelRel, initRel, nil
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

// maskUnit masks a unit in the deployment /etc/systemd/system overlay: a
// symlink to /dev/null. etcDir is under a real loop-mount mountpoint, so
// direct os use is intentional: the FS seam carries the loop/mount surface
// and overlay file writes, but there is no approved Symlink method
// (supervisor: no other FS methods).
func maskUnit(etcDir, unit string) error {
	link := filepath.Join(etcDir, unit)
	_ = os.Remove(link)
	return os.Symlink("/dev/null", link)
}

// rundirUnit is the spec §6.1 headless unit that creates /run/user/1000 at
// boot: the VM user has no logind session, so the runtime dir must exist
// before anything runs as the user.
const rundirUnit = "[Unit]\n" +
	"Description=Create /run/user/1000 for the VM user\n" +
	"DefaultDependencies=no\n" +
	"After=local-fs.target\n" +
	"Before=multi-user.target\n" +
	"\n" +
	"[Service]\n" +
	"Type=oneshot\n" +
	"ExecStart=/usr/bin/mkdir -m 0700 -p /run/user/1000\n" +
	"ExecStart=/usr/bin/chown 1000:1000 /run/user/1000\n" +
	"RemainAfterExit=yes\n" +
	"\n" +
	"[Install]\n" +
	"WantedBy=multi-user.target\n"

// bakeHeadlessUnits bakes the spec §6.1 headless units into the deployment
// /etc overlay: the bolt-rundir unit, a NetworkManager mask, and enables for
// bolt-rundir + systemd-networkd. On stock bootc images NetworkManager is
// enabled and would own the NIC before the baked 10-bolt.network could
// apply, so networkd must be the unit that is enabled. (The resolved mask is
// kept separately: networkd owns DNS.)
func bakeHeadlessUnits(fs FS, etc string) error {
	dir := filepath.Join(etc, "systemd", "system")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	unit := filepath.Join(dir, "bolt-rundir.service")
	if err := fs.WriteFile(unit, []byte(rundirUnit), 0o644); err != nil {
		return err
	}
	// Enables: the wants symlinks systemd `enable` creates for units that
	// live under /etc (networkd's unit file is vendor-provided).
	wants := filepath.Join(dir, "multi-user.target.wants")
	if err := os.MkdirAll(wants, 0o750); err != nil {
		return err
	}
	for src, name := range map[string]string{
		"/etc/systemd/system/bolt-rundir.service":          "bolt-rundir.service",
		"/usr/lib/systemd/system/systemd-networkd.service": "systemd-networkd.service",
		// qlvm vm run execs `waypipe ssh`: the guest needs a server.
		"/usr/lib/systemd/system/sshd.service": "sshd.service",
	} {
		link := filepath.Join(wants, name)
		_ = os.Remove(link)
		if err := os.Symlink(src, link); err != nil {
			return err
		}
	}
	return maskUnit(dir, "NetworkManager.service")
}

// SharedMount is one 9p share to bake into the VM guest as a systemd .mount
// unit: Where is the guest mount point (absolute), What is the 9p tag
// (libxl mrtag) the domain exports.
type SharedMount struct {
	Where string
	What  string
}

// MountUnitName derives the .mount unit basename (no suffix) for the i-th
// SharedMount from its guest path, systemd-style: /var/lib/qvm/data ->
// var-lib-qvm-data-0.
func MountUnitName(m SharedMount, i int) string {
	name := strings.TrimPrefix(m.Where, "/")
	return strings.ReplaceAll(name, "/", "-") + "-" + strconv.Itoa(i)
}

// SharedMountUnit renders the .mount unit for a SharedMount: 9p with
// trans=virtio (the xen9pfsd backend), gated on the network (_netdev) since
// the share is reachable only through the VM's own NIC.
func SharedMountUnit(m SharedMount) string {
	return "[Unit]\n" +
		"Description=qlvm shared mount " + m.Where + "\n" +
		"_netdev=true\n" +
		"After=systemd-networkd.service\n" +
		"\n" +
		"[Mount]\n" +
		"What=" + m.What + "\n" +
		"Where=" + m.Where + "\n" +
		"Type=9p\n" +
		"Options=trans=virtio\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
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
// the DirFor dir name (see dirDigest). Image is inherited from any
// pre-existing META in dir — including a stale-digest META left by the
// superseded build it replaces — because the DirFor name cannot carry it.
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
// EnsureOpts.Bake): loop-attach, probe the partitions read-only to find the
// ostree root and /boot, copy vmlinuz+initramfs into dir, remount the root
// partition rw for the deployment-tree writes (VM identity + resolved mask
// + spec §6.1 headless units + SSH authorized_keys), and persist the
// enriched template META. fstype and sshAuthKeys (concatenated public keys,
// sshd(8) authorized_keys format) are supplied by the wiring layer
// (image-builder's --bootc-default-fs; the caller's ssh identities).
// Every loop device and mount is released on every exit path.
func BakeTemplate(ctx context.Context, fs FS, dir, fstype, sshAuthKeys string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fstype == "" {
		return fmt.Errorf("fstype required (wiring layer supplies image-builder's --bootc-default-fs)")
	}
	if sshAuthKeys == "" {
		return fmt.Errorf("sshAuthKeys required: a VM without SSH identities is unrunnable (qlvm vm run)")
	}
	disk := filepath.Join(dir, "template.raw")
	loop, err := fs.LoopAttach(disk)
	if err != nil {
		return fmt.Errorf("attach %s: %w", disk, err)
	}
	defer func() { _ = fs.LoopDetach(loop) }()

	root, boot, rootPart, _, err := findParts(ctx, fs, loop, true, true, fstype, "mount")
	if err != nil {
		return err
	}

	// Probes are read-only; the deployment-tree writes need the root rw. Umount
	// the ro probe and remount the same partition rw (review ruling).
	_ = fs.Umount(root.target)
	root.cleanup()

	rwTarget, rwCleanup, err := mountTarget()
	if err != nil {
		_ = fs.Umount(boot.target)
		boot.cleanup()
		return err
	}
	if err := fs.Mount("/dev/"+rootPart, rwTarget, fstype, false); err != nil {
		rwCleanup()
		_ = fs.Umount(boot.target)
		boot.cleanup()
		return fmt.Errorf("mount rw /dev/%s: %w", rootPart, err)
	}
	defer func() {
		_ = fs.Umount(rwTarget)
		rwCleanup()
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

	partuuid, err := fs.PartUUID(filepath.Join(dir, "template.raw"), rootPart)
	if err != nil {
		return fmt.Errorf("root partuuid of %s: %w", rootPart, err)
	}
	rootFlags := ""
	if fstype == "btrfs" {
		rootFlags = "subvol=root" // template.Template contract
	}
	opath, osid, _, err := ostreePath(filepath.Join(rwTarget, "ostree"), fs)
	if err != nil {
		return err
	}

	// The deployment commit is resolved from /ostree/deploy, not from the
	// boot dir: bootc images keep the /boot (and boot.N) contents of the
	// base commit while the deployment tree carries the final commit.
	dep, err := deploymentDir(filepath.Join(rwTarget, "ostree"), osid, fs)
	if err != nil {
		return err
	}
	etc := filepath.Join(rwTarget, "ostree", "deploy", osid, "deploy", dep, "etc")
	if err := writeIdentity(fs, etc); err != nil {
		return err
	}
	systemdDir := filepath.Join(etc, "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o750); err != nil {
		return err
	}
	// The per-VM 10-bolt.network owns the static address and DNS, so
	// resolved stays masked.
	if err := maskUnit(systemdDir, "systemd-resolved.service"); err != nil {
		return err
	}
	if err := bakeHeadlessUnits(fs, etc); err != nil {
		return err
	}
	// SSH identity for `qlvm vm run` (waypipe ssh) and sshx: authorized_keys in
	// the VM user's home. The writable /var is the osid-level shared var
	// (bootc layout: <root>/ostree/deploy/<osid>/var carries the deployment
	// var/), so home lives there and survives into every reflinked VM.
	// Root-owned 0755/0644 passes sshd StrictModes (owner user-or-root, no
	// group/other write) without a chown on the loop-mounted tree.
	home := filepath.Join(rwTarget, "ostree", "deploy", osid, "var", "home", defaultVMUser)
	sshDir := filepath.Join(home, ".ssh")
	// Through the FS, not os: a raw MkdirAll materializes the dir under the
	// fake's real temp mount target, which defeats the Lstat guards in
	// chownForUser/setSELinuxContext (raw chown/setxattr then run against a
	// half-real tree and fail on non-root/non-SELinux hosts, e.g. CI).
	if err := fs.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	authKeys := filepath.Join(sshDir, "authorized_keys")
	if err := fs.WriteFile(authKeys, []byte(sshAuthKeys), 0o600); err != nil {
		return err
	}
	// The guest user must own its home chain: sshd StrictModes accepts
	// root-owned, but GUI apps write profiles under ~/. sshd reads
	// authorized_keys as root (the privsep monitor), so 0600-user is fine.
	for _, p := range []string{home, sshDir, authKeys} {
		if err := chownForUser(p, defaultVMUID); err != nil {
			return err
		}
	}
	for path, ctx := range map[string]string{
		home:     "system_u:object_r:user_home_dir_t:s0",
		sshDir:   "system_u:object_r:ssh_home_t:s0",
		authKeys: "system_u:object_r:ssh_home_t:s0",
	} {
		if err := setSELinuxContext(path, ctx); err != nil {
			return err
		}
	}
	// PARTUUID (not the fs UUID): the value is the GPT partition GUID,
	// which the kernel resolves via /dev/disk/by-partuuid only under the
	// PARTUUID= prefix; root=UUID= looks in by-uuid (fs UUIDs) and the
	// guest drops to the dracut emergency shell.
	return saveMeta(dir, ver, "PARTUUID="+partuuid, rootFlags, opath)
}

// BakeNetworkd writes the per-VM systemd-networkd config into the deployment
// /etc overlay of a template copy at diskPath: loop-attach, mount the root
// partition rw (via the FS mounter, fstype from the wiring layer), write
// 10-bolt.network, umount, detach. An rw mount failure on a shared template
// image risks a dirty journal, so the error points at xfs_repair -L.
// rootRWMount loop-attaches diskPath and mounts its root partition rw,
// returning the mount target and a cleanup (umount + partition release +
// detach).
func rootRWMount(ctx context.Context, fs FS, diskPath, fstype string) (string, func(), error) {
	loop, err := fs.LoopAttach(diskPath)
	if err != nil {
		return "", nil, fmt.Errorf("attach %s: %w", diskPath, err)
	}
	root, _, _, _, err := findParts(ctx, fs, loop, false, false, fstype,
		"mount rw (if the filesystem is damaged run xfs_repair -L on a copy of the image and retry)")
	cleanup := func() {
		_ = fs.Umount(root.target)
		root.cleanup()
		_ = fs.LoopDetach(loop)
	}
	if err != nil {
		// On error findParts returned a zero root (its probe mounts are its
		// own to release); only the loop is ours to detach here.
		_ = fs.LoopDetach(loop)
		return "", nil, err
	}
	return root.target, cleanup, nil
}

// deploymentEtc returns the deployment /etc overlay dir under a mounted
// root target.
func deploymentEtc(ctx context.Context, fs FS, rootTarget string) (string, error) {
	_, osid, _, err := ostreePath(filepath.Join(rootTarget, "ostree"), fs)
	if err != nil {
		return "", err
	}
	dep, err := deploymentDir(filepath.Join(rootTarget, "ostree"), osid, fs)
	if err != nil {
		return "", err
	}
	return filepath.Join(rootTarget, "ostree", "deploy", osid, "deploy", dep, "etc"), nil
}

func BakeNetworkd(ctx context.Context, fs FS, diskPath, fstype, ip, gw, mac, dns string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fstype == "" {
		return fmt.Errorf("fstype required (wiring layer supplies image-builder's --bootc-default-fs)")
	}
	root, cleanup, err := rootRWMount(ctx, fs, diskPath, fstype)
	if err != nil {
		return err
	}
	defer cleanup()

	etc, err := deploymentEtc(ctx, fs, root)
	if err != nil {
		return err
	}
	dir := filepath.Join(etc, "systemd", "network")
	// root is a real loop-mount mountpoint: direct os.MkdirAll is
	// intentional (directory creation has no FS seam method).
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := fs.WriteFile(filepath.Join(dir, "10-bolt.network"), []byte(NetworkdFile(ip, gw, mac, dns)), 0o644); err != nil {
		return err
	}
	// DNS: the distro /etc/resolv.conf is a dangling symlink to the
	// systemd-resolved stub (resolved is masked), and this systemd's
	// networkd defers resolv.conf writes to /run/systemd/resolve.hook
	// instead of managing the symlink — so bake a real file. networkd
	// sees a regular file ("foreign") and leaves it alone; the DNS= in
	// the .network unit stays as documentation for LLMNR-less setups.
	resolv := filepath.Join(etc, "resolv.conf")
	_ = os.Remove(resolv) // replace the dangling symlink (ENOENT ok on fakes)
	return fs.WriteFile(resolv, []byte("nameserver "+dns+"\n"), 0o644)
}

// ControlScript is the guest-side waypipe control relay, baked to
// /etc/qvm/qvm-ctl. One instance per dom0 connection (systemd oneshot):
// verify the per-VM token, start the ncat unix->TCP bridge back to the
// dom0 data port, then run the app under a one-shot waypipe server.
const ControlScript = `#!/bin/sh
# qlvm waypipe control relay: stdin is the dom0 control connection
# (activated by qvm-ctl.socket on tcp:4711). Frame:
#   <token>\n<data-port>\n<dom0-ip>\n<exec>\n
IFS= read -r tok || exit 1
want=$(cat /etc/qvm/waypipe-token 2>/dev/null) || exit 1
[ -n "$want" ] || exit 1
[ "$tok" = "$want" ] || exit 1
IFS= read -r port || exit 1
IFS= read -r dom0 || exit 1
IFS= read -r exec || exit 1
[ -n "$exec" ] || exec="bash -l"
sock=/run/user/1000/qlvm-waypipe-$$.sock
ncat -lkU "$sock" --sh-exec "ncat -w 120 $dom0 $port" &
bridge=$!
trap 'kill $bridge 2>/dev/null; rm -f "$sock"' EXIT
# wait for the bridge socket so waypipe's first dial does not race it
i=0
while [ ! -S "$sock" ] && [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done
/usr/bin/waypipe -o -n -s "$sock" --display qvm server -- $exec
rc=$?
kill $bridge 2>/dev/null
wait $bridge 2>/dev/null
rm -f "$sock"
exit $rc
`

// ControlSocketUnit listens on all interfaces: the guest is single-homed
// on the netfront, and the token (not the network) is the auth boundary.
const ControlSocketUnit = `[Unit]
Description=qlvm waypipe control socket (dom0 app projection)

[Socket]
ListenStream=4711

[Install]
WantedBy=sockets.target
`

// ControlServiceUnit runs one relay per connection as the baked VM user;
// bolt-rundir owns /run/user/1000 in these headless guests. /bin/sh <file>
// (not direct exec) so SELinux only needs read on the etc_t script.
func ControlServiceUnit(user string) string {
	return fmt.Sprintf(`[Unit]
Description=qlvm waypipe app server (one instance per dom0 run)
Requires=bolt-rundir.service
After=bolt-rundir.service

[Service]
Type=oneshot
User=%s
Environment=HOME=/var/home/%s
Environment=XDG_RUNTIME_DIR=/run/user/1000
ExecStart=/bin/sh /etc/qvm/qvm-ctl
`, user, user)
}

// BakeControl bakes the per-VM waypipe control channel: the qvm-ctl relay
// script + token (0600), the systemd socket/service pair, and the socket
// enablement symlink. Runs at vm create (same rw mount path as
// BakeNetworkd).
func BakeControl(ctx context.Context, fs FS, diskPath, fstype, user, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fstype == "" {
		return fmt.Errorf("fstype required (wiring layer supplies image-builder's --bootc-default-fs)")
	}
	root, cleanup, err := rootRWMount(ctx, fs, diskPath, fstype)
	if err != nil {
		return err
	}
	defer cleanup()

	etc, err := deploymentEtc(ctx, fs, root)
	if err != nil {
		return err
	}
	qvm := filepath.Join(etc, "qvm")
	if err := os.MkdirAll(qvm, 0o750); err != nil {
		return err
	}
	if err := fs.WriteFile(filepath.Join(qvm, "qvm-ctl"), []byte(ControlScript), 0o755); err != nil {
		return err
	}
	if err := fs.WriteFile(filepath.Join(qvm, "waypipe-token"), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	sysd := filepath.Join(etc, "systemd", "system")
	if err := os.MkdirAll(sysd, 0o750); err != nil {
		return err
	}
	if err := fs.WriteFile(filepath.Join(sysd, "qvm-ctl.socket"), []byte(ControlSocketUnit), 0o644); err != nil {
		return err
	}
	if err := fs.WriteFile(filepath.Join(sysd, "qvm-ctl.service"), []byte(ControlServiceUnit(user)), 0o644); err != nil {
		return err
	}
	wants := filepath.Join(sysd, "sockets.target.wants")
	if err := os.MkdirAll(wants, 0o750); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(wants, "qvm-ctl.socket"))
	return os.Symlink("../qvm-ctl.socket", filepath.Join(wants, "qvm-ctl.socket"))
}

// BakeMounts bakes one per-share systemd .mount unit (plus its enablement
// symlink) into the deployment /etc overlay of a template copy at diskPath:
// loop-attach, mount the root partition rw (via the FS mounter, fstype from
// the wiring layer), write the units, umount, detach. The units are the
// guest-side contract for the 9p shares DomainConfig exports.
func BakeMounts(ctx context.Context, fs FS, diskPath, fstype string, mounts []SharedMount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fstype == "" {
		return fmt.Errorf("fstype required (wiring layer supplies image-builder's --bootc-default-fs)")
	}
	loop, err := fs.LoopAttach(diskPath)
	if err != nil {
		return fmt.Errorf("attach %s: %w", diskPath, err)
	}
	defer func() { _ = fs.LoopDetach(loop) }()

	root, _, _, _, err := findParts(ctx, fs, loop, false, false, fstype,
		"mount rw (if the filesystem is damaged run xfs_repair -L on a copy of the image and retry)")
	if err != nil {
		return err
	}
	defer func() {
		_ = fs.Umount(root.target)
		root.cleanup()
	}()

	_, osid, _, err := ostreePath(filepath.Join(root.target, "ostree"), fs)
	if err != nil {
		return err
	}
	dep, err := deploymentDir(filepath.Join(root.target, "ostree"), osid, fs)
	if err != nil {
		return err
	}
	systemdDir := filepath.Join(root.target, "ostree", "deploy", osid, "deploy", dep, "etc", "systemd", "system")
	wants := filepath.Join(systemdDir, "multi-user.target.wants")
	// root.target is a real loop-mount mountpoint: direct os use is
	// intentional (no FS seam method for dirs/symlinks, as in BakeNetworkd).
	if err := os.MkdirAll(wants, 0o750); err != nil {
		return err
	}
	for i, m := range mounts {
		unit := MountUnitName(m, i) + ".mount"
		if err := fs.WriteFile(filepath.Join(systemdDir, unit), []byte(SharedMountUnit(m)), 0o644); err != nil {
			return err
		}
		link := filepath.Join(wants, unit)
		_ = os.Remove(link)
		if err := os.Symlink("/etc/systemd/system/"+unit, link); err != nil {
			return err
		}
	}
	return nil
}
