package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSince turns a --since argument into the instant a session must have
// been touched at or after.
//
// Two shapes, because "since" asks two different questions. A duration is a
// rolling window — "what am I in the middle of?" — and counts back from now.
// A day name or a date is a calendar boundary — "what did I do yesterday?" —
// and snaps to local midnight. The two are not interchangeable: at 09:00,
// `--since 24h` starts nine hours later than `--since yesterday` and hides a
// whole morning's work.
//
// now is a parameter rather than a time.Now() call inside, so the calendar
// cases can be asserted against a fixed clock.
func parseSince(arg string, now time.Time) (time.Time, error) {
	arg = strings.TrimSpace(arg)

	switch strings.ToLower(arg) {
	case "today":
		return midnight(now), nil
	case "yesterday":
		return midnight(now).AddDate(0, 0, -1), nil
	}

	// Days are handled before ParseDuration rather than left to it, because
	// its units stop at "h" — it has no "d". Untouched, the overwhelmingly
	// obvious `--since 2d` would be a parse error rather than two days.
	//
	// AddDate rather than 2*24h: across a daylight-saving change, "two days
	// ago" means the same time on that day's clock, not 48 hours exactly.
	if digits, ok := strings.CutSuffix(arg, "d"); ok {
		if days, err := strconv.Atoi(digits); err == nil && days >= 0 {
			return now.AddDate(0, 0, -days), nil
		}
	}

	if d, err := time.ParseDuration(arg); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("--since %s asks for the future", arg)
		}
		return now.Add(-d), nil
	}

	// ParseInLocation, not Parse: Parse reads a bare date as UTC, which puts
	// the boundary hours away from the midnight you had in mind.
	if t, err := time.ParseInLocation("2006-01-02", arg, now.Location()); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf(
		"cannot read %q as a time — try a duration (90m, 4h, 2d), "+
			"a day (today, yesterday), or a date (2006-01-02)", arg)
}

// midnight is the start of now's day in now's own location: "yesterday" is a
// question about the clock on your wall, not the one in UTC.
func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
