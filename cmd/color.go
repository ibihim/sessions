package cmd

import (
	"os"
	"strings"

	"github.com/ibihim/sessions/sessions"
)

// The palette is the terminal's own 16 colors, not truecolor: the theme
// that picked your background also picked a green that reads on it.
// Raw escapes rather than a styling library, because that is how this
// package already speaks ANSI (watchLoop, the hidden-rows footer).
const (
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// colorEnabled says whether stdout wants escape codes at all. It asks
// stdout itself rather than the io.Writer being printed to, because the
// writer is sometimes a frame buffer (--watch) or a test buffer — what
// matters is where the bytes finally land.
//
// NO_COLOR is honored per spec: set and non-empty means off, which is
// also what Getenv's empty string collapses to.
func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// dimLine wraps a whole line in dim — headers, prompt echoes, footers:
// text that should be present but not compete.
func dimLine(line string) string { return ansiDim + line + ansiReset }

// whereAt is the rune column where the WHERE cell begins, read off the
// header. Every row is aligned to the header, and the header is ASCII,
// so the byte index doubles as the rune index.
func whereAt(header string) int { return strings.Index(header, "WHERE") }

// paintRow colors one aligned table row: the status marker by state, and
// everything from the WHERE column on in dim.
//
// It runs after tabwriter has flushed, which is the entire trick:
// tabwriter measures escape codes as cell width, so color added before
// alignment skews every column it touches. Injected into a padded line,
// zero-width bytes cannot move anything.
//
// WHERE is dimmed first, by rune offset into the pristine line — the
// marker substitution below inserts runes ahead of it and would shift
// the offset. The substitution itself is safe on first match because
// the status column precedes both TITLE and WHERE, the only cells that
// could carry a stray ● or —.
func paintRow(line string, s sessions.Session, at int) string {
	if r := []rune(line); at > 0 && at < len(r) {
		line = string(r[:at]) + ansiDim + string(r[at:]) + ansiReset
	}
	// Same order as status(): the color must never disagree with the label.
	switch {
	case !s.Live():
		line = strings.Replace(line, "—", ansiDim+"—"+ansiReset, 1)
	case !s.Attached():
		line = strings.Replace(line, "●", ansiYellow+"●"+ansiReset, 1)
	default:
		line = strings.Replace(line, "●", ansiGreen+"●"+ansiReset, 1)
	}
	return line
}
