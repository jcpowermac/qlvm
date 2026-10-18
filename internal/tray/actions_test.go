package tray

import (
	"errors"
	"testing"
)

var errBoom = errors.New("boom")

func TestLaunchAppStoppedStartsWaitsThenRuns(t *testing.T) {
	var calls []string
	running := func(vm string) bool { return false }
	start := func(vm string) error { calls = append(calls, "start:"+vm); return nil }
	wait := func(vm string) error { calls = append(calls, "wait:"+vm); return nil }
	run := func(vm, exec string) error { calls = append(calls, "run:"+vm+"/"+exec); return nil }
	notify := func(string) {}

	err := LaunchApp(running, start, wait, run, notify, "web", "firefox.desktop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"start:web", "wait:web", "run:web/firefox.desktop"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

func TestLaunchAppRunningSkipsStartAndWait(t *testing.T) {
	var calls []string
	running := func(string) bool { return true }
	start := func(string) error { calls = append(calls, "start"); return nil }
	wait := func(string) error { calls = append(calls, "wait"); return nil }
	run := func(vm, exec string) error { calls = append(calls, "run"); return nil }
	notify := func(string) {}

	if err := LaunchApp(running, start, wait, run, notify, "web", "firefox.desktop"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "run" {
		t.Fatalf("expected only run, got %v", calls)
	}
}

func TestLaunchAppStartErrorSkipsRunAndNotifies(t *testing.T) {
	var calls []string
	var notified []string
	running := func(string) bool { return false }
	start := func(string) error { calls = append(calls, "start"); return errBoom }
	wait := func(string) error { calls = append(calls, "wait"); return nil }
	run := func(string, string) error { calls = append(calls, "run"); return nil }
	notify := func(m string) { notified = append(notified, m) }

	err := LaunchApp(running, start, wait, run, notify, "web", "firefox.desktop")
	if err == nil {
		t.Fatal("expected error from start")
	}
	if len(calls) != 1 || calls[0] != "start" {
		t.Fatalf("expected only start to run, got %v", calls)
	}
	if len(notified) != 1 {
		t.Fatalf("expected 1 notification, got %v", notified)
	}
}

func TestLaunchAppRunErrorNotifies(t *testing.T) {
	var notified []string
	run := func(string, string) error { return errBoom }
	notify := func(m string) { notified = append(notified, m) }
	err := LaunchApp(func(string) bool { return true }, nil, nil, run, notify, "web", "x")
	if err == nil {
		t.Fatal("expected run error")
	}
	if len(notified) != 1 {
		t.Fatalf("expected 1 notification, got %v", notified)
	}
}

func TestSyncVMForStoppedErrorsNamingVM(t *testing.T) {
	var synced bool
	n, err := SyncVMFor(func(string) bool { return false },
		func(string) (int, error) { synced = true; return 0, nil }, "db")
	if err == nil {
		t.Fatal("expected error for stopped VM")
	}
	if n != 0 || synced {
		t.Errorf("should not call sync for stopped VM (n=%d synced=%v)", n, synced)
	}
	if got := err.Error(); got != "sync db: start the VM first (its desktop list is fetched over SSH)" {
		t.Errorf("error = %q", got)
	}
}

func TestSyncVMForRunningCallsSync(t *testing.T) {
	n, err := SyncVMFor(func(string) bool { return true },
		func(string) (int, error) { return 7, nil }, "web")
	if err != nil || n != 7 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
