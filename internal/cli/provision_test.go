package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/provisioner"
)

func TestProvisionMode(t *testing.T) {
	m, err := provisionMode(false, false)
	require.NoError(t, err)
	assert.Equal(t, provisioner.ModeAll, m)

	m, err = provisionMode(true, false)
	require.NoError(t, err)
	assert.Equal(t, provisioner.ModePackages, m)

	m, err = provisionMode(false, true)
	require.NoError(t, err)
	assert.Equal(t, provisioner.ModeDotfiles, m)

	_, err = provisionMode(true, true)
	assert.Error(t, err)
}
