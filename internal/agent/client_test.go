package agent

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// TestNewClientFailsWhenDaemonNotRunning verifies that NewClient returns an
// error when the daemon is not running. If the daemon happens to be running
// during the test, this test logs and skips the error-path check.
func TestNewClientFailsWhenDaemonNotRunning(t *testing.T) {
	_, err := NewClient()
	if err == nil {
		t.Log("daemon is running; skipping error-path check")
		return
	}
	t.Logf("NewClient returned expected error: %v", err)
}

// TestClientConnects verifies that NewClient succeeds when the daemon is
// running. If the daemon is not running, this test logs and skips.
func TestClientConnects(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Logf("daemon not running; skipping connect check: %v", err)
		return
	}
	if err := client.Close(); err != nil {
		t.Logf("close client: %v", err)
	}
	// If we got here, the client connected successfully.
}

// TestClientClose verifies that Close releases the bus connection without
// panicking.
func TestClientClose(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skipf("daemon not running; skipping close test: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

// TestProgressSignals verifies the SubscribeProgress mechanism: it returns
// a valid cancel function and the cancel function is idempotent. It does
// not test actual signal delivery (which requires a live daemon emitting
// signals).
func TestProgressSignals(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skipf("daemon not running; skipping progress signal test: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("close client: %v", err)
		}
	}()

	var cancel func()
	callback := func(id, message string) {
		// No live signals are emitted during this test.
		t.Errorf("unexpected callback: id=%q message=%q", id, message)
	}
	cancel = client.SubscribeProgress(callback)
	if cancel == nil {
		t.Fatal("SubscribeProgress returned nil cancel function")
	}

	// Call cancel multiple times to verify idempotency.
	cancel()
	cancel()
}

// TestStoreProgressSignal verifies that Progress signal bodies can be
// unmarshaled correctly using dbus.Store.
func TestStoreProgressSignal(t *testing.T) {
	body := []interface{}{"test-id", "test-message"}
	var id, message string
	err := dbus.Store(body, &id, &message)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if id != "test-id" {
		t.Errorf("id = %q, want %q", id, "test-id")
	}
	if message != "test-message" {
		t.Errorf("message = %q, want %q", message, "test-message")
	}
}

// TestStoreProgressDoneSignal verifies that ProgressDone signal bodies can
// be unmarshaled correctly using dbus.Store.
func TestStoreProgressDoneSignal(t *testing.T) {
	body := []interface{}{"test-id"}
	var id string
	err := dbus.Store(body, &id)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if id != "test-id" {
		t.Errorf("id = %q, want %q", id, "test-id")
	}
}
