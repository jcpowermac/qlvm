package vm

import (
	"fmt"
	"log"
	"os"
)

// CreateWorkspaceDisk creates a raw sparse disk image of sizeMB megabytes.
// The pi-in-xen initramfs formats it with xfs on first boot if unformatted.
// sizeMB <= 0 means no disk (no-op).
func CreateWorkspaceDisk(path string, sizeMB int) error {
	if sizeMB <= 0 {
		return nil // no workspace disk
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create workspace disk: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("close workspace disk %s: %v", path, err)
		}
	}()
	if err := f.Truncate(int64(sizeMB) * 1024 * 1024); err != nil {
		return fmt.Errorf("truncate workspace disk: %w", err)
	}
	return nil
}
