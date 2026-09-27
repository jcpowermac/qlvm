package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWaypipeGuard(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	err := runWaypipe([]string{"ssh", "alpha"}, bytes.NewReader(nil), io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WAYLAND_DISPLAY")
}
