package provisioner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeRunner records what Provision asks the Runner to execute.
type fakeRunner struct {
	ran  bool
	host string
	ops  []Op
}

func (f *fakeRunner) Run(_ context.Context, host string, ops []Op) error {
	f.ran, f.host, f.ops = true, host, ops
	return nil
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDotfileList(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".bashrc"), "x")
	writeTestFile(t, filepath.Join(dir, ".gitkeep"), "")
	writeTestFile(t, filepath.Join(dir, ".config", "starship.toml"), "x")

	got, err := DotfileList(dir)
	if err != nil {
		t.Fatalf("DotfileList: %v", err)
	}
	want := []string{".bashrc", ".config/starship.toml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DotfileList = %q, want %q", got, want)
	}

	// missing dir is empty, not an error
	got, err = DotfileList(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatalf("DotfileList(missing): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("DotfileList(missing) = %q, want empty", got)
	}
}

// provisionFixture lays out base + per-vm dotfile layers plus an ssh config
// entry for the "vma" alias.
func provisionFixture(t *testing.T, withVMDir bool) (home, provisionDir string) {
	t.Helper()
	home = t.TempDir()
	writeTestFile(t, filepath.Join(home, ".ssh", "config"),
		"Host vma\n  HostName 192.168.1.50\n  User user\n")

	provisionDir = t.TempDir()
	base := filepath.Join(provisionDir, "base")
	writeTestFile(t, filepath.Join(base, "dotfiles", ".gitkeep"), "")
	writeTestFile(t, filepath.Join(base, "dotfiles", ".bashrc"), "base-bashrc\n")
	writeTestFile(t, filepath.Join(base, "dotfiles", ".config", "starship.toml"), "base-starship\n")
	if withVMDir {
		vmDir := filepath.Join(provisionDir, "vma")
		writeTestFile(t, filepath.Join(vmDir, "dotfiles", ".bashrc"), "vm-bashrc\n")
	}
	return home, provisionDir
}

func TestProvisionDotfiles(t *testing.T) {
	ctx := context.Background()
	home, provisionDir := provisionFixture(t, true)

	wantFiles := []Op{
		{Kind: KindFile, Path: ".bashrc", Data: []byte("vm-bashrc\n")}, // vm layer wins
		{Kind: KindFile, Path: ".config/starship.toml", Data: []byte("base-starship\n")},
	}

	t.Run("vm layer overrides base", func(t *testing.T) {
		f := &fakeRunner{}
		if err := Provision(ctx, f, home, "vma", provisionDir); err != nil {
			t.Fatal(err)
		}
		if !f.ran || f.host != "192.168.1.50" {
			t.Fatalf("ran=%v host=%q, want ran with 192.168.1.50", f.ran, f.host)
		}
		if !reflect.DeepEqual(f.ops, wantFiles) {
			t.Fatalf("ops = %#v, want %#v", f.ops, wantFiles)
		}
	})

	t.Run("missing vm dir is base only", func(t *testing.T) {
		_, provisionDir := provisionFixture(t, false)
		f := &fakeRunner{}
		if err := Provision(ctx, f, home, "vma", provisionDir); err != nil {
			t.Fatal(err)
		}
		want := []Op{
			{Kind: KindFile, Path: ".bashrc", Data: []byte("base-bashrc\n")},
			{Kind: KindFile, Path: ".config/starship.toml", Data: []byte("base-starship\n")},
		}
		if !reflect.DeepEqual(f.ops, want) {
			t.Fatalf("ops = %#v, want %#v", f.ops, want)
		}
	})

	t.Run("nothing to do skips the runner", func(t *testing.T) {
		f := &fakeRunner{}
		if err := Provision(ctx, f, home, "vma", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if f.ran {
			t.Fatal("runner ran with no layers present")
		}
	})
}
