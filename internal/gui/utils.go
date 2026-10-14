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

// truncateDigest truncates a long digest string for display.
func truncateDigest(digest string) string {
	if len(digest) > 20 {
		return digest[:20] + "…"
	}
	return digest
}
