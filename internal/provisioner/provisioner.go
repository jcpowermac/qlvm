// Package provisioner syncs per-VM dotfiles from a provision dir: base/ then
// <vm>/, missing layers skipped. Ops are executed by a Runner; the shipped
// runner (NewSSHRunner) uploads dotfiles over sftp into the VM user's home.
//
// System packages are NOT provisioned: the VM root is an ostree deployment
// built from the bootc container image, and dnf is disabled on it. Extra
// system packages belong in the container image (extend the image, don't
// provision).
package provisioner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/jcpowermac/qlvm/internal/sshx"
)

// Op kinds executed by a Runner.
const (
	KindFile = "file" // Path/Data: dotfile to place under the VM user's home
)

// Op is one provisioning unit.
type Op struct {
	Kind string
	Path string // KindFile: relative to the VM user's home
	Data []byte // KindFile
}

// Runner executes ops on one VM. host is the resolved remote host
// (Provision resolves the ssh alias); implementations must treat an empty
// op list as a no-op. The seam is where a future config-management backend
// reattaches.
type Runner interface {
	Run(ctx context.Context, host string, ops []Op) error
}

// DotfileList returns the files under dir as slash-separated relative
// paths, .gitkeep excluded. A missing dir yields an empty list.
func DotfileList(dir string) ([]string, error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".gitkeep" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// Provision builds the layered dotfile ops for vmName from provisionDir
// (base/ then <vmName>/, missing dirs skipped) and hands them to r. The ssh
// alias is resolved via home's ~/.ssh/config; when no layer has anything to
// do the runner is not called.
func Provision(ctx context.Context, r Runner, home, vmName, provisionDir string) error {
	base := filepath.Join(provisionDir, "base")
	vmDir := filepath.Join(provisionDir, vmName)

	sources := map[string]string{} // relative path -> local source (later layer wins)
	for _, dir := range []string{base, vmDir} {
		rels, err := DotfileList(filepath.Join(dir, "dotfiles"))
		if err != nil {
			return fmt.Errorf("dotfiles in %s: %w", dir, err)
		}
		for _, rel := range rels {
			sources[rel] = filepath.Join(dir, "dotfiles", rel)
		}
	}
	rels := make([]string, 0, len(sources))
	for rel := range sources {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	var ops []Op
	for _, rel := range rels {
		data, err := os.ReadFile(sources[rel])
		if err != nil {
			return err
		}
		ops = append(ops, Op{Kind: KindFile, Path: rel, Data: data})
	}
	if len(ops) == 0 {
		return nil
	}
	host, _, err := sshx.Resolve(vmName, home)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", vmName, err)
	}
	return r.Run(ctx, host, ops)
}
