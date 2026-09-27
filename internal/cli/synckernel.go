package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// kernelSyncer is the ssh seam TestSyncKernelFlow fakes: a remote command
// plus file fetches against the running VM.
type kernelSyncer interface {
	Run(ctx context.Context, cmd string) (string, error)
	FetchFile(ctx context.Context, path string) (io.ReadCloser, error)
}

type sshKernelSyncer struct{ c *ssh.Client }

func (s sshKernelSyncer) Run(ctx context.Context, cmd string) (string, error) {
	return sshx.Run(ctx, s.c, cmd)
}

func (s sshKernelSyncer) FetchFile(ctx context.Context, path string) (io.ReadCloser, error) {
	return sshx.FetchFile(ctx, s.c, path)
}

// syncKernel copies the VM's current kernel and initramfs from its /boot
// into dir (the VM's template dir): each file lands under its exact fetched
// name and additionally as the unversioned vmlinuz/initramfs that the VM's
// domain config boots (vm/domcfg), so a restart picks the new kernel up.
// The template META's kernel_ver is updated to keep state honest.
func syncKernel(ctx context.Context, host kernelSyncer, dir string) (string, error) {
	raw, err := host.Run(ctx, "rpm -q kernel-core --last | head -1")
	if err != nil {
		return "", fmt.Errorf("query vm kernel: %w", err)
	}
	ver, err := sshx.KernelVersion(raw)
	if err != nil {
		return "", err
	}
	files := []struct{ remote, local, alias string }{
		{"/boot/vmlinuz-" + ver, "vmlinuz-" + ver, "vmlinuz"},
		{"/boot/initramfs-" + ver + ".img", "initramfs-" + ver + ".img", "initramfs"},
	}
	for _, f := range files {
		rc, err := host.FetchFile(ctx, f.remote)
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, err)
		}
		body, rerr := io.ReadAll(rc)
		cerr := rc.Close()
		if rerr != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, rerr)
		}
		if cerr != nil {
			return "", fmt.Errorf("fetch %s: %w", f.remote, cerr)
		}
		for _, name := range []string{f.local, f.alias} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
				return "", err
			}
		}
	}
	tpl, err := template.LoadMeta(dir)
	if err != nil {
		return "", err
	}
	tpl.KernelVer = ver
	if err := tpl.SaveMeta(dir); err != nil {
		return "", err
	}
	return ver, nil
}

func syncKernelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync-kernel <vm>",
		Short: "Copy the VM's current kernel + initramfs into its template dir",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]
			m, err := vm.LoadMeta(vmDirOf(name))
			if err != nil {
				return fmt.Errorf("sync-kernel %s: %w", name, err)
			}
			tpl, err := template.LoadByRef(installRoot, m.Image, m.Digest)
			if err != nil {
				return fmt.Errorf("sync-kernel %s: template: %w", name, err)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			// Direct to the VM's IP as the bake's fixed user: the VM just
			// came up with a fresh host key, and disposable VMs have no
			// ssh-config alias at all.
			c, err := sshx.Connect(ctx, home, m.IP, vm.SSHUser)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			ver, err := syncKernel(ctx, sshKernelSyncer{c: c}, tpl.Dir)
			if err != nil {
				return fmt.Errorf("sync-kernel %s: %w", name, err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "synced kernel %s to %s\n", ver, tpl.Dir)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restart to boot it: qlvm stop %s && qlvm start %s\n", name, name)
			return nil
		},
	}
}

func init() {
	NewRootCmd().AddCommand(syncKernelCmd())
}
