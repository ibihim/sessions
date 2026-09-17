package sessions

import (
	"strings"
	"testing"
	"time"
)

// Running sessions hold their places. They are ordered by when their
// process started, which a busy session's writing does not move — the
// fixture's EndedAt order disagrees on purpose — and ties fall to the id.
// The rest keep the newest-first order Scan gave them.
func TestLiveFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	in := []Session{ // newest-first by EndedAt, as Scan returns them
		{ID: "late", PID: 3, LiveSince: t0.Add(2 * time.Hour), EndedAt: t0.Add(5 * time.Hour)},
		{ID: "done", EndedAt: t0.Add(4 * time.Hour)},
		{ID: "early", PID: 1, LiveSince: t0, EndedAt: t0.Add(3 * time.Hour)},
		{ID: "tie-b", PID: 5, LiveSince: t0.Add(time.Hour), EndedAt: t0.Add(2 * time.Hour)},
		{ID: "tie-a", PID: 4, LiveSince: t0.Add(time.Hour), EndedAt: t0.Add(time.Hour)},
		{ID: "older", EndedAt: t0},
	}

	var got []string
	for _, s := range LiveFirst(in) {
		got = append(got, s.ID)
	}
	if g, want := strings.Join(got, " "), "early tie-a tie-b late done older"; g != want {
		t.Errorf("LiveFirst() = %q, want %q", g, want)
	}
}

func TestConservativeWindowMatching(t *testing.T) {
	client := func(title, address string) window {
		w := window{Title: title, Address: address}
		w.Workspace.ID = 7
		return w
	}
	live := Session{Provider: Codex, ID: "codex-id", PID: 42, Entrypoint: "cli", Title: "Plan support", CWD: "/work/sessions"}
	for _, tc := range []struct {
		name    string
		s       Session
		clients []window
		want    bool
	}{
		{"project suffix", live, []window{client("Plan support | sessions", "0x1")}, true},
		{"activity prefix", live, []window{client("⠂ Plan support | sessions", "0x1")}, true},
		{"idle prefix", live, []window{client("✳ Plan support | sessions", "0x1")}, true},
		{"duplicate titles", live, []window{client("Plan support | sessions", "0x1"), client("Plan support | sessions", "0x2")}, false},
		{"duplicate after normalization", live, []window{client("⠂ Plan support | sessions", "0x1"), client("✳ Plan support | sessions", "0x2")}, false},
		{"two possible windows", live, []window{client("Plan support", "0x1"), client("Plan support | sessions", "0x2")}, false},
		{"project alone", live, []window{client("sessions", "0x1")}, false},
		{"unmatched", live, []window{client("Other | sessions", "0x1")}, false},
		{"empty", live, nil, false},
		{"origin is not attachment", Session{Provider: Codex, Origin: "cli", Title: live.Title, CWD: live.CWD}, []window{client("Plan support | sessions", "0x1")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := matchWindow(uniqueWindows(tc.clients), tc.s)
			if ok != tc.want {
				t.Fatalf("matched=%v want=%v", ok, tc.want)
			}
		})
	}
	other := live
	other.ID = "same-title-different-session"
	all := []Session{live, other}
	assignWindows(all, uniqueWindows([]window{client("Plan support | sessions", "0x1")}))
	if all[0].WindowAddress != "" || all[1].WindowAddress != "" {
		t.Fatal("one window assigned to two live sessions")
	}
	all = []Session{live}
	assignWindows(all, uniqueWindows([]window{client("Plan support | sessions", "0x1")}))
	if all[0].WindowAddress != "0x1" || all[0].Workspace != 7 {
		t.Fatalf("unique window not assigned: %+v", all)
	}
	if got := stripStatusGlyph("[review] fix"); got != "[review] fix" {
		t.Fatalf("stripped title punctuation: %q", got)
	}
}

func TestAmbiguousWindowClaimsAreNotDiscarded(t *testing.T) {
	all := []Session{
		{Provider: Claude, ID: "a", PID: 1, Entrypoint: "cli", Name: "first", Title: "shared"},
		{Provider: Codex, ID: "b", PID: 2, Entrypoint: "cli", Title: "shared"},
	}
	assignWindows(all, map[string]win{"first": {Address: "0x1"}, "shared": {Address: "0x2"}})
	if all[0].WindowAddress != "" || all[1].WindowAddress != "" {
		t.Fatalf("ambiguous claim became someone else's window: %+v", all)
	}
	if _, ok := matchWindow(map[string]win{"first": {Address: "0x1"}, "shared": {}}, all[0]); ok {
		t.Fatal("unique alias hid a duplicate title")
	}
}
