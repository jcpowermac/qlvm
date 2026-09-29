package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
)

const (
	cliTestRef    = "registry.example.com/ns/os-bolt:latest"
	cliTestDigest = "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456"
)

func writeTemplate(t *testing.T, root, ref, digest string, raw bool) string {
	t.Helper()
	dir := template.DirOfRef(root, ref, digest)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, (&template.Template{Dir: dir, Image: ref, Digest: digest, KernelVer: "6.12.0"}).SaveMeta(dir))
	if raw {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "template.raw"), []byte("raw"), 0o600))
	}
	return dir
}

func writeVM(t *testing.T, root, name, ref, digest string) {
	t.Helper()
	dir := filepath.Join(root, "vms", name)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, (&vm.Meta{Name: name, Image: ref, Digest: digest}).Save(dir))
}

func TestScanTemplatesEmpty(t *testing.T) {
	root := t.TempDir()
	rows, err := scanTemplates(root)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Equal(t, "no templates\n", templateListOutput(rows))
}

func TestScanTemplates(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, root, cliTestRef, cliTestDigest, true)
	writeVM(t, root, "t2", cliTestRef, cliTestDigest)
	writeVM(t, root, "t1", cliTestRef, cliTestDigest)
	// A second, incomplete dir (no META), sorted after the first.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "templates", "ns-os-bolt-sha256:deadbeef"), 0o750))

	rows, err := scanTemplates(root)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	assert.True(t, rows[0].Complete)
	assert.Equal(t, cliTestRef, rows[0].Image)
	assert.Equal(t, "6.12.0", rows[0].Kernel)
	assert.True(t, rows[0].HasRaw)
	assert.Equal(t, []string{"t1", "t2"}, rows[0].Refs, "refs must be sorted")

	assert.False(t, rows[1].Complete)
	assert.False(t, rows[1].HasRaw)
	assert.Empty(t, rows[1].Refs)
}

func TestTemplateListOutput(t *testing.T) {
	rows := []tplRow{
		{Dir: "ns-os-bolt-sha256:a", Complete: true, Image: cliTestRef, Kernel: "6.12.0", HasRaw: true, Size: 1 << 30, Refs: []string{"t1", "t2"}},
		{Dir: "ns-os-bolt-sha256:b"},
	}
	want := "TEMPLATE  IMAGE  KERNEL  SIZE  REFERENCED-BY\n" +
		"ns-os-bolt-sha256:a  registry.example.com/ns/os-bolt:latest  6.12.0  1.0G  t1, t2\n" +
		"ns-os-bolt-sha256:b  -  -  -  -\n"
	assert.Equal(t, want, templateListOutput(rows))

	w := templateWarnings(rows)
	assert.Contains(t, w, "ns-os-bolt-sha256:b")
	assert.Contains(t, w, "clean --force")
	assert.NotContains(t, w, "ns-os-bolt-sha256:a")
}

func TestCleanPlan(t *testing.T) {
	rows := []tplRow{
		{Dir: "a", Complete: true},                       // unreferenced complete: removed
		{Dir: "b", Complete: true, Refs: []string{"vm"}}, // referenced: kept
		{Dir: "c"}, // incomplete: force only
	}
	assert.Equal(t, []string{"a"}, cleanPlan(rows, false))
	assert.Equal(t, []string{"a", "c"}, cleanPlan(rows, true))
}

func TestTemplateCreatePlan(t *testing.T) {
	tests := []struct {
		name       string
		exists     bool
		referenced bool
		force      bool
		wantCalls  []string // order of action invocations
		wantErr    []string // substrings the refusal must contain
	}{
		{name: "fresh: reap then bake", exists: false, wantCalls: []string{"reap", "bake"}},
		{name: "exists without force: refuse", exists: true, wantErr: []string{"ns-os-bolt", "--force"}},
		{name: "exists referenced with force: refuse", exists: true, referenced: true, force: true, wantErr: []string{"t1", "delete those VMs first"}},
		{name: "exists unreferenced with force: reap remove bake", exists: true, force: true, wantCalls: []string{"reap", "remove", "bake"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := template.DirOfRef(root, cliTestRef, cliTestDigest)
			if tc.exists {
				require.NoError(t, os.MkdirAll(dir, 0o750))
			}
			if tc.referenced {
				writeVM(t, root, "t1", cliTestRef, cliTestDigest)
			}
			var calls []string
			act := templateCreateActions{
				Reap:   func() { calls = append(calls, "reap") },
				Remove: func() error { calls = append(calls, "remove"); return os.RemoveAll(dir) },
				Bake:   func() error { calls = append(calls, "bake"); return nil },
			}
			err := templateCreatePlan(root, dir, tc.force, act)
			if tc.wantErr != nil {
				require.Error(t, err)
				for _, sub := range tc.wantErr {
					assert.Contains(t, err.Error(), sub)
				}
				assert.Empty(t, calls, "a refusal must run no actions")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCalls, calls)
			if tc.exists {
				_, statErr := os.Stat(dir)
				assert.True(t, os.IsNotExist(statErr), "force path removes the dir before baking")
			}
		})
	}
}

func TestParseLoopLines(t *testing.T) {
	root := t.TempDir()
	alive := filepath.Join(root, "alive.img")
	require.NoError(t, os.WriteFile(alive, []byte("x"), 0o600))
	output := strings.Join([]string{
		"  (path in parens)",                                            // not a loop line
		"/dev/loop0: [0807]:106 (" + alive + ")",                        // under root, file exists: keep
		"/dev/loop1: [0807]:107 (/etc/other.img)",                       // outside root: ignore
		"/dev/loop2 (NAME OF LOOP): [0807]:108 (" + root + "/gone.img)", // under root, deleted: stale
		"/dev/loop3: [0807]:109 ( /etc/vms/x/disk.img )",                // outside root (spaces): ignore
		"/dev/loop4: [0036]:1 (" + root + "/gone2.img (deleted))",        // (deleted) suffix: stale
	}, "\n")
	stale := parseLoopLines(output, root)
	assert.Equal(t, []string{"/dev/loop2", "/dev/loop4"}, stale)
}

func TestParseLoopLinesNoLoops(t *testing.T) {
	assert.Empty(t, parseLoopLines("", t.TempDir()))
}

func TestHumanSize(t *testing.T) {
	assert.Equal(t, "2.0G", humanSize(2<<30))
	assert.Equal(t, "512M", humanSize(512<<20))
}
