package agent

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkWellFormedXML validates that data is well-formed XML by decoding all tokens.
func checkWellFormedXML(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func TestServiceFileNonEmpty(t *testing.T) {
	data := ServiceFile()
	assert.NotEmpty(t, data, "systemd service file should be non-empty")
}

func TestDBusPolicyNonEmpty(t *testing.T) {
	data := DBusPolicy()
	assert.NotEmpty(t, data, "D-Bus policy file should be non-empty")
}

func TestPolkitActionNonEmpty(t *testing.T) {
	data := PolkitAction()
	assert.NotEmpty(t, data, "polkit action file should be non-empty")
}

func TestServiceFileSections(t *testing.T) {
	data := ServiceFile()
	content := string(data)
	assert.Contains(t, content, "[Unit]")
	assert.Contains(t, content, "[Service]")
	assert.Contains(t, content, "[Install]")
}

func TestServiceFileRequiredDirectives(t *testing.T) {
	data := ServiceFile()
	content := string(data)
	assert.Contains(t, content, "ExecStart=/usr/local/bin/qlvm-agent")
	assert.Contains(t, content, "Requires=dbus.socket")
	assert.Contains(t, content, "After=dbus.socket")
	assert.Contains(t, content, "Restart=on-failure")
	assert.Contains(t, content, "WantedBy=multi-user.target")
	// Security hardening
	assert.Contains(t, content, "NoNewPrivileges=true")
	assert.Contains(t, content, "ProtectSystem=strict")
	assert.Contains(t, content, "PrivateTmp=true")
}

func TestDBusPolicyValidXML(t *testing.T) {
	data := DBusPolicy()
	assert.NoError(t, checkWellFormedXML(data), "D-Bus policy should be well-formed XML")
}

func TestDBusPolicyContainsMethods(t *testing.T) {
	data := DBusPolicy()
	content := string(data)
	// Read methods
	assert.Contains(t, content, "send_member=\"ListVMs\"")
	assert.Contains(t, content, "send_member=\"ListTemplates\"")
	assert.Contains(t, content, "send_member=\"ListDomains\"")
	assert.Contains(t, content, "send_member=\"GetVM\"")
	assert.Contains(t, content, "send_member=\"GetTemplate\"")
	assert.Contains(t, content, "send_member=\"GetConfig\"")
	assert.Contains(t, content, "send_member=\"GetCoremap\"")
	// Mutation methods
	assert.Contains(t, content, "send_member=\"CreateVM\"")
	assert.Contains(t, content, "send_member=\"DeleteVM\"")
	assert.Contains(t, content, "send_member=\"StartVM\"")
	assert.Contains(t, content, "send_member=\"StopVM\"")
	assert.Contains(t, content, "send_member=\"KillVM\"")
	assert.Contains(t, content, "send_member=\"RestartVM\"")
	assert.Contains(t, content, "send_member=\"CreateTemplate\"")
	assert.Contains(t, content, "send_member=\"DeleteTemplate\"")
	assert.Contains(t, content, "send_member=\"ProvisionVM\"")
	assert.Contains(t, content, "send_member=\"SyncKernel\"")
	// Install should NOT be in the policy (not a daemon method)
	assert.NotContains(t, content, "send_member=\"Install\"")
	// Signals
	assert.Contains(t, content, "send_member=\"Progress\"")
	assert.Contains(t, content, "send_member=\"ProgressDone\"")
	// Root can own the service
	assert.Contains(t, content, "allow own=\"io.qlvm.Agent\"")
}

func TestPolkitActionValidXML(t *testing.T) {
	data := PolkitAction()
	assert.NoError(t, checkWellFormedXML(data), "polkit action file should be well-formed XML")
}

func TestPolkitActionContainsActions(t *testing.T) {
	data := PolkitAction()
	content := string(data)
	actions := []string{
		"io.qlvm.CreateVM",
		"io.qlvm.DeleteVM",
		"io.qlvm.StartVM",
		"io.qlvm.StopVM",
		"io.qlvm.KillVM",
		"io.qlvm.RestartVM",
		"io.qlvm.CreateTemplate",
		"io.qlvm.DeleteTemplate",
		"io.qlvm.ProvisionVM",
		"io.qlvm.SyncKernel",
	}
	for _, action := range actions {
		assert.Contains(t, content, "id=\""+action+"\"", "polkit action file should contain action "+action)
	}
	// All actions should use auth_admin
	assert.True(t, strings.Count(content, "auth_admin") >= len(actions),
		"all polkit actions should use auth_admin")
	// Install should NOT be a polkit action
	assert.NotContains(t, content, "id=\"io.qlvm.Install\"")
}

func TestWriteServiceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system", "qlvm-agent.service")

	err := WriteServiceFile(path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, ServiceFile(), data)
}

func TestWriteDBusPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.d", "io.qlvm.agent.conf")

	err := WriteDBusPolicy(path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, DBusPolicy(), data)
}

func TestWritePolkitAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions", "io.qlvm.agent.policy")

	err := WritePolkitAction(path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, PolkitAction(), data)
}

func TestEmbeddedFSContainsFiles(t *testing.T) {
	entries, err := embeddedFS.ReadDir("embedded")
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.Contains(t, names, "qlvm-agent.service")
	assert.Contains(t, names, "io.qlvm.agent.conf")
	assert.Contains(t, names, "io.qlvm.agent.policy")
}
