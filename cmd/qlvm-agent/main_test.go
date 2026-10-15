package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner records calls and optionally returns errors for specific commands.
type mockRunner struct {
	calls   []string
	errFor  map[string]error
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		errFor: make(map[string]error),
	}
}

func (m *mockRunner) run(name string, args ...string) error {
	key := name
	for _, a := range args {
		key += " " + a
	}
	m.calls = append(m.calls, key)
	if err, ok := m.errFor[key]; ok {
		return err
	}
	return nil
}

// requireRoot sets up the test to simulate root or non-root by checking
// os.Geteuid. Tests that need non-root behavior should skip when running as root.
func requireNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test requires non-root user; skipping when running as root")
	}
}

func TestInstallSystemFilesRequiresRoot(t *testing.T) {
	requireNonRoot(t)

	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := installSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires root")
	assert.Empty(t, runner.calls, "no commands should be run when not root")
}

func TestInstallSystemFilesWritesFiles(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "system", "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "system.d", "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "actions", "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)
	require.NoError(t, err)

	// Verify service file
	data, err := os.ReadFile(servicePath) //nolint:gosec,G304
	require.NoError(t, err)
	assert.Equal(t, agent.ServiceFile(), data)

	// Verify D-Bus policy
	data, err = os.ReadFile(dbusPath) //nolint:gosec,G304
	require.NoError(t, err)
	assert.Equal(t, agent.DBusPolicy(), data)

	// Verify polkit action
	data, err = os.ReadFile(polkitPath) //nolint:gosec,G304
	require.NoError(t, err)
	assert.Equal(t, agent.PolkitAction(), data)
}

func TestInstallSystemFilesRunsSystemctl(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)
	require.NoError(t, err)

	// Verify systemctl calls in order
	assert.Equal(t, []string{
		"systemctl daemon-reload",
		"systemctl enable qlvm-agent",
	}, runner.calls)
}

func TestInstallSystemFilesServiceWriteFailure(t *testing.T) {
	// Use a path that cannot be created (file exists where dir is needed).
	file := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644)) //nolint:gosec,G306
	servicePath := filepath.Join(file, "subdir", "qlvm-agent.service")
	dbusPath := filepath.Join(t.TempDir(), "io.qlvm.agent.conf")
	polkitPath := filepath.Join(t.TempDir(), "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing service file")
	assert.Empty(t, runner.calls, "systemctl should not run if file write fails")
}

func TestInstallSystemFilesDBusWriteFailure(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	file := filepath.Join(dir, "blocked")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644)) //nolint:gosec,G306
	dbusPath := filepath.Join(file, "subdir", "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing D-Bus policy")
	assert.Empty(t, runner.calls, "systemctl should not run if file write fails")
}

func TestInstallSystemFilesPolkitWriteFailure(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "io.qlvm.agent.conf")
	file := filepath.Join(dir, "blocked")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644)) //nolint:gosec,G306
	polkitPath := filepath.Join(file, "subdir", "io.qlvm.agent.policy")

	runner := newMockRunner()
	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing polkit action")
	assert.Empty(t, runner.calls, "systemctl should not run if file write fails")
}

func TestInstallSystemFilesDaemonReloadFailure(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "io.qlvm.agent.policy")

	runner := newMockRunner()
	runner.errFor["systemctl daemon-reload"] = errors.New("daemon-reload failed")

	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl daemon-reload")
	assert.Equal(t, []string{"systemctl daemon-reload"}, runner.calls, "enable should not run if daemon-reload fails")
}

func TestInstallSystemFilesEnableFailure(t *testing.T) {
	dir := t.TempDir()
	servicePath := filepath.Join(dir, "qlvm-agent.service")
	dbusPath := filepath.Join(dir, "io.qlvm.agent.conf")
	polkitPath := filepath.Join(dir, "io.qlvm.agent.policy")

	runner := newMockRunner()
	runner.errFor["systemctl enable qlvm-agent"] = errors.New("enable failed")

	err := doInstallSystemFiles(servicePath, dbusPath, polkitPath, runner.run)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl enable qlvm-agent")
	assert.Equal(t, []string{
		"systemctl daemon-reload",
		"systemctl enable qlvm-agent",
	}, runner.calls)
}

func TestRunExternalCommandSuccess(t *testing.T) {
	// Run a simple command that should succeed.
	err := runExternalCommand("true")
	assert.NoError(t, err)
}

func TestRunExternalCommandFailure(t *testing.T) {
	// Run a command that should fail.
	err := runExternalCommand("false")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "false")
}

func TestRunExternalCommandOutputCaptured(t *testing.T) {
	// Verify that command output is captured in the error message.
	err := runExternalCommand("echo", "test-output")
	assert.NoError(t, err)

	// Use sh -c to produce output on failure
	err = runExternalCommand("sh", "-c", "echo error-msg >&2; exit 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error-msg")
}

func TestJoinStrings(t *testing.T) {
	assert.Equal(t, "", joinStrings(nil))
	assert.Equal(t, "", joinStrings([]string{}))
	assert.Equal(t, "a", joinStrings([]string{"a"}))
	assert.Equal(t, "a b", joinStrings([]string{"a", "b"}))
	assert.Equal(t, "a b c", joinStrings([]string{"a", "b", "c"}))
}
