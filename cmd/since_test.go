package cmd

import (
	"testing"
	"time"
)

// cet is a fixed offset rather than a loaded location: the assertions are
// about which midnight is meant, and a zone whose rules can change with the
// tzdata on the box would make them about something else.
var cet = time.FixedZone("CET", 2*60*60)

// nine on a Friday morning. The hour matters: it is far enough into the day
// that a rolling window and a calendar day give visibly different answers.
var now = time.Date(2026, 7, 17, 9, 0, 0, 0, cet)

func TestParseSince(t *testing.T) {
	tests := []struct {
		arg  string
		want time.Time
	}{
		{"today", time.Date(2026, 7, 17, 0, 0, 0, 0, cet)},
		{"yesterday", time.Date(2026, 7, 16, 0, 0, 0, 0, cet)},
		{"Yesterday", time.Date(2026, 7, 16, 0, 0, 0, 0, cet)},
		{"  yesterday  ", time.Date(2026, 7, 16, 0, 0, 0, 0, cet)},
		{"2026-07-16", time.Date(2026, 7, 16, 0, 0, 0, 0, cet)},
		{"90m", time.Date(2026, 7, 17, 7, 30, 0, 0, cet)},
		{"4h", time.Date(2026, 7, 17, 5, 0, 0, 0, cet)},
		{"24h", time.Date(2026, 7, 16, 9, 0, 0, 0, cet)},
		{"0d", now},
		{"2d", time.Date(2026, 7, 15, 9, 0, 0, 0, cet)},
		{"30d", time.Date(2026, 6, 17, 9, 0, 0, 0, cet)},
	}

	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			got, err := parseSince(tc.arg, now)
			if err != nil {
				t.Fatalf("parseSince(%q) = error %v, want %s", tc.arg, err, tc.want)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseSince(%q) = %s, want %s", tc.arg, got, tc.want)
			}
		})
	}
}

// A bare date is the case Parse would quietly get wrong: it reads the date as
// UTC, which lands two hours into the wrong side of a CET midnight and drops
// anything worked on in that gap.
func TestParseSinceDateIsLocalMidnight(t *testing.T) {
	got, err := parseSince("2026-07-16", now)
	if err != nil {
		t.Fatalf("parseSince: %v", err)
	}
	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
		t.Errorf("got %s, want midnight", got)
	}
	if got.Location().String() != cet.String() {
		t.Errorf("got location %s, want %s", got.Location(), cet)
	}
}

// The reason --since parses two shapes rather than one. Asked at 09:00, the
// rolling day and the calendar's yesterday are nine hours apart — the nine
// hours of yesterday morning that "24h" cannot see.
func TestParseSinceRollingIsNotCalendar(t *testing.T) {
	rolling, err := parseSince("24h", now)
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := parseSince("yesterday", now)
	if err != nil {
		t.Fatal(err)
	}
	if gap := rolling.Sub(calendar); gap != 9*time.Hour {
		t.Errorf("24h starts %s after yesterday, want 9h", gap)
	}
}

func TestParseSinceRejects(t *testing.T) {
	// "2w" and "tomorrow" are the plausible near-misses; the rest are the
	// ways the day-suffix shortcut could misfire if it were sloppier.
	for _, arg := range []string{
		"", "d", "-2d", "1.5d", "2days", "2w", "tomorrow", "-4h",
		"2026-13-01", "16-07-2026", "lunchtime",
	} {
		t.Run(arg, func(t *testing.T) {
			if got, err := parseSince(arg, now); err == nil {
				t.Errorf("parseSince(%q) = %s, want error", arg, got)
			}
		})
	}
}
