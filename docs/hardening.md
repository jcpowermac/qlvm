# dom0 Hardening

Guidance for shrinking the dom0 attack surface once you are confident in
the qlvm setup. None of this is done by `qlvm install` — it is
deliberate, manual, and destructive.

> **Warning:** removing packages uninstalls a full desktop and ~200 other
> packages. Do it from a console or a session that survives the removal —
> not from the desktop you are removing. Keep a known-good recovery path
> (a live USB or a second session) before you start.
>
> The dom0 is an ostree (atomic) system: dnf is disabled on it. Package
> changes go through `rpm-ostree`, which *stages* a new deployment that
> becomes active at the next boot. If you manage the dom0 image from a
> recipe, the durable home for these changes is the recipe itself (removals
> drop out of the recipe's package set, installs are added to it) and a
> rebuilt image — `rpm-ostree` here is the manual equivalent.

## Package removal

The dom0 image ships with a GNOME desktop, QEMU/libvirt, and printing
stacks that a Xen-only dom0 never uses. Removing them cuts the attack
surface (and the update surface) substantially. Xen packages are protected
by default; the usual invocation is a single staged transaction that
removes the GNOME session, QEMU/libvirt, and the printing stack:

```sh
sudo rpm-ostree remove \
    gnome-shell gnome-session gdm mutter gnome-control-center gnome-settings-daemon \
    gnome-initial-setup gnome-software gnome-system-monitor gnome-text-editor \
    nautilus baobab gnome-calculator gnome-calendar gnome-contacts gnome-disks \
    epiphany yelp \
    qemu-kvm qemu-system-x86-core qemu-common qemu-guest-agent \
    libvirt-daemon libvirt-daemon-kvm libvirt-client virt-manager \
    edk2-ovmf seabios-bin virglrenderer virtiofsd usbredir spice-server spice-vdagent \
    cups cups-browsed cups-client cups-filters gutenprint ghostscript
```

Tune the list to what you actually run: if a package is installed by a
package you still need, the dependency solver will keep or pull it back —
review the transaction summary it prints before it stages. Reboot to make
the removal active (`rpm-ostree status` shows the staged deployment until
then). If you use QEMU/libvirt alongside Xen (you should not, on a dom0),
skip those packages.

Also check for and repair broken Xen shims afterwards, e.g. a dangling
`/usr/libexec/xen/bin/qemu-system-*` symlink left behind by the QEMU
removal:

```sh
find /usr/libexec/xen -xtype l -ls   # list broken symlinks under the Xen tree
```

Point anything dangling at `/bin/false` (or remove it) so Xen's toolstack
never resolves a missing emulator.

## greetd: a minimal display manager

If you need a GUI login for dom0 sessions (e.g. for `qlvm run`, which
requires a Wayland session), replace GDM with the tiny `greetd` login
service plus a compositor of your choice:

```sh
sudo rpm-ostree install greetd greetd-selinux
sudo systemctl enable greetd
```

The install stages a new deployment — reboot to activate it (enablement
persists across the switch).

Minimal `/etc/greetd/config.toml` (Wayland greeter on VT1):

```toml
[terminal]
vt = 1

[default_session]
command = "your-compositor --config /etc/greetd/compositor-config"
user = "greetd"
```

with a compositor config that execs the greeter and exits the compositor
on logout, e.g. for a gtkgreet-based session:

```
exec "gtkgreet -l -s /usr/share/wayland-sessions; swaymsg exit"
```

Adjust the compositor/greeter combination to what you installed. Reboot
(or `systemctl restart greetd`) and log in on VT1.

## What this is not

- qlvm does not automate hardening; `install` reconciles only the planes
  it owns (OVS, OVN, firewalld, systemd-networkd, services, storage, the
  vif-ovn script).
- The `[firewall.egress]` section of `qlvm.toml` is the dom0 *egress*
  policy; this document covers *host* hardening (what runs on dom0 at
  all). Both apply, independently.
- Review `rpm-ostree` transaction output and your SELinux context for the
  greeter before rebooting; a broken login on the only console is your own
  fault, not the solver's.
