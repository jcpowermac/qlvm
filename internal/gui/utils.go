package gui

import "fmt"

// formatCores formats a list of core IDs as a string.
func formatCores(cores []int) string {
	if len(cores) == 0 {
		return "auto"
	}
	result := ""
	for i, c := range cores {
		if i > 0 {
			result += ", "
		}
		result += fmt.Sprintf("%d", c)
	}
	return result
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
