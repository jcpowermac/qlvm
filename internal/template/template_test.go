package template

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRef    = "registry.example.com/ns/os-bolt:latest"
	testDigest = "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456"
)

func TestIsAgent(t *testing.T) {
	if !IsAgent(&Template{Type: "agent"}) {
		t.Error("expected agent")
	}
	if IsAgent(&Template{Type: "ostree"}) {
		t.Error("expected not agent")
	}
	if IsAgent(&Template{Type: ""}) {
		t.Error("empty type should not be agent")
	}
}

// fakePodman records calls; RunImageBuilder simulates the builder by
// dropping a raw output file in the workdir.
type fakePodman struct {
	digest        string
	pulls         []string
	builderCalls  []builderCall
	builderErr    error
	builderConts  []string // ids returned by ListBuilderContainers
	removedConts  []string
	removeContErr error
	inspectFunc   func(ctx context.Context, ref string) (map[string]string, error)
	createFunc    func(ctx context.Context, ref string, cmd []string) (string, error)
	copyFunc      func(ctx context.Context, id, path string, writer io.Writer) error
}

type builderCall struct{ workdir, ref string }

func (f *fakePodman) Pull(_ context.Context, ref string) (string, error) {
	f.pulls = append(f.pulls, ref)
	return f.digest, nil
}

func (f *fakePodman) RunImageBuilder(_ context.Context, workdir, ref string, _ io.Writer) error {
	f.builderCalls = append(f.builderCalls, builderCall{workdir: workdir, ref: ref})
	if f.builderErr != nil {
		return f.builderErr
	}
	return os.WriteFile(filepath.Join(workdir, "bootc.raw"), []byte("bootc output"), 0o600)
}

func (f *fakePodman) ListBuilderContainers(_ context.Context) ([]string, error) {
	return f.builderConts, nil
}

func (f *fakePodman) RemoveContainer(_ context.Context, id string, _ bool) error {
	if f.removeContErr != nil {
		return f.removeContErr
	}
	f.removedConts = append(f.removedConts, id)
	return nil
}

func (f *fakePodman) InspectImage(ctx context.Context, ref string) (map[string]string, error) {
	if f.inspectFunc != nil {
		return f.inspectFunc(ctx, ref)
	}
	return map[string]string{}, nil
}

func (f *fakePodman) CreateContainer(ctx context.Context, ref string, cmd []string) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, ref, cmd)
	}
	return "fake-container", nil
}

func (f *fakePodman) CopyFromContainer(ctx context.Context, id, path string, writer io.Writer) error {
	if f.copyFunc != nil {
		return f.copyFunc(ctx, id, path, writer)
	}
	return nil
}

func TestDirFor(t *testing.T) {
	slug := slugFromRef(testRef)
	assert.Equal(t, "ns-os-bolt", slug)
	want := filepath.Join("/var/lib/qlvm", "templates", "ns-os-bolt-"+testDigest)
	assert.Equal(t, want, DirFor("/var/lib/qlvm", slug, testDigest))
}

func TestEnsureSkipsWhenMetaMatchesDigest(t *testing.T) {
	root := t.TempDir()
	dir := DirFor(root, slugFromRef(testRef), testDigest)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	saved := &Template{
		Dir:        dir,
		Image:      testRef,
		Digest:     testDigest,
		KernelVer:  "6.12.0",
		RootDev:    "UUID=1234abcd",
		RootFlags:  "subvol=root",
		OstreePath: "/ostree/boot.1/ns-os-bolt/commit0/0",
	}
	require.NoError(t, saved.SaveMeta(dir))

	p := &fakePodman{digest: testDigest}
	got, err := Ensure(context.Background(), p, EnsureOpts{Root: root, Ref: testRef, Log: io.Discard})
	require.NoError(t, err)
	assert.Equal(t, []string{testRef}, p.pulls, "Pull must be called to learn the digest")
	assert.Empty(t, p.builderCalls, "builder must not run when META digest matches")
	assert.Equal(t, saved, got)
}

func TestEnsureAgentTemplatePersistsMeta(t *testing.T) {
	root := t.TempDir()
	p := &fakePodman{
		digest: testDigest,
		inspectFunc: func(ctx context.Context, ref string) (map[string]string, error) {
			return map[string]string{
				"io.qlvm.type":      "pvh-initramfs",
				"io.qlvm.kernel":    "/usr/lib/modules/7.2.8-xen1/vmlinuz",
				"io.qlvm.initramfs": "/usr/lib/modules/7.2.8-xen1/initramfs.img",
			}, nil
		},
		createFunc: func(ctx context.Context, ref string, cmd []string) (string, error) {
			return "container123", nil
		},
		copyFunc: func(ctx context.Context, id, path string, writer io.Writer) error {
			var content []byte
			if strings.Contains(path, "vmlinuz") {
				content = []byte("fake-kernel")
			} else if strings.Contains(path, "initramfs") {
				content = []byte("fake-initramfs")
			} else if strings.Contains(path, "kernel-version") {
				content = []byte("7.2.8-xen1")
			}
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{
				Name:     filepath.Base(path),
				Mode:     0o644,
				Size:     int64(len(content)),
				Typeflag: tar.TypeReg,
			})
			_, _ = tw.Write(content)
			_ = tw.Close()
			_, _ = writer.Write(buf.Bytes())
			return nil
		},
	}
	tpl, err := Ensure(context.Background(), p, EnsureOpts{
		Root: root,
		Ref:  testRef,
		Log:  io.Discard,
	})
	require.NoError(t, err)

	wantDir := filepath.Join(root, "templates", "ns-os-bolt-"+testDigest)
	require.Equal(t, wantDir, tpl.Dir)
	require.Equal(t, "agent", tpl.Type)
	require.Equal(t, "7.2.8-xen1", tpl.KernelVer)
	require.Equal(t, testDigest, tpl.Digest)

	// META must be persisted (the bug: agent path never called SaveMeta)
	loaded, err := LoadMeta(wantDir)
	require.NoError(t, err, "META must be persisted for agent templates")
	require.Equal(t, "agent", loaded.Type)
	require.Equal(t, "7.2.8-xen1", loaded.KernelVer)
	require.Equal(t, testDigest, loaded.Digest)

	// vmlinuz and initramfs must exist
	_, err = os.Stat(filepath.Join(wantDir, "vmlinuz"))
	require.NoError(t, err, "vmlinuz must be extracted")
	_, err = os.Stat(filepath.Join(wantDir, "initramfs"))
	require.NoError(t, err, "initramfs must be extracted")

	// The builder must NOT have run (agent path skips image-builder)
	assert.Empty(t, p.builderCalls)
}

func TestEnsureRunsBuilderWhenMissing(t *testing.T) {
	root := t.TempDir()
	p := &fakePodman{digest: testDigest}
	var baked []string
	tpl, err := Ensure(context.Background(), p, EnsureOpts{
		Root: root,
		Ref:  testRef,
		Log:  io.Discard,
		Bake: func(dir string) error { baked = append(baked, dir); return nil },
	})
	require.NoError(t, err)

	require.Len(t, p.builderCalls, 1)
	call := p.builderCalls[0]
	wantDir := filepath.Join(root, "templates", "ns-os-bolt-"+testDigest)
	assert.Equal(t, wantDir, call.workdir)
	assert.Equal(t, testRef, call.ref)

	require.Len(t, baked, 1)
	assert.Equal(t, wantDir, baked[0], "Bake runs on the template dir")

	raw, err := os.ReadFile(filepath.Join(wantDir, "template.raw")) // #nosec G304 -- internal template dir
	require.NoError(t, err, "adopted raw must be renamed to template.raw")
	assert.Equal(t, "bootc output", string(raw))
	_, err = os.Stat(filepath.Join(wantDir, "bootc.raw"))
	assert.True(t, os.IsNotExist(err), "builder output must be renamed, not copied")

	loaded, err := LoadMeta(wantDir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, loaded.Dir)
	assert.Equal(t, testRef, loaded.Image)
	assert.Equal(t, testDigest, loaded.Digest)
	assert.Equal(t, loaded, tpl)
}

func TestEnsureAdoptsUnprocessedTemplate(t *testing.T) {
	root := t.TempDir()
	dir := DirFor(root, slugFromRef(testRef), testDigest)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, (&Template{Dir: dir, Image: testRef, Digest: "sha256:stale"}).SaveMeta(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.raw"), []byte("stale"), 0o600))

	p := &fakePodman{digest: testDigest}
	tpl, err := Ensure(context.Background(), p, EnsureOpts{Root: root, Ref: testRef, Log: io.Discard})
	require.NoError(t, err)
	assert.Len(t, p.builderCalls, 1, "digest mismatch must re-run the builder")

	raw, err := os.ReadFile(filepath.Join(dir, "template.raw")) // #nosec G304 -- internal template dir
	require.NoError(t, err)
	assert.Equal(t, "bootc output", string(raw), "builder output must replace the stale raw")
	assert.Equal(t, testDigest, tpl.Digest)
}

func TestEnsurePreservesBakeEnrichedMeta(t *testing.T) {
	root := t.TempDir()
	wantDir := DirFor(root, slugFromRef(testRef), testDigest)
	p := &fakePodman{digest: testDigest}
	tpl, err := Ensure(context.Background(), p, EnsureOpts{
		Root: root,
		Ref:  testRef,
		Log:  io.Discard,
		Bake: func(dir string) error {
			return (&Template{
				Dir:        dir,
				Image:      testRef,
				Digest:     testDigest,
				KernelVer:  "6.12.9",
				RootDev:    "UUID=bake1234",
				RootFlags:  "subvol=root",
				OstreePath: "/ostree/boot.1/ns-os-bolt/commit1/0",
			}).SaveMeta(dir)
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "UUID=bake1234", tpl.RootDev, "Ensure must return the bake-enriched template")
	assert.Equal(t, "6.12.9", tpl.KernelVer)
	assert.Equal(t, "subvol=root", tpl.RootFlags)
	assert.Equal(t, "/ostree/boot.1/ns-os-bolt/commit1/0", tpl.OstreePath)

	loaded, err := LoadMeta(wantDir) // #nosec G304 -- internal template dir
	require.NoError(t, err)
	assert.Equal(t, "UUID=bake1234", loaded.RootDev, "on-disk META must retain bake fields")
	assert.Equal(t, tpl, loaded)
}

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	orig := &Template{
		Dir:        "/var/lib/qlvm/templates/ns-os-bolt-sha256:abc",
		Image:      testRef,
		Digest:     testDigest,
		KernelVer:  "6.12.7",
		RootDev:    "UUID=abcd1234",
		RootFlags:  "subvol=root",
		OstreePath: "/ostree/boot.1/ns-os-bolt/commit0/0",
	}
	require.NoError(t, orig.SaveMeta(dir))
	got, err := LoadMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
}

func TestSlugFromRef(t *testing.T) {
	cases := map[string]string{
		"registry.example.com/ns/os-bolt:latest": "ns-os-bolt",
		"registry.example.com:5000/ns/os-bolt":   "ns-os-bolt",
		"quay.io/Fedora/FedoraCoreOS:stable":     "fedora-fedoracoreos",
		"os-bolt:latest":                         "os-bolt",
		"localhost/ns/os-bolt":                   "ns-os-bolt",
	}
	for ref, want := range cases {
		assert.Equal(t, want, slugFromRef(ref), "ref %q", ref)
	}
	assert.Equal(t, "a-b", slugFromRef(strings.ToUpper("Registry.example.com/a/B")))
}

func TestEnsureReusesFinishedRaw(t *testing.T) {
	root := t.TempDir()
	dir := DirFor(root, slugFromRef(testRef), testDigest)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "template.raw"), []byte("done"), 0o600))

	p := &fakePodman{digest: testDigest}
	tpl, err := Ensure(context.Background(), p, EnsureOpts{Root: root, Ref: testRef, Log: io.Discard})
	require.NoError(t, err)
	assert.Len(t, p.builderCalls, 0, "existing template.raw without META must not re-run the builder")
	assert.Equal(t, testDigest, tpl.Digest)
}

func TestNormalizeDigest(t *testing.T) {
	assert.Equal(t, "sha256:9bc21b", NormalizeDigest("sha256:9bc21b"))
	assert.Equal(t, "sha256:9bc21b264ad327fbe8b3af63bfe1597789f29a5f25f5399b0057cb010ab0e70b",
		NormalizeDigest("9bc21b264ad327fbe8b3af63bfe1597789f29a5f25f5399b0057cb010ab0e70b"))
	assert.Equal(t, "weird", NormalizeDigest("weird"))
	assert.Equal(t, "abc", NormalizeDigest("abc"))
}

func TestEnsureCanonicalizesBareDigest(t *testing.T) {
	root := t.TempDir()
	bare := "9bc21b264ad327fbe8b3af63bfe1597789f29a5f25f5399b0057cb010ab0e70b"
	p := &fakePodman{digest: bare}
	_, err := Ensure(context.Background(), p, EnsureOpts{Root: root, Ref: testRef, Log: io.Discard})
	_ = err
	want := filepath.Join(root, "templates", slugFromRef(testRef)+"-sha256:"+bare)
	_, statErr := os.Stat(want)
	assert.NoError(t, statErr, "template dir must use the canonical sha256:-prefixed digest")
}

func TestReapBuilderContainers(t *testing.T) {
	p := &fakePodman{builderConts: []string{"abc123", "def456"}}
	var log strings.Builder
	require.NoError(t, ReapBuilderContainers(context.Background(), p, &log))
	assert.Equal(t, []string{"abc123", "def456"}, p.removedConts)
	assert.Contains(t, log.String(), "abc123")
	assert.Contains(t, log.String(), "def456")
}

func TestReapBuilderContainersNoopWhenNone(t *testing.T) {
	p := &fakePodman{}
	var log strings.Builder
	require.NoError(t, ReapBuilderContainers(context.Background(), p, &log))
	assert.Empty(t, p.removedConts)
	assert.Empty(t, log.String())
}

func TestReapBuilderContainersSurfacesRemoveError(t *testing.T) {
	p := &fakePodman{builderConts: []string{"abc123"}, removeContErr: errors.New("boom")}
	err := ReapBuilderContainers(context.Background(), p, io.Discard)
	require.ErrorContains(t, err, "abc123")
	assert.ErrorContains(t, err, "boom")
}

func TestDirOfRef(t *testing.T) {
	assert.Equal(t,
		filepath.Join("/var/lib/qvm", "templates", "ns-os-bolt-"+testDigest),
		DirOfRef("/var/lib/qvm", testRef, testDigest))
}

func TestEnsureBackfillsEmptyImageFromBakeMeta(t *testing.T) {
	root := t.TempDir()
	p := &fakePodman{digest: testDigest}
	tpl, err := Ensure(context.Background(), p, EnsureOpts{
		Root: root,
		Ref:  testRef,
		Log:  io.Discard,
		Bake: func(dir string) error {
			// Simulate the ostree enricher: persists a META without Image.
			return (&Template{Dir: dir, Digest: testDigest, KernelVer: "6.12.0"}).SaveMeta(dir)
		},
	})
	require.NoError(t, err)
	assert.Equal(t, testRef, tpl.Image)
	baked, err := LoadMeta(tpl.Dir)
	require.NoError(t, err)
	assert.Equal(t, testRef, baked.Image, "backfill must be persisted to the META file")
}
