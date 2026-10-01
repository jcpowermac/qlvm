package cli

import (
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRootCmdIsShared(t *testing.T) {
	a, b := NewRootCmd(), NewRootCmd()
	if a != b {
		t.Fatal("NewRootCmd must return the shared root so init()-registered subcommands reach main")
	}
}

// childNames is the registered subcommand surface of cmd (cobra's help and
// completion commands are not added until Execute, so this is exactly what
// the user can type).
func childNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	sort.Strings(names)
	return names
}

func child(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestCommandTree(t *testing.T) {
	root := NewRootCmd()
	require.Equal(t, []string{"apps", "install", "template", "vm"}, childNames(root),
		"top level keeps apps/install; VM commands move under vm")
	vm := child(root, "vm")
	require.NotNil(t, vm, "vm parent must exist")
	require.Equal(t, []string{"create", "delete", "kill", "list", "provision", "restart", "run", "start", "stop", "sync-kernel"},
		childNames(vm))
	for _, old := range []string{"create", "start", "stop", "kill", "delete", "list", "run", "provision", "restart", "sync-kernel"} {
		require.Nil(t, child(root, old), "old top-level %q must be an unknown command (no aliases)", old)
	}
	tpl := child(root, "template")
	require.NotNil(t, tpl, "template parent must exist")
	require.Equal(t, []string{"create", "delete", "list"}, childNames(tpl),
		"template follows the vm surface: list, create, delete (clean is gone)")
	tplCreate := child(tpl, "create")
	require.NotNil(t, tplCreate, "template create must exist")
	assert.Equal(t, "create <ref>", tplCreate.Use)
	require.NotNil(t, tplCreate.Flags().Lookup("force"), "template create must expose --force")
	require.Nil(t, child(tpl, "rebuild"), "rebuild must be gone entirely (no alias)")
}
