package tray

import "testing"

func TestParseID(t *testing.T) {
	cases := []struct {
		id        string
		kind      idKind
		name, arg string
	}{
		{"vm/web/app/firefox.desktop", kindApp, "web", "firefox.desktop"},
		{"vm/db/sync", kindSync, "db", ""},
		{"vm/db/start", kindStart, "db", ""},
		{"vm/db/stop", kindStop, "db", ""},
		{"refresh-all", kindRefreshAll, "", ""},
		{"quit", kindQuit, "", ""},
		{"bogus", kindUnknown, "", ""},
	}
	for _, c := range cases {
		kind, name, arg := parseID(c.id)
		if kind != c.kind || name != c.name || arg != c.arg {
			t.Errorf("parseID(%q) = (%v,%q,%q), want (%v,%q,%q)",
				c.id, kind, name, arg, c.kind, c.name, c.arg)
		}
	}
}

func TestDispatchInvokesCorrectCallback(t *testing.T) {
	var calls []string
	a := Actions{
		StartVM:   func(n string) error { calls = append(calls, "start:"+n); return nil },
		StopVM:    func(n string) error { calls = append(calls, "stop:"+n); return nil },
		SyncVM:    func(n string) (int, error) { calls = append(calls, "sync:"+n); return 3, nil },
		SyncAll:   func() error { calls = append(calls, "syncall"); return nil },
		LaunchApp: func(vm, exec string) error { calls = append(calls, "launch:"+vm+"/"+exec); return nil },
		Quit:      func() { calls = append(calls, "quit") },
		Notify:    func(string) {},
	}
	d := Dispatch(a)
	d("vm/web/start")
	d("vm/web/stop")
	d("vm/web/sync")
	d("vm/web/app/firefox.desktop")
	d("refresh-all")
	d("quit")
	want := []string{
		"start:web", "stop:web", "sync:web", "launch:web/firefox.desktop", "syncall", "quit",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

func TestDispatchNotifiesOnLaunchError(t *testing.T) {
	var notified []string
	a := Actions{
		LaunchApp: func(string, string) error { return errBoom },
		Notify:    func(m string) { notified = append(notified, m) },
	}
	Dispatch(a)("vm/web/app/x.desktop")
	if len(notified) != 1 || notified[0] != "boom" {
		t.Fatalf("notified = %v, want [boom]", notified)
	}
}
