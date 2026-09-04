package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ibihim/sessions/sessions"
)

// wrap's contract is that nothing is lost and nothing is padded: the words
// that go in come out, in order, and no line carries trailing space.
func TestWrap(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  []string
	}{{
		name:  "short enough to leave alone",
		text:  "fix the bug",
		width: 20,
		want:  []string{"fix the bug"},
	}, {
		name:  "broken on spaces",
		text:  "the quick brown fox jumps",
		width: 12,
		want:  []string{"the quick", "brown fox", "jumps"},
	}, {
		name:  "typed newlines survive as paragraphs",
		text:  "first\n\nsecond",
		width: 20,
		want:  []string{"first", "", "second"},
	}, {
		name:  "indentation is carried onto continuations",
		text:  "    func main() { fmt.Println() }",
		width: 20,
		want:  []string{"    func main() {", "    fmt.Println() }"},
	}, {
		name:  "a word longer than the width overflows rather than breaking",
		text:  "see https://github.com/ibihim/sessions/blob/main/sessions/prompts.go now",
		width: 20,
		want:  []string{"see", "https://github.com/ibihim/sessions/blob/main/sessions/prompts.go", "now"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrap(c.text, c.width)
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("wrap =\n  %q\nwant\n  %q", got, c.want)
			}
			for _, line := range got {
				if line != strings.TrimRight(line, " ") {
					t.Errorf("line has trailing space: %q", line)
				}
			}
		})
	}
}

// The gap column is the reason the listing reads as a progression, and it
// is the one number that has to survive between --follow batches.
func TestGap(t *testing.T) {
	base := time.Date(2026, 9, 2, 9, 47, 0, 0, time.UTC)
	cases := []struct {
		name string
		prev time.Time
		at   time.Time
		want string
	}{
		{"first prompt has nothing to measure", time.Time{}, base, "·"},
		{"seconds", base, base.Add(19 * time.Second), "+19s"},
		{"minutes", base, base.Add(9*time.Minute + 30*time.Second), "+9m"},
		{"hours", base, base.Add(3 * time.Hour), "+3h"},
		{"days", base, base.Add(50 * time.Hour), "+2d"},
		{"a record with no clock", base, time.Time{}, "·"},
		{"time running backwards is not a gap", base, base.Add(-time.Hour), "·"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gap(c.prev, c.at); got != c.want {
				t.Errorf("gap = %q, want %q", got, c.want)
			}
		})
	}
}

// followLoop's whole job is to print what arrives after it started, without
// reprinting what was already on screen and without restarting the
// numbering or the gap column.
func TestFollowLoopPrintsOnlyWhatArrives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	at := time.Date(2026, 9, 2, 9, 47, 0, 0, time.UTC)
	emit := func(text string, when time.Time) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		line := `{"type":"user","timestamp":"` + when.Format(time.RFC3339) +
			`","message":{"content":"` + text + `"}}` + "\n"
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}

	emit("already here", at)
	reader := sessions.NewPromptReader(path)
	first, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	var before strings.Builder
	prev := writePrompts(&before, first, time.Time{}, false, 80-gutter)

	// The loop runs until the context is cancelled, so the append has to
	// happen while it is ticking.
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		emit("arrived later", at.Add(90*time.Second))
		time.Sleep(4 * followInterval)
		cancel()
	}()

	var after strings.Builder
	if err := followLoop(ctx, &after, reader, prev, false); err != nil {
		t.Fatal(err)
	}

	got := after.String()
	if strings.Contains(got, "already here") {
		t.Error("reprinted a prompt that was on screen before the loop started")
	}
	if !strings.Contains(got, "arrived later") {
		t.Fatalf("did not print the prompt that arrived; got %q", got)
	}
	// Numbering continues from the first batch, and the gap is measured
	// against it rather than restarting at the bullet.
	if !strings.Contains(got, "  2  ") {
		t.Errorf("numbering restarted; got %q", got)
	}
	if !strings.Contains(got, "+1m") {
		t.Errorf("gap did not survive the batch boundary; got %q", got)
	}
}
