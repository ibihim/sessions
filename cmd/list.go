package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newListCmd() *cobra.Command {
	var (
		n        int
		all      bool
		since    string
		watch    bool
		interval time.Duration
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the Claude Code and Codex sessions you can return to",
		Long: "Shows the sessions running in a terminal right now, with the " +
			"Hyprland workspace each one sits on — they do not need resuming, " +
			"only finding. Use --all for finished sessions too, newest first.\n\n" +
			"--since asks what you have worked on: `--since yesterday` for a " +
			"calendar day, `--since 4h` for a rolling window. It covers " +
			"finished sessions on its own, since a time window over only the " +
			"running ones would answer nothing.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch && asJSON {
				return fmt.Errorf("--watch redraws a screen; it cannot also emit JSON")
			}
			if watch && interval <= 0 {
				return fmt.Errorf("--interval must be positive with --watch")
			}
			// --since is a question about history, and the default listing is
			// only the live sessions — every one of which is recent by
			// definition. Answering it against those alone would be a filter
			// with nothing to remove.
			if since != "" {
				all = true
			}
			// Decided once, against the real stdout: under --watch the frames
			// render into a builder first, but the bytes end on the terminal.
			color := colorEnabled()

			render := func(w io.Writer) error {
				_, found, err := loadCatalog(cmd)
				if err != nil {
					return err
				}

				if !all {
					found = attached(found)
				}
				if since != "" {
					// Parsed per frame rather than once: under --watch a
					// rolling window has to keep rolling, or an hour in,
					// `--since 4h` is quietly showing you five.
					cutoff, err := parseSince(since, time.Now())
					if err != nil {
						return err
					}
					found = endedSince(found, cutoff)
				}
				found = sessions.LiveFirst(found)

				hidden := 0
				if n > 0 && len(found) > n {
					hidden = len(found) - n
					found = found[:n]
				}

				if asJSON {
					return writeJSON(w, "sessions", found)
				}
				return writeSessionsText(w, found, all, since, hidden, color)
			}

			if watch {
				return watchLoop(cmd.Context(), cmd.OutOrStdout(), interval, render)
			}
			return render(cmd.OutOrStdout())
		},
	}

	cmd.Flags().IntVarP(&n, "n", "n", 20, "show at most N sessions (0 for all)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include finished and headless sessions")
	cmd.Flags().StringVar(&since, "since", "", "only sessions touched since a duration (4h, 2d), a day (today, yesterday), or a date (2006-01-02); implies --all")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "redraw until interrupted")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "redraw every (with --watch)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of text")

	return cmd
}

// watchLoop redraws until the context is cancelled.
//
// The frame is built in memory and written in one call, so a slow scan
// cannot leave a half-drawn table on screen. Clearing and redrawing beats
// an alternate screen buffer here: when you quit, the last state stays in
// your scrollback, which is usually the thing you opened it to read.
func watchLoop(ctx context.Context, w io.Writer, every time.Duration, render func(io.Writer) error) error {
	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		var frame strings.Builder
		if err := render(&frame); err != nil {
			return err
		}
		fmt.Fprint(w, "\033[H\033[2J", frame.String())
		fmt.Fprintf(w, "\n\033[2mwatching every %s — ctrl-c to stop\033[0m\n", every)

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// attached keeps only the sessions running in a terminal. A finished
// session is history, and a headless `claude -p` run is a subprocess with
// no window — neither is something to return to.
func attached(all []sessions.Session) []sessions.Session {
	kept := make([]sessions.Session, 0, len(all))
	for _, s := range all {
		if s.Attached() {
			kept = append(kept, s)
		}
	}
	return kept
}

// endedSince keeps the sessions last touched at or after cutoff.
//
// The cut is on EndedAt rather than StartedAt: "what did I work on
// yesterday" means the work, not the opening. A session begun last week and
// picked up again yesterday is one you worked on yesterday — and, being
// long-lived, is likelier than most to be the one you want back.
//
// A session whose timestamps would not parse keeps a zero EndedAt, which is
// before every cutoff and so falls out here. That is the right answer: a
// session with no clock cannot claim to be from yesterday.
func endedSince(all []sessions.Session, cutoff time.Time) []sessions.Session {
	kept := make([]sessions.Session, 0, len(all))
	for _, s := range all {
		if !s.EndedAt.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	return kept
}

// writeSessionsText renders the listing. hidden is how many rows the -n cap
// removed, so the table can admit to being partial.
func writeSessionsText(w io.Writer, found []sessions.Session, all bool, since string, hidden int, color bool) error {
	if len(found) == 0 {
		switch {
		case since != "":
			fmt.Fprintf(w, "No sessions since %s.\n", since)
		case all:
			fmt.Fprintln(w, "No sessions.")
		default:
			fmt.Fprintln(w, "No verified terminal sessions. Use --all to include history and unknown live status.")
		}
		return nil
	}

	// tabwriter measures cells in runes, so the status dot and any
	// non-ASCII in a title do not throw the columns off — fixed-width
	// verbs would pad by byte count and misalign every row carrying one.
	//
	// It also measures escape codes, which is why color is not in these
	// cells: the table renders plain into a buffer, and paintRow injects
	// the codes into the already-aligned lines.
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTOOL\tSTATUS\tWS\tAGE\tMSGS\tTITLE\tWHERE")
	for _, s := range found {
		title := s.Title
		if title == "" {
			// A live session Claude has not titled yet still carries a
			// derived registry name ("linux-63") — thin, but beats nothing.
			title = s.Name
		}
		if title == "" {
			title = "(untitled)"
		}
		// WHERE goes last and unbounded: it is the only column whose
		// length is not ours to choose, and the only one worth reading in
		// full six weeks from now.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			shortID(s), s.Tool(), status(s), workspace(s), age(s.EndedAt), s.Messages,
			truncate(title, 52), where(s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	rows := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if !color {
		for _, row := range rows {
			fmt.Fprintln(w, row)
		}
	} else {
		at := whereAt(rows[0])
		fmt.Fprintln(w, dimLine(rows[0]))
		for i, s := range found {
			fmt.Fprintln(w, paintRow(rows[i+1], s, at))
		}
	}

	// A cap that drops rows without saying so answers "what did I work on"
	// with something that looks complete and is not. The count is the whole
	// fix: a listing may be partial, but it may not pretend otherwise.
	if hidden > 0 {
		more := fmt.Sprintf("… and %d more — use -n 0 for all", hidden)
		if color {
			more = dimLine(more)
		}
		fmt.Fprintln(w, more)
	}
	return nil
}

// shortID renders the prefix you hand to `sessions open`. It leads the
// listing because it is the one column there to be copied, not read.
//
// Eight characters of a uuid separate any two sessions you will ever have
// on disk. The id is a filename, though, and nothing enforces that a
// transcript is named like a uuid — so a short one is printed whole rather
// than sliced out of range.
func shortID(s sessions.Session) string {
	if len(s.ID) <= 8 {
		return s.ID
	}
	return s.ID[:8]
}

// status says what the session is doing, and whether it is anywhere at
// all. A headless run reports no status of its own — it is simply
// running — so it is labelled by what it is.
func status(s sessions.Session) string {
	switch {
	case !s.Live() && s.LiveState == sessions.LiveUnknown:
		return "? unknown"
	case !s.Live():
		return "—"
	case s.Tool() == sessions.Codex:
		return "● running"
	case !s.Attached():
		return "● headless"
	case s.Status == "":
		return "● running"
	default:
		return "● " + s.Status
	}
}

func workspace(s sessions.Session) string {
	if s.Workspace == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", s.Workspace)
}

// where says which checkout a session is in: the path, plus the branch
// when it adds anything.
//
// A basename alone ("pkg") is legible today and meaningless next month,
// so the whole path is shown — abbreviated the way a shell prompt does
// it, which is how these directories already read in a window title.
// It is the last column, so it can run as long as it needs to.
func where(s sessions.Session) string {
	path := shortPath(s.CWD)
	switch s.Branch {
	case "", "HEAD", "main", "master":
		return path
	}
	// Worktrees get named after their branch, so dir@branch says it twice.
	if s.Branch == filepath.Base(s.CWD) {
		return path
	}
	return path + "@" + s.Branch
}

// shortPath renders a path the way a shell prompt does: relative to home,
// every parent component cut to its first character, the last kept whole.
// /home/ibihim/go/src/github.com/ibihim/sessions reads as ~/g/s/g/i/sessions.
func shortPath(p string) string {
	if p == "" {
		return "?"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = filepath.Join("~", rel)
		}
	}

	parts := strings.Split(p, string(filepath.Separator))
	for i, part := range parts[:max(len(parts)-1, 0)] {
		if part == "" || part == "~" {
			continue
		}
		parts[i] = string([]rune(part)[:1])
	}
	return strings.Join(parts, string(filepath.Separator))
}

// age renders a duration the way you'd say it out loud. On a running
// session it reads as how long it has been waiting for you.
func age(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncate cuts to max runes, not bytes: titles carry the odd non-ASCII
// character, and slicing one in half prints a replacement glyph.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
