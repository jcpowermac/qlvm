// Package sshx is qlvm's Go SSH client: it resolves ssh-config Host
// aliases, connects to auto-provisioned VMs (ssh-agent then ~/.ssh
// identity files, no host-key check — the VM is disposable), and runs
// remote commands or fetches files without a local ssh process.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// Resolve maps an ssh host alias to its HostName/User from home's
// ~/.ssh/config. Missing block is an error; missing User falls back to
// vm.SSHUser.
func Resolve(host, home string) (string, string, error) {
	hostname, user, err := vm.SSHEntry(home, host)
	if err != nil {
		return "", "", err
	}
	if user == "" {
		user = vm.SSHUser
	}
	return hostname, user, nil
}

// Connect dials host: ssh-agent keys first (SSH_AUTH_SOCK), then
// ~/.ssh/id_ed25519, id_ecdsa, id_rsa. Host-key checking is skipped
// (InsecureIgnoreHostKey) — VMs are auto-provisioned with a fresh key,
// the Go equivalent of the old bash helper's StrictHostKeyChecking no.
func Connect(ctx context.Context, home, host, user string) (*ssh.Client, error) {
	signers, err := signersFor(home)
	if err != nil {
		return nil, err
	}
	if len(signers) == 0 {
		return nil, errors.New("no ssh auth: set SSH_AUTH_SOCK or add ~/.ssh/id_ed25519")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- VMs are auto-provisioned with a fresh key; equivalent of StrictHostKeyChecking no
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh %s@%s: %w", user, host, err)
	}
	return c, nil
}

// signersFor collects ssh-agent signers (if SSH_AUTH_SOCK is set) followed
// by the standard ~/.ssh identity files that exist.
func signersFor(home string) ([]ssh.Signer, error) {
	var out []ssh.Signer
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock) // #nosec G704 -- user's ssh-agent socket, not a network target
		if err != nil {
			return nil, fmt.Errorf("ssh-agent %s: %w", sock, err)
		}
		keys, err := agent.NewClient(conn).Signers()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ssh-agent: %w", err)
		}
		out = append(out, keys...)
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", name)) // #nosec G304 -- standard identity path under the caller's home
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// WaitForSSH retries dial until it succeeds, maxTries attempts with
// interval between them; returns the last dial error when exhausted.
func WaitForSSH(ctx context.Context, dial func() (*ssh.Client, error), maxTries int, interval time.Duration) error {
	var err error
	for i := 0; i < maxTries; i++ {
		var c *ssh.Client
		c, err = dial()
		if err == nil {
			if c != nil { // nil client = fake in tests
				_ = c.Close()
			}
			return nil
		}
		if i+1 == maxTries {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return err
}

// Run executes cmd on the remote host and returns its combined output.
func Run(ctx context.Context, c *ssh.Client, cmd string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	var out bytes.Buffer
	s.Stdout = &out
	s.Stderr = &out
	if err := s.Run(cmd); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// FetchFile streams the remote file back as a pipe over `cat`. The
// reader's EOF error (or Close) surfaces the remote command's failure
// instead of silently truncating.
func FetchFile(ctx context.Context, c *ssh.Client, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	s.Stdout = pw
	if err := s.Start("cat -- '" + strings.ReplaceAll(path, "'", `'\''`) + "'"); err != nil {
		_ = s.Close()
		_ = pw.Close()
		return nil, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- s.Wait()
		_ = pw.Close()
	}()
	return &fetchRead{pr: pr, wait: waitCh, sess: s}, nil
}

type fetchRead struct {
	pr   *io.PipeReader
	wait chan error
	sess *ssh.Session
}

func (f *fetchRead) Read(p []byte) (int, error) {
	n, err := f.pr.Read(p)
	if err == io.EOF {
		if werr := <-f.wait; werr != nil {
			err = fmt.Errorf("remote cat: %w", werr)
		}
	}
	return n, err
}

func (f *fetchRead) Close() error {
	err := f.pr.Close()
	_ = f.sess.Close()
	return err
}

// KernelVersion extracts the version-release from
// `rpm -q kernel-core --last` output, e.g.
// "kernel-core-6.11.9-300.fc44.x86_64" -> "6.11.9-300.fc44".
func KernelVersion(output string) (string, error) {
	line := strings.TrimSpace(output)
	i := strings.Index(line, "kernel-core-")
	if i < 0 {
		return "", fmt.Errorf("no kernel-core package in %q", line)
	}
	ver := line[i+len("kernel-core-"):]
	if j := strings.LastIndex(ver, "."); j > 0 {
		ver = ver[:j] // drop the .<arch> suffix
	}
	if ver == "" {
		return "", fmt.Errorf("empty kernel version in %q", line)
	}
	return ver, nil
}
