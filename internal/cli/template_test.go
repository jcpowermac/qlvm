package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jcpowermac/qlvm/internal/agent"
)

func TestTemplateListCmd(t *testing.T) {
	fc := &fakeClient{
		listTemplatesResult: []agent.TemplateInfo{
			{Dir: "ns-os-bolt-sha256:a", Image: "registry.example.com/ns/os-bolt:latest", Kernel: "6.12.0", VMCount: 2, SizeBytes: 1 << 30, Complete: true},
			{Dir: "ns-os-bolt-sha256:b", Image: "registry.example.com/ns/os-bolt:latest", Kernel: "", VMCount: 0, SizeBytes: 0, Complete: false},
		},
	}
	useFakeClient(t, fc)

	cmd := templateListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "ns-os-bolt-sha256:a")
	assert.Contains(t, out.String(), "ns-os-bolt-sha256:b")
	assert.Contains(t, out.String(), "TEMPLATE")
	assert.Contains(t, out.String(), "IMAGE")
	assert.Contains(t, out.String(), "KERNEL")
	assert.Contains(t, out.String(), "VMs")
	assert.Contains(t, out.String(), "SIZE")
	assert.Contains(t, out.String(), "COMPLETE")
}

func TestTemplateListCmdEmpty(t *testing.T) {
	fc := &fakeClient{
		listTemplatesResult: []agent.TemplateInfo{},
	}
	useFakeClient(t, fc)

	cmd := templateListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "no templates\n", out.String())
}

func TestTemplateListCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := templateListCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestTemplateCreateCmd(t *testing.T) {
	fc := &fakeClient{
		createTemplateResult: "ns-os-bolt-sha256:a",
	}
	useFakeClient(t, fc)

	cmd := templateCreateCmd()
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"registry.example.com/ns/os-bolt:latest"})
	require.NoError(t, cmd.Execute())

	require.Len(t, fc.createTemplateCalls, 1)
	assert.Equal(t, "registry.example.com/ns/os-bolt:latest", fc.createTemplateCalls[0].image)
	assert.False(t, fc.createTemplateCalls[0].force)
	assert.Contains(t, out.String(), "baked template ns-os-bolt-sha256:a")
}

func TestTemplateCreateCmdForce(t *testing.T) {
	fc := &fakeClient{
		createTemplateResult: "ns-os-bolt-sha256:a",
	}
	useFakeClient(t, fc)

	cmd := templateCreateCmd()
	cmd.SetArgs([]string{"registry.example.com/ns/os-bolt:latest"})
	require.NoError(t, cmd.Flags().Set("force", "true"))
	require.NoError(t, cmd.Execute())

	require.Len(t, fc.createTemplateCalls, 1)
	assert.Equal(t, "registry.example.com/ns/os-bolt:latest", fc.createTemplateCalls[0].image)
	assert.True(t, fc.createTemplateCalls[0].force)
}

func TestTemplateCreateCmdProgress(t *testing.T) {
	fc := &fakeClient{
		createTemplateResult: "ns-os-bolt-sha256:a",
	}
	useFakeClient(t, fc)

	cmd := templateCreateCmd()
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"registry.example.com/ns/os-bolt:latest"})
	require.NoError(t, cmd.Execute())

	// Verify that progress was subscribed.
	require.NotNil(t, fc.progressCallback, "progress callback should be set")

	// Simulate progress messages.
	fc.progressCallback("bake-123", "pulling image")
	fc.progressCallback("bake-123", "extracting rootfs")
	fc.progressCallback("bake-123", "")
	assert.Contains(t, errOut.String(), "bake bake-123: pulling image")
	assert.Contains(t, errOut.String(), "bake bake-123: extracting rootfs")
	assert.Contains(t, errOut.String(), "bake bake-123: done")
}

func TestTemplateDeleteCmd(t *testing.T) {
	fc := &fakeClient{}
	useFakeClient(t, fc)

	cmd := templateDeleteCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"ns-os-bolt-sha256:a"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"ns-os-bolt-sha256:a"}, fc.deleteTemplateCalls)
	assert.Contains(t, out.String(), "deleted template ns-os-bolt-sha256:a")
}

func TestTemplateDeleteCmdDaemonNotRunning(t *testing.T) {
	resetClient()
	old := clientFactory
	clientFactory = func() (DaemonClient, error) {
		return nil, errors.New("daemon not running")
	}
	t.Cleanup(func() {
		clientFactory = old
		resetClient()
	})

	cmd := templateDeleteCmd()
	cmd.SetArgs([]string{"ns-os-bolt-sha256:a"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon is not running")
}

func TestFormatSize(t *testing.T) {
	assert.Equal(t, "1.0G", formatSize(1<<30))
	assert.Equal(t, "512.0M", formatSize(512<<20))
	assert.Equal(t, "1.0K", formatSize(1024))
	assert.Equal(t, "0B", formatSize(0))
	assert.Equal(t, "1.0M", formatSize(1<<20))
}
