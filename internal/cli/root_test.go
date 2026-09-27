package cli

import "testing"

func TestNewRootCmdIsShared(t *testing.T) {
	a, b := NewRootCmd(), NewRootCmd()
	if a != b {
		t.Fatal("NewRootCmd must return the shared root so init()-registered subcommands reach main")
	}
}
