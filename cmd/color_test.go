package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ibihim/sessions/sessions"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// paintRow's one hard promise: it inserts escape codes and nothing else.
// Stripped of them, the line must be byte-for-byte what went in — that
// is the alignment guarantee, since the line was padded before painting.
func TestPaintRowOnlyAddsEscapeCodes(t *testing.T) {
	header := "ID        STATUS      WS  AGE  MSGS  TITLE               WHERE"
	at := whereAt(header)

	cases := []struct {
		name   string
		s      sessions.Session
		line   string
		marker string // the escape expected on the status marker
	}{
		{
			name:   "attached",
			s:      sessions.Session{PID: 1, Entrypoint: "cli"},
			line:   "61b1fdc8  ● running   3   now  42    fork open flag      ~/g/s/g/i/pkg",
			marker: ansiGreen + "●",
		},
		{
			name:   "headless",
			s:      sessions.Session{PID: 1},
			line:   "a3f9c210  ● headless  —   2m   7     nightly triage      ~/g/s/g/i/x@fix",
			marker: ansiYellow + "●",
		},
		{
			name:   "finished",
			s:      sessions.Session{},
			line:   "74f4ca61  —           —   3h   118   sessions: add open  ~/g/s/g/i/pkg",
			marker: ansiDim + "—",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			painted := paintRow(tc.line, tc.s, at)
			if got := ansi.ReplaceAllString(painted, ""); got != tc.line {
				t.Fatalf("visible text changed:\n got %q\nwant %q", got, tc.line)
			}
			if !strings.Contains(painted, tc.marker) {
				t.Errorf("status marker not painted: %q missing in %q", tc.marker, painted)
			}
			if !strings.Contains(painted, ansiDim+"~") {
				t.Errorf("WHERE column not dimmed at offset %d in %q", at, painted)
			}
		})
	}
}
