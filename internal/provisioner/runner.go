package provisioner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/jcpowermac/qlvm/internal/sshx"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// vmHomeDir is the fixed home of the user every VM bakes in (vm.SSHUser).
const vmHomeDir = "/home/user"

// NewSSHRunner returns the production Runner: pkg ops install over the
// remote shell with `sudo dnf install -y`; file ops upload into the VM
// user's home over sftp. host is the resolved remote host; the ssh alias is
// resolved by Provision before the runner is called.
func NewSSHRunner(home string) Runner { return sshRunner{home: home} }

type sshRunner struct{ home string }

func (s sshRunner) Run(ctx context.Context, host string, ops []Op) error {
	var pkgs []string
	var files []Op
	for _, op := range ops {
		switch op.Kind {
		case KindPkg:
			pkgs = append(pkgs, op.Pkgs...)
		case KindFile:
			files = append(files, op)
		default:
			return fmt.Errorf("unknown op kind %q", op.Kind)
		}
	}
	if len(pkgs) == 0 && len(files) == 0 {
		return nil
	}
	cmd, err := dnfInstallCmd(pkgs)
	if err != nil {
		return err
	}
	c, err := sshx.Connect(ctx, s.home, host, vm.SSHUser)
	if err != nil {
		return fmt.Errorf("connect %s: %w", host, err)
	}
	defer func() { _ = c.Close() }()
	if cmd != "" {
		if _, err := sshx.Run(ctx, c, cmd); err != nil {
			return fmt.Errorf("install packages: %w", err)
		}
	}
	if err := uploadFiles(c, files); err != nil {
		return fmt.Errorf("upload dotfiles: %w", err)
	}
	return nil
}

// dnfInstallCmd builds the remote dnf command. Package names are
// whitelisted to rpm-safe tokens, which makes shell quoting unnecessary;
// anything else is an error. An empty list yields an empty command.
func dnfInstallCmd(pkgs []string) (string, error) {
	if len(pkgs) == 0 {
		return "", nil
	}
	args := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		if !pkgNameOK(p) {
			return "", fmt.Errorf("unsafe package name %q", p)
		}
		args = append(args, p)
	}
	return "sudo dnf install -y " + strings.Join(args, " "), nil
}

// pkgNameOK reports whether s is a safe rpm package-name token.
func pkgNameOK(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r == '.' || r == '_' || r == '-' || r == ':':
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

// safeRemotePath maps a dotfile's home-relative path to its absolute
// remote path, rejecting absolute paths and anything escaping the home
// dir (a "." element is harmless; only ".." can escape).
func safeRemotePath(rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", false
		}
	}
	return vmHomeDir + "/" + filepath.ToSlash(rel), true
}

// uploadFiles writes each dotfile under vmHomeDir over sftp, creating
// parent dirs and normalizing the mode to 0644. Paths are rejected unless
// they are clean home-relative paths (sftp is not a shell, but a stray
// absolute or .. path would still escape the home dir).
func uploadFiles(c *ssh.Client, files []Op) error {
	client, err := sftp.NewClient(c)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	for _, f := range files {
		remote, ok := safeRemotePath(f.Path)
		if !ok {
			return fmt.Errorf("unsafe dotfile path %q", f.Path)
		}
		if err := client.MkdirAll(filepath.Dir(remote)); err != nil { // server umask gives 0755
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(remote), err)
		}
		rf, err := client.Create(remote)
		if err != nil {
			return err
		}
		if _, err := rf.Write(f.Data); err != nil {
			_ = rf.Close()
			return err
		}
		if err := rf.Close(); err != nil {
			return err
		}
		if err := client.Chmod(remote, 0o644); err != nil {
			return err
		}
	}
	return nil
}
