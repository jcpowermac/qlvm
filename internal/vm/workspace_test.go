package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateWorkspaceDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.img")
	if err := CreateWorkspaceDisk(path, 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("disk not created: %v", err)
	}
	if info.Size() != 100*1024*1024 {
		t.Errorf("expected 100MB disk, got %d bytes", info.Size())
	}
}

func TestCreateWorkspaceDisk_Zero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.img")
	if err := CreateWorkspaceDisk(path, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not create the file
	if _, err := os.Stat(path); err == nil {
		t.Error("disk should not be created with size 0")
	}
}
