package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/template"
)

func TestResolveTemplate(t *testing.T) {
	root := t.TempDir()
	templates := filepath.Join(root, "templates")
	for _, d := range []string{"os-bolt-sha256:aaa", "os-bolt-sha256:bbb", "os-rock-sha256:ccc"} {
		require.NoError(t, os.MkdirAll(filepath.Join(templates, d), 0o750))
	}

	tests := []struct {
		name    string
		ref     string
		wantDir string
		wantErr []string // substrings the error must contain
	}{
		{name: "exact", ref: "os-rock-sha256:ccc", wantDir: "os-rock-sha256:ccc"},
		{name: "unique prefix", ref: "os-bolt-sha256:b", wantDir: "os-bolt-sha256:bbb"},
		{name: "ambiguous", ref: "os-bolt", wantErr: []string{"os-bolt-sha256:aaa", "os-bolt-sha256:bbb"}},
		{name: "missing", ref: "nope", wantErr: []string{"os-bolt-sha256:aaa", "os-rock-sha256:ccc"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := resolveTemplate(root, tc.ref)
			if tc.wantErr != nil {
				require.Error(t, err)
				for _, sub := range tc.wantErr {
					assert.Contains(t, err.Error(), sub)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, filepath.Join(templates, tc.wantDir), dir)
		})
	}
}

func TestResolveTemplateNoTemplatesDir(t *testing.T) {
	_, err := resolveTemplate(t.TempDir(), "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qlvm template create")
}

func TestLoadRefTemplate(t *testing.T) {
	root := t.TempDir()
	complete := writeTemplate(t, root, cliTestRef, cliTestDigest, true)
	// META but no template.raw
	noRaw := filepath.Join(root, "templates", "t-noraw")
	require.NoError(t, os.MkdirAll(noRaw, 0o750))
	require.NoError(t, (&template.Template{Dir: noRaw, Image: cliTestRef, Digest: cliTestDigest}).SaveMeta(noRaw))
	// template.raw but no META
	noMeta := filepath.Join(root, "templates", "t-nometa")
	require.NoError(t, os.MkdirAll(noMeta, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(noMeta, "template.raw"), []byte("raw"), 0o600))

	tpl, err := loadRefTemplate(root, filepath.Base(complete))
	require.NoError(t, err)
	assert.Equal(t, cliTestRef, tpl.Image)
	assert.Equal(t, cliTestDigest, tpl.Digest)
	assert.Equal(t, complete, tpl.Dir)

	for _, dir := range []string{noRaw, noMeta} {
		_, err := loadRefTemplate(root, filepath.Base(dir))
		require.Error(t, err, "%s must be incomplete", dir)
		assert.Contains(t, err.Error(), filepath.Base(dir), "error must name the dir")
		assert.Contains(t, err.Error(), "qlvm template create", "error must point at the bake command")
	}
}
