package gui

import "fmt"

// formatSize formats a byte count as a human-readable string.
func formatSize(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	bytes /= unit
	if bytes < unit {
		return fmt.Sprintf("%d KB", bytes)
	}
	bytes /= unit
	if bytes < unit {
		return fmt.Sprintf("%d MB", bytes)
	}
	bytes /= unit
	return fmt.Sprintf("%d GB", bytes)
}

// truncateDigest truncates a digest to its first 12 hex characters
// (standard Docker/OCI display format, e.g. sha256:6293750d9ed2…).
func truncateDigest(digest string) string {
	// Strip "sha256:" prefix if present.
	hex := digest
	if len(hex) > 7 && hex[:7] == "sha256:" {
		hex = hex[7:]
	}
	if len(hex) > 12 {
		return "sha256:" + hex[:12]
	}
	return digest
}
