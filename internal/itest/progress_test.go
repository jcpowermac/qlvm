//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"

	"github.com/jcpowermac/qlvm/internal/agent"
)

// TestProgressSignalsDelivered is a regression test for the godbus
// "Invalid match rule" bug: SubscribeProgress added a type=signal match
// option on top of the one godbus's AddMatchSignal already prepends,
// producing "type='signal',type='signal',..." which the systemd bus daemon
// rejects. The (swallowed) AddMatch error left the subscription dead, so
// no Progress/ProgressDone signals ever reached the CLI or GUI — long
// operations like `qlvm template create` ran with zero output. This test
// subscribes, triggers a fast-failing CreateTemplate (the agent emits
// Progress "starting" and ProgressDone before returning), and requires at
// least one signal to arrive.
func TestProgressSignalsDelivered(t *testing.T) {
	c, err := agent.NewClient()
	if err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer c.Close()

	type progressMsg struct {
		id, message string
	}
	ch := make(chan progressMsg, 16)
	cancel := c.SubscribeProgress(func(id, message string) {
		select {
		case ch <- progressMsg{id, message}:
		default: // drop if slow, the test only needs one signal
		}
	})
	defer cancel()

	// A bogus image ref makes the pull fail quickly, but the agent has
	// already emitted "starting" and (in the deferred done) ProgressDone.
	_, err = c.CreateTemplate("definitely-bad-ref/none:0", false)
	if err == nil {
		t.Fatal("expected pull failure for bogus image")
	}

	select {
	case m := <-ch:
		if !strings.HasPrefix(m.id, "template-create-") {
			t.Fatalf("unexpected progress id %q (message %q)", m.id, m.message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Progress signal received; the match rule may have been rejected by the bus")
	}
}
