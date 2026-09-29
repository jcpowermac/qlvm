package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/jcpowermac/qlvm/internal/ostree"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

// podmanSocket is the podman REST socket (podman.socket unit).
const podmanSocket = "unix:///run/podman/podman.sock"

// fstype is the filesystem for both the template bake and the VM disk
// (ruling 4: the CLI passes it explicitly; one spelling so the two can't drift).
const fstype = "xfs"

// ensureTemplate pulls+bakes (if needed) the template for image and returns
// it. Shared by `create` and `template rebuild`.
func ensureTemplate(ctx context.Context, out io.Writer, image string) (*template.Template, error) {
	pod, err := template.NewPodman(ctx, podmanSocket)
	if err != nil {
		return nil, err
	}
	return template.Ensure(ctx, pod, template.EnsureOpts{
		Root: installRoot,
		Ref:  image,
		Log:  out,
		Bake: func(dir string) error {
			keys, err := sshAuthKeys()
			if err != nil {
				return err
			}
			return ostree.BakeTemplate(ctx, ostree.NewFS(nil), dir, fstype, keys)
		},
	})
}

// templateCmd groups the template cache lifecycle: list (default), rebuild,
// clean.
func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage baked OS templates (list, rebuild, clean)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := scanTemplates(installRoot)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), templateListOutput(rows))
			if w := templateWarnings(rows); w != "" {
				_, _ = fmt.Fprint(cmd.ErrOrStderr(), w)
			}
			return nil
		},
	}
	cmd.AddCommand(templateRebuildCmd(), templateCleanCmd())
	return cmd
}

func templateRebuildCmd() *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Force a fresh pull + bake of a template image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			pod, err := template.NewPodman(ctx, podmanSocket)
			if err != nil {
				return err
			}
			digest, err := pod.Pull(ctx, image)
			if err != nil {
				return err
			}
			digest = template.NormalizeDigest(digest)
			dir := template.DirOfRef(installRoot, image, digest)
			if names := vmTemplateRefs(installRoot)[dir]; len(names) > 0 {
				return fmt.Errorf("template %s is referenced by VMs %s; delete those VMs first", filepath.Base(dir), strings.Join(names, ", "))
			}
			reapStale(ctx, pod, installRoot, out)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			if _, err := ensureTemplate(ctx, out, image); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "rebuilt %s\n", filepath.Base(dir))
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "bootc image reference (required)")
	_ = cmd.MarkFlagRequired("image")
	return cmd
}

func templateCleanCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove template dirs no VM references (--force also removes incomplete dirs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := scanTemplates(installRoot)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			n := 0
			for _, name := range cleanPlan(rows, force) {
				if err := os.RemoveAll(filepath.Join(installRoot, "templates", name)); err != nil {
					return err
				}
				n++
				_, _ = fmt.Fprintf(out, "removed %s\n", name)
			}
			if n == 0 {
				_, _ = fmt.Fprintln(out, "clean: nothing to remove")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "also remove incomplete dirs (no META)")
	return cmd
}

// tplRow is one scanned template dir.
type tplRow struct {
	Dir      string // dir basename (slug-digest)
	Complete bool   // loadable META present
	Image    string // META.Image
	Kernel   string // META.KernelVer
	HasRaw   bool
	Size     int64
	Refs     []string // VM names consuming this dir, sorted
}

// scanTemplates walks <root>/templates: one row per dir, complete when a
// loadable META is present. A missing templates dir is an empty result, not
// an error.
func scanTemplates(root string) ([]tplRow, error) {
	refs := vmTemplateRefs(root)
	entries, err := os.ReadDir(filepath.Join(root, "templates"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []tplRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, "templates", e.Name())
		row := tplRow{Dir: e.Name(), Refs: refs[dir]}
		if t, merr := template.LoadMeta(dir); merr == nil {
			row.Complete = true
			row.Image = t.Image
			row.Kernel = t.KernelVer
		}
		if fi, serr := os.Stat(filepath.Join(dir, "template.raw")); serr == nil {
			row.HasRaw = true
			row.Size = fi.Size()
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// vmTemplateRefs maps template dir path -> sorted VM names whose meta.toml
// (Image, Digest) resolves to that dir.
func vmTemplateRefs(root string) map[string][]string {
	refs := map[string][]string{}
	entries, err := os.ReadDir(filepath.Join(root, "vms"))
	if err != nil {
		return refs
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := vm.LoadMeta(filepath.Join(root, "vms", e.Name()))
		if err != nil {
			continue // crashed create may leave a dir without meta
		}
		dir := template.DirOfRef(root, m.Image, m.Digest)
		refs[dir] = append(refs[dir], m.Name)
	}
	for _, v := range refs {
		sort.Strings(v)
	}
	return refs
}

// templateListOutput renders `qlvm template list`: header + one row per dir;
// incomplete dirs show "-" cells (the warnings go to stderr).
func templateListOutput(rows []tplRow) string {
	if len(rows) == 0 {
		return "no templates\n"
	}
	var b strings.Builder
	b.WriteString("TEMPLATE  IMAGE  KERNEL  SIZE  REFERENCED-BY\n")
	for _, r := range rows {
		image, kernel, size := "-", "-", "-"
		if r.Complete {
			image = r.Image
			kernel = r.Kernel
		}
		if r.HasRaw {
			size = humanSize(r.Size)
		}
		refs := "-"
		if len(r.Refs) > 0 {
			refs = strings.Join(r.Refs, ", ")
		}
		fmt.Fprintf(&b, "%s  %s  %s  %s  %s\n", r.Dir, image, kernel, size, refs)
	}
	return b.String()
}

// templateWarnings reports incomplete template dirs (no META, or META
// without template.raw) — nothing in them is reusable.
func templateWarnings(rows []tplRow) string {
	var b strings.Builder
	for _, r := range rows {
		switch {
		case !r.Complete:
			fmt.Fprintf(&b, "warning: %s is incomplete (no META); remove with: qlvm template clean --force\n", r.Dir)
		case !r.HasRaw:
			fmt.Fprintf(&b, "warning: %s has no template.raw (incomplete bake); remove with: qlvm template clean\n", r.Dir)
		}
	}
	return b.String()
}

// cleanPlan returns template dir names to remove: complete and unreferenced
// always; incomplete (no META) only with force.
func cleanPlan(rows []tplRow, force bool) []string {
	var out []string
	for _, r := range rows {
		if len(r.Refs) > 0 {
			continue
		}
		if r.Complete || force {
			out = append(out, r.Dir)
		}
	}
	return out
}

func humanSize(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
}

// reapStale clears the residue a killed bake leaves behind before a fresh
// rebuild: stale loop devices, leftover bake scratch mounts, and exited
// image-builder containers. Best-effort — each failure is a warning and the
// rebuild proceeds.
func reapStale(ctx context.Context, pod template.Podman, root string, out io.Writer) {
	staleLoops(ctx, root, out)
	leftoverBakeMounts(out)
	if err := template.ReapBuilderContainers(ctx, pod, out); err != nil {
		_, _ = fmt.Fprintf(out, "warning: reap image-builder containers: %v\n", err)
	}
}

// loopLineRe matches `losetup -a` lines. The backing path may carry a
// " (deleted)" suffix; group 2 captures the path without it.
var loopLineRe = regexp.MustCompile(`^(/\S+)(?: \([^)]*\))?:.*\(([^()]+)(?: \(deleted\))?\)\s*$`)

// parseLoopLines returns the loop devices in `losetup -a` output whose
// backing file is under root and no longer exists (stale loops).
func parseLoopLines(output, root string) []string {
	prefix := root + string(filepath.Separator)
	var out []string
	for _, line := range strings.Split(output, "\n") {
		m := loopLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := strings.TrimSpace(m[2])
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			out = append(out, m[1])
		}
	}
	return out
}

// staleLoops detaches loop devices under root whose backing file was deleted
// (a killed create leaves them attached, and they break template re-attach).
func staleLoops(ctx context.Context, root string, out io.Writer) {
	b, err := exec.CommandContext(ctx, "losetup", "-a").Output() // #nosec G204 -- fixed binary, fixed args
	if err != nil {
		_, _ = fmt.Fprintf(out, "warning: losetup: %v\n", err)
		return
	}
	for _, dev := range parseLoopLines(string(b), root) {
		if err := exec.CommandContext(ctx, "losetup", "-d", dev).Run(); err != nil { // #nosec G204 -- dev is a parsed /dev/loopN name
			_, _ = fmt.Fprintf(out, "warning: losetup -d %s: %v\n", dev, err)
		} else {
			_, _ = fmt.Fprintf(out, "detached stale loop %s\n", dev)
		}
	}
}

// leftoverBakeMounts unmounts and removes <tmp>/qlvm-ostree-* scratch dirs a
// killed bake leaves mounted (ostree.mountTarget).
func leftoverBakeMounts(out io.Writer) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ostree.MountPrefix) {
			continue
		}
		dir := filepath.Join(os.TempDir(), e.Name())
		if err := unix.Unmount(dir, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
			_, _ = fmt.Fprintf(out, "warning: unmount %s: %v\n", dir, err)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			_, _ = fmt.Fprintf(out, "warning: remove %s: %v\n", dir, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "removed stale bake mount %s\n", dir)
	}
}

func init() {
	NewRootCmd().AddCommand(templateCmd())
}
