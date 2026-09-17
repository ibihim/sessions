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
