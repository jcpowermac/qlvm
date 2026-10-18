package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed embedded
var embeddedFS embed.FS

// installSystemFiles writes the user systemd unit to
// ~/.config/systemd/user/qlvm-tray.service and enables+starts it.
func installSystemFiles() error {
	content, err := embeddedFS.ReadFile("embedded/qlvm-tray.service")
	if err != nil {
		return fmt.Errorf("read embedded unit: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home: %w", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "qlvm-tray.service")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)

	// Best-effort reload + enable; the user session may be on a different
	// bus, so surface systemctl's own output and treat failure as fatal so
	// the user sees why.
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "qlvm-tray.service"},
	} {
		cmd := exec.Command("systemctl", args...) // #nosec G204 -- systemctl is the intended target
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("systemctl %v: %w", args, err)
		}
	}
	return nil
}
