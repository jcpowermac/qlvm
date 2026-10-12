# Guest SSH / `qlvm vm run` Pitfalls

- **`qlvm vm run` is a user-session command** (waypipe needs the desktop's
  WAYLAND_DISPLAY; sudo strips it and the guard says so). Therefore
  `vms/<name>/` is 0755 and `meta.yaml` 0644 (LoadMeta as the user);
  `disk.img` stays 0600. Run connects `user@<meta.IP>` directly —
  disposables have no `~/.ssh/config` entry by design — with
  StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null (VM generations
  reuse IPs and ship the image's host keys).
- **SSH identity is baked at template bake**: sshd.service enabled, and
  root's + SUDO_USER's `~/.ssh/id_{ed25519,ecdsa,rsa}.pub` written to
  `var/home/user/.ssh/authorized_keys` in the **osid shared var**
  (`ostree/deploy/<os>/var`, not the deployment's `var/`). Shadow `!` does
  NOT block pubkey auth. No SSH keys found = create hard-fails (a VM
  without SSH is unrunnable).
- **The guest runs SELinux Enforcing and bake-created files carry dom0's
  auto-stamped labels** (e.g. `var_t`), which confined guests reject —
  sshd logged "key is not allowed" with **no AVC line** (kauditd
  coalescing; console shows only "callbacks suppressed"). Diagnose from
  inside: `ls -Z`, `matchpathcon -V <path>` ("verified" = policy agrees).
  `setSELinuxContext` stamps `user_home_dir_t`/`ssh_home_t` on the home
  chain; stamp `security.selinux` on ANY baked path a confined daemon
  reads. dom0 setxattr works and persists.
- **DNS**: resolved+NM are masked and the distro `/etc/resolv.conf` is a
  dangling symlink to the resolved *stub*; this systemd's networkd defers
  resolv.conf writes to `/run/systemd/resolve.hook` (writes nothing, even
  retargeting the symlink to the uplink file — networkctl shows `DNS:`
  applied yet no file appears). `BakeNetworkd` therefore writes a real
  `/etc/resolv.conf` (`nameserver <dns>`); networkd sees "foreign" and
  leaves it alone.
- **A bake costs ~5 minutes** and only `qlvm template create` runs one
  (`vm create` only references): give `template create` a `timeout 600`+
  (killing it early leaves
  the image-builder podman container RUNNING as root — `podman ps -a` —
  and a template dir without `template.raw`). The dir name carries the
  digest from the build that created it — `ls templates/` before
  `rm -rf`/recreate, don't guess it from a manifest digest.
- **Never loop-mount a VM disk before the domain is gone from `xl list`**:
  ACPI `stop` can linger in `---s--` for minutes; a second rw mount of a
  live xfs is a corruption hazard. When the domain won't exit: `xl
  destroy`, verify, then mount.
