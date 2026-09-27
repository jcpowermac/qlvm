// Package template builds container-based OS templates: it pulls a bootc
// image, runs osbuild image-builder against it through the podman Go client
// (pkg/bindings), adopts the raw output, and records the result in a
// per-digest META file so rebuilds are skipped when the digest matches.
package template

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/specgen"
	spec "github.com/opencontainers/runtime-spec/specs-go"
)

// imageBuilderImage is the osbuild image-builder used to produce bootc raw images.
const imageBuilderImage = "ghcr.io/osbuild/image-builder-cli:latest"

// Podman is the podman seam (pkg/bindings over the REST socket) used for
// template operations. Tests substitute a recording fake.
type Podman interface {
	Pull(ctx context.Context, ref string) (digest string, err error)
	RunImageBuilder(ctx context.Context, workdir, ref string, errStream io.Writer) error
}

// Template is a built OS template. RootFlags is "subvol=root" on btrfs hosts,
// empty otherwise; OstreePath is the boot path inside the rootfs.
type Template struct {
	Dir        string `toml:"dir"`
	Image      string `toml:"image"`
	Digest     string `toml:"digest"`
	KernelVer  string `toml:"kernel_ver"`
	RootDev    string `toml:"root_dev"`
	RootFlags  string `toml:"root_flags"`
	OstreePath string `toml:"ostree_path"`
}

// EnsureOpts configures Ensure.
type EnsureOpts struct {
	Root string // qlvm state root; templates land in <Root>/templates
	Ref  string // bootc image reference
	Log  io.Writer
	// Bake prepares the adopted raw rootfs (Task 8); it runs after builder
	// output adoption and before META is saved, so a failed bake re-runs
	// the whole build on the next Ensure.
	Bake func(dir string) error
}

// slugFromRef lowercases an image ref, drops the registry host and tag, and
// turns path separators into dashes:
// "registry.example.com/ns/os-bolt:latest" -> "ns-os-bolt".
func slugFromRef(ref string) string {
	ref = strings.ToLower(ref)
	if i := strings.Index(ref, "/"); i >= 0 {
		host := ref[:i]
		if strings.ContainsAny(host, ".:") || host == "localhost" {
			ref = ref[i+1:]
		}
	}
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		ref = ref[:i]
	}
	return strings.ReplaceAll(ref, "/", "-")
}

// DirFor returns the template directory for a slug/digest pair.
func DirFor(root, slug, digest string) string {
	return filepath.Join(root, "templates", slug+"-"+digest)
}

// Ensure returns the template for o.Ref, building it when it is not already
// present: pull -> digest -> skip if META matches, else run image-builder in
// DirFor, adopt <dir>/*.raw as template.raw, bake, then save META.
func Ensure(ctx context.Context, p Podman, o EnsureOpts) (*Template, error) {
	digest, err := p.Pull(ctx, o.Ref)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", o.Ref, err)
	}
	dir := DirFor(o.Root, slugFromRef(o.Ref), digest)
	if t, err := LoadMeta(dir); err == nil && t.Digest == digest {
		return t, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := p.RunImageBuilder(ctx, dir, o.Ref, o.Log); err != nil {
		return nil, fmt.Errorf("image-builder for %s: %w", o.Ref, err)
	}
	if err := adoptRaw(dir); err != nil {
		return nil, err
	}
	t := &Template{Dir: dir, Image: o.Ref, Digest: digest}
	if o.Bake != nil {
		if err := o.Bake(dir); err != nil {
			return nil, fmt.Errorf("bake %s: %w", dir, err)
		}
		// Bake may have persisted an enriched META; keep it as-is.
		if baked, err := LoadMeta(dir); err == nil && baked.Digest == digest {
			return baked, nil
		}
	}
	if err := t.SaveMeta(dir); err != nil {
		return nil, err
	}
	return t, nil
}

// adoptRaw renames the lexicographically first *.raw in dir to template.raw.
// ponytail: lexicographic pick; prefer newest by mtime if a builder can
// emit multiple raws at once.
func adoptRaw(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.raw"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no .raw output in %s", dir)
	}
	sort.Strings(matches)
	return os.Rename(matches[0], filepath.Join(dir, "template.raw"))
}

// LoadByRef returns the template under root for ref+digest without
// talking to podman (start/delete resolve the VM's kernel/initramfs dir
// from persisted meta).
func LoadByRef(root, ref, digest string) (*Template, error) {
	return LoadMeta(DirFor(root, slugFromRef(ref), digest))
}

// LoadMeta reads a template's META file.
func LoadMeta(dir string) (*Template, error) {
	data, err := os.ReadFile(filepath.Join(dir, "META")) // #nosec G304 -- dir is an internal template path under the qlvm root
	if err != nil {
		return nil, err
	}
	var t Template
	if err := toml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse META in %s: %w", dir, err)
	}
	return &t, nil
}

// SaveMeta writes the template's META file.
func (t *Template) SaveMeta(dir string) error {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(t); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "META"), b.Bytes(), 0o600)
}

// podmanClient is the real Podman over the podman REST socket (pkg/bindings).
type podmanClient struct {
	// ctx carries the bindings Connection; bindings has no public way to
	// re-associate a caller context, so request contexts are only checked
	// for cancellation before calls start.
	ctx context.Context
}

// NewPodman connects to the podman socket, e.g. unix:///run/podman/podman.sock.
func NewPodman(ctx context.Context, socket string) (Podman, error) {
	cctx, err := bindings.NewConnection(ctx, socket)
	if err != nil {
		return nil, fmt.Errorf("connect podman %s: %w", socket, err)
	}
	return &podmanClient{ctx: cctx}, nil
}

func (p *podmanClient) Pull(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	digests, err := images.Pull(p.ctx, ref, nil)
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	for i := len(digests) - 1; i >= 0; i-- {
		if digests[i] != "" {
			return digests[i], nil
		}
	}
	return "", fmt.Errorf("pull %s: no digest in response", ref)
}

func (p *podmanClient) RunImageBuilder(ctx context.Context, workdir, ref string, errStream io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := p.Pull(p.ctx, imageBuilderImage); err != nil {
		return err
	}
	resp, err := containers.CreateWithSpec(p.ctx, &specgen.SpecGenerator{
		ContainerBasicConfig: specgen.ContainerBasicConfig{
			Command: []string{"--bootc-ref", ref, "--bootc-pull-container", "--bootc-default-fs", "xfs", "--output-dir", "/output"},
		},
		ContainerStorageConfig: specgen.ContainerStorageConfig{
			Image:  imageBuilderImage,
			Mounts: []spec.Mount{{Type: "bind", Source: workdir, Destination: "/output", Options: []string{"bind", "rw"}}},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("create image-builder container: %w", err)
	}
	id := resp.ID
	// Removal is best-effort: a lingering container must not mask the
	// builder's own result.
	defer func() {
		if _, rerr := containers.Remove(p.ctx, id, &containers.RemoveOptions{Force: boolPtr(true)}); rerr != nil {
			_ = rerr
		}
	}()
	if err := containers.Start(p.ctx, id, nil); err != nil {
		return fmt.Errorf("start image-builder: %w", err)
	}
	outCh, errCh := make(chan string), make(chan string)
	go func() {
		defer close(outCh)
		defer close(errCh)
		_ = containers.Logs(p.ctx, id, &containers.LogOptions{Stdout: boolPtr(true), Stderr: boolPtr(true)}, outCh, errCh)
	}()
	go func() {
		for l := range outCh {
			_, _ = fmt.Fprint(errStream, l)
		}
	}()
	go func() {
		for l := range errCh {
			_, _ = fmt.Fprint(errStream, l)
		}
	}()
	code, werr := containers.Wait(p.ctx, id, nil)
	if werr != nil {
		return fmt.Errorf("wait image-builder: %w", werr)
	}
	if code != 0 {
		return fmt.Errorf("image-builder exited with code %d", code)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }
