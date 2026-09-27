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

// NewSSHRunner returns the production Runner: file ops upload into the VM
// user's home over sftp. host is the resolved remote host; the ssh alias is
// resolved by Provision before the runner is called.
func NewSSHRunner(home string) Runner { return sshRunner{home: home} }

type sshRunner struct{ home string }

func (s sshRunner) Run(ctx context.Context, host string, ops []Op) error {
	var files []Op
	for _, op := range ops {
		switch op.Kind {
		case KindFile:
			files = append(files, op)
		default:
			return fmt.Errorf("unknown op kind %q", op.Kind)
		}
	}
	if len(files) == 0 {
		return nil
	}
	c, err := sshx.Connect(ctx, s.home, host, vm.SSHUser)
	if err != nil {
		return fmt.Errorf("connect %s: %w", host, err)
	}
	defer func() { _ = c.Close() }()
	if err := uploadFiles(c, files); err != nil {
		return fmt.Errorf("upload dotfiles: %w", err)
	}
	return nil
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
