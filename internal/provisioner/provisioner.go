// Package provisioner layers VM provisioning (packages + dotfiles) from a
// provision dir: base/ then <vm>/, missing layers skipped. Ops are executed
// by a Runner; the shipped runner (NewSSHRunner) runs dnf over sshx and
// uploads dotfiles over sftp into the VM user's home.
package provisioner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jcpowermac/qlvm/internal/sshx"
)

// Mode selects which provisioning layers run.
type Mode int

const (
	// ModeAll provisions both packages and dotfiles.
	ModeAll Mode = iota
	// ModePackages provisions packages only.
	ModePackages
	// ModeDotfiles provisions dotfiles only.
	ModeDotfiles
)

// Op kinds executed by a Runner.
const (
	KindPkg  = "pkg"  // Pkgs: packages to install
	KindFile = "file" // Path/Data: dotfile to place under the VM user's home
)

// Op is one provisioning unit.
type Op struct {
	Kind string
	Pkgs []string // KindPkg
	Path string   // KindFile: relative to the VM user's home
	Data []byte   // KindFile
}

// Runner executes ops on one VM. host is the resolved remote host
// (Provision resolves the ssh alias); implementations must treat an empty
// op list as a no-op.
type Runner interface {
	Run(ctx context.Context, host string, ops []Op) error
}

// ParsePackages returns the union of the packages.txt files in dirs, in
// file order (earlier dirs first), with # comments, blank lines, and
// duplicates dropped. Missing/unreadable layers are skipped.
func ParsePackages(dirs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(d, "packages.txt")) // #nosec G304 -- d is the caller-provisioned layer dir
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
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

// Provision builds the layered ops for vmName from provisionDir (base/ then
// <vmName>/, missing dirs skipped) and hands them to r. The ssh alias is
// resolved via home's ~/.ssh/config; when no layer has anything to do the
// runner is not called.
func Provision(ctx context.Context, r Runner, home, vmName, provisionDir string, mode Mode) error {
	base := filepath.Join(provisionDir, "base")
	vmDir := filepath.Join(provisionDir, vmName)

	var ops []Op
	if mode == ModeAll || mode == ModePackages {
		if pkgs := ParsePackages(base, vmDir); len(pkgs) > 0 {
			ops = append(ops, Op{Kind: KindPkg, Pkgs: pkgs})
		}
	}
	if mode == ModeAll || mode == ModeDotfiles {
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
		for _, rel := range rels {
			data, err := os.ReadFile(sources[rel])
			if err != nil {
				return err
			}
			ops = append(ops, Op{Kind: KindFile, Path: rel, Data: data})
		}
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
