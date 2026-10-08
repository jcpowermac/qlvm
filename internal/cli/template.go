package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
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
// it. The bake path: `template create` (vm create only references baked
// templates, it never bakes).
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

// templateCmd groups the template cache lifecycle: list (default), create,
// delete. The template dir is immutable after the bake — VMs boot their own
// per-VM kernel copies, so nothing at runtime writes into it.
func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage baked OS templates (list, create, delete)",
		RunE:  runTemplateList,
	}
	cmd.AddCommand(templateListCmd(), templateCreateCmd(), templateDeleteCmd())
	return cmd
}

func templateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List baked templates (dir, image, kernel, size, referencing VMs)",
		Args:  cobra.NoArgs,
		RunE:  runTemplateList,
	}
}

// runTemplateList renders the template table (stdout) and incomplete-dir
// warnings (stderr). Shared by the `list` subcommand and the bare
// `qlvm template` default.
func runTemplateList(cmd *cobra.Command, _ []string) error {
	rows, err := scanTemplates(installRoot)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), templateListOutput(rows))
	if w := templateWarnings(rows); w != "" {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), w)
	}
	return nil
}

// templateCreateActions are the post-decision steps of a template create
// (tests inject fakes to assert ordering: reap always precedes bake).
type templateCreateActions struct {
	Reap   func()
	Remove func() error
	Bake   func() error
}

// templateCreatePlan is the post-pull decision of `template create`: the
// dir exists + no --force -> refuse (a bake is ~5 min; never silent); the
// dir exists + --force -> flat refusal (no override flag) while any VM
// references the dir, then reap + remove + bake (today's rebuild, verbatim);
// the dir is absent -> reap + bake (a killed prior bake can leave a loop
// device on a deleted file or a scratch mount that breaks template attach).
func templateCreatePlan(root, dir string, force bool, act templateCreateActions) error {
	exists := false
	if _, err := os.Stat(dir); err == nil {
		exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists && !force {
		return fmt.Errorf("template %s already exists; re-bake with --force: qlvm template create <ref> --force", filepath.Base(dir))
	}
	if exists {
		if names := vmTemplateRefs(root)[dir]; len(names) > 0 {
			return fmt.Errorf("template %s is referenced by VMs %s; delete those VMs first", filepath.Base(dir), strings.Join(names, ", "))
		}
	}
	act.Reap()
	if exists {
		if err := act.Remove(); err != nil {
			return err
		}
	}
	return act.Bake()
}

func templateCreateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "create <ref>",
		Short: "Pull + bake a template image (refuses over an existing dir without --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			ref := args[0]
			pod, err := template.NewPodman(ctx, podmanSocket)
			if err != nil {
				return err
			}
			digest, err := pod.Pull(ctx, ref)
			if err != nil {
				return err
			}
			dir := template.DirOfRef(installRoot, ref, template.NormalizeDigest(digest))
			if err := templateCreatePlan(installRoot, dir, force, templateCreateActions{
				Reap:   func() { reapStale(ctx, pod, installRoot, out) },
				Remove: func() error { return os.RemoveAll(dir) },
				Bake: func() error {
					_, err := ensureTemplate(ctx, out, ref)
					return err
				},
			}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "created %s\n", filepath.Base(dir))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "re-bake over an existing template dir (still refused while a VM references it)")
	return cmd
}

// templateDeleteCmd removes template dirs: the named ones (exact dir name
// or unique prefix, as `vm create --template` resolves), or with no args,
// every dir no VM references (incomplete dirs only with --force).
func templateDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete [dir...]",
		Short: "Remove the named template dirs, or with no args, every dir no VM references (--force also removes incomplete dirs)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := scanTemplates(installRoot)
			if err != nil {
				return err
			}
			names, err := templateDeletePlan(installRoot, rows, args, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, name := range names {
				if err := os.RemoveAll(filepath.Join(installRoot, "templates", name)); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(out, "removed %s\n", name)
			}
			if len(names) == 0 {
				_, _ = fmt.Fprintln(out, "delete: nothing to remove")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "with no args, also remove incomplete dirs (no META)")
	return cmd
}

// templateDeletePlan names the dirs `template delete` removes: the args
// (exact dir name or unique prefix, resolved like `vm create --template`),
// or with no args the GC plan of every unreferenced dir. A named dir a VM
// still references is a flat refusal — delete the VMs first.
func templateDeletePlan(root string, rows []tplRow, names []string, force bool) ([]string, error) {
	if len(names) == 0 {
		return cleanPlan(rows, force), nil
	}
	refs := map[string][]string{}
	for _, r := range rows {
		refs[r.Dir] = r.Refs
	}
	var out []string
	for _, name := range names {
		dir, err := resolveTemplate(root, name)
		if err != nil {
			return nil, fmt.Errorf("template delete %s: %w", name, err)
		}
		hit := filepath.Base(dir)
		if vms := refs[hit]; len(vms) > 0 {
			return nil, fmt.Errorf("template %s is referenced by VMs %s; delete those VMs first", hit, strings.Join(vms, ", "))
		}
		out = append(out, hit)
	}
	return out, nil
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

// vmTemplateRefs maps template dir path -> sorted VM names whose meta.yaml
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
			fmt.Fprintf(&b, "warning: %s is incomplete (no META); remove with: qlvm template delete --force\n", r.Dir)
		case !r.HasRaw:
			fmt.Fprintf(&b, "warning: %s has no template.raw (incomplete bake); remove with: qlvm template delete\n", r.Dir)
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
// bake: stale loop devices, leftover bake scratch mounts, and exited
// image-builder containers. Best-effort — each failure is a warning and the
// bake proceeds.
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

// sshAuthKeys collects the id_*.pub identities (the same names sshx offers:
// ed25519, ecdsa, rsa) of the calling user — root under `sudo qlvm template
// create <ref> --force` — and of SUDO_USER, the desktop user who will run
// `qlvm vm run` without sudo. Baked into every VM's authorized_keys; empty result is a
// hard error: a VM without SSH identities is unrunnable.
func sshAuthKeys() (string, error) {
	var homes []string
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			homes = append(homes, u.HomeDir)
		}
	}
	seen := map[string]bool{}
	var keys []string
	for _, h := range homes {
		for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
			b, err := os.ReadFile(filepath.Join(h, ".ssh", n)) // #nosec G304 G703 -- homes from $HOME/passwd, fixed file names
			if err != nil {
				continue
			}
			k := strings.TrimSpace(string(b))
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("no ssh identity: run `ssh-keygen -t ed25519` as your desktop user before baking a template (qlvm vm run sshes into VMs with it)")
	}
	return strings.Join(keys, "\n") + "\n", nil
}
