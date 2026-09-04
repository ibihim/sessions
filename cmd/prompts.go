package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/ibihim/sessions/sessions"
)

// followInterval is how often --follow looks for new prompts. A prompt
// arrives every few minutes at best, so this is not a poll for freshness
// but for latency: it is the delay between pressing enter on the left and
// seeing it appear on the right.
const followInterval = 500 * time.Millisecond

// gutter is the printed width of the "  N  HH:MM  +Xm  " prefix.
// Continuation lines are indented by it, so a wrapped prompt reads as one
// block instead of drifting back under the numbers.
const gutter = 18

func newPromptsCmd() *cobra.Command {
	var (
		follow bool
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "prompts [session]",
		Short: "Replay what you asked in a session, oldest first",
		Long: "Prints your side of a conversation — the prompts you typed, the " +
			"slash commands you ran, and the points where you interrupted — " +
			"oldest at the top, so the thread reads as the progression it " +
			"was.\n\n" +
			"With no argument this is the session started in the current " +
			"directory, preferring one that is still running: the pane next " +
			"door. Name a session by id prefix or title to read any other.\n\n" +
			"With --follow it keeps printing as you type, appending rather " +
			"than redrawing, so what has already scrolled past stays where it " +
			"is. It stays on the session it resolved at startup — a new " +
			"session needs a new invocation, which is the point: you always " +
			"know whose prompts you are reading.\n\n" +
			"Nothing is ever truncated. Prompts wrap to the terminal, which is " +
			"the one thing a status line cannot do.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if follow && asJSON {
				return fmt.Errorf("--follow and --json cannot be combined: " +
					"the JSON is one document, and a document does not end " +
					"while the session is still writing")
			}

			root, err := sessions.DefaultRoot()
			if err != nil {
				return err
			}
			all, err := sessions.Scan(root)
			if err != nil {
				return err
			}
			// Enrich is what makes "the running one" resolvable; without it
			// every session looks equally finished.
			sessions.Enrich(cmd.Context(), all)

			s, err := pick(all, args)
			if err != nil {
				return err
			}
			path, err := sessions.TranscriptPath(root, s.ID)
			if err != nil {
				return err
			}

			reader := sessions.NewPromptReader(path)
			first, err := reader.Next()
			if err != nil {
				return err
			}

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), "prompts", first)
			}

			// The banner names whose prompts these are, on stderr — the same
			// place open announces what it is about to do, and out of the way
			// of anything piping the prompts themselves.
			fmt.Fprintln(cmd.ErrOrStderr(), banner(s, follow))

			out := cmd.OutOrStdout()
			color := colorEnabled()
			prev := writePrompts(out, first, time.Time{}, color, textWidth())
			if !follow {
				return nil
			}
			return followLoop(cmd.Context(), out, reader, prev, color)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false,
		"keep printing prompts as you type them")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of text")

	return cmd
}

// pick chooses the session to read.
//
// An explicit query is resolved the way open resolves it. With none, it is
// the session belonging to this directory — preferring one still attached
// to a terminal, because that is the pane you have open next door, and
// falling back to the most recent when none is running.
func pick(all []sessions.Session, args []string) (sessions.Session, error) {
	if len(args) == 1 {
		return resolve(all, args[0])
	}

	cwd, err := os.Getwd()
	if err != nil {
		return sessions.Session{}, fmt.Errorf("locating working directory: %w", err)
	}

	var newest *sessions.Session
	for i := range all { // Scan returns newest first
		if all[i].CWD != cwd {
			continue
		}
		if all[i].Attached() {
			return all[i], nil
		}
		if newest == nil {
			newest = &all[i]
		}
	}
	if newest != nil {
		return *newest, nil
	}
	return sessions.Session{}, fmt.Errorf("no session started in %s — name one "+
		"by id prefix or title, or run this from a session's directory", cwd)
}

// followLoop prints prompts as the session appends them, until ctrl-c.
//
// It appends where watchLoop redraws. This view earns its pane by keeping
// the thread you have already scrolled past; clearing the screen every tick
// would throw away exactly the thing worth watching.
func followLoop(ctx context.Context, w io.Writer, r *sessions.PromptReader, prev time.Time, color bool) error {
	tick := time.NewTicker(followInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}

		ps, err := r.Next()
		if err != nil {
			return err
		}
		prev = writePrompts(w, ps, prev, color, textWidth())
	}
}

// writePrompts renders a batch and returns the time to measure the next
// batch's first gap against — follow calls this once per arrival, and the
// gap has to survive between calls.
//
// width is how far a prompt may run past the gutter. It is handed in
// rather than asked of the terminal here, because the picker renders the
// same thread into a pane, and a pane has no tty to ask.
func writePrompts(w io.Writer, ps []sessions.Prompt, prev time.Time, color bool, width int) time.Time {
	pad := strings.Repeat(" ", gutter)

	for _, p := range ps {
		lead := fmt.Sprintf("%3d  %s  %-5s ", p.N, clock(p.At), gap(prev, p.At))
		if color {
			lead = dimLine(lead)
		}

		body := p.Text
		switch {
		case p.Kind == sessions.KindInterrupt:
			// The transcript's own wording is a sentence about a request;
			// what you want to see is the moment you hit escape.
			body = "⎋ interrupted"
		case p.Image && !strings.Contains(body, "[Image #"):
			// Claude Code usually leaves its own placeholder in the text. The
			// marker is for the prompts where it did not, so that a pasted
			// screenshot is never silently absent from the record.
			body += "  [+image]"
		}

		lines := wrap(body, width)
		for i, line := range lines {
			// Slash commands and interrupts are what you did, not what you
			// said. Dimmed, they mark the thread without competing with it.
			if color && p.Kind != sessions.KindPrompt {
				line = dimLine(line)
			}
			switch {
			case i == 0:
				fmt.Fprintln(w, lead+line)
			case line == "":
				// A blank line you typed is a paragraph break. Padding it
				// would leave trailing spaces that break copy-paste out of
				// the pane and show up as whitespace noise in a diff.
				fmt.Fprintln(w)
			default:
				fmt.Fprintln(w, pad+line)
			}
		}
		fmt.Fprintln(w)

		if !p.At.IsZero() {
			prev = p.At
		}
	}
	return prev
}

// banner names the session being read, so a pane left running overnight
// can still answer "whose prompts are these".
func banner(s sessions.Session, follow bool) string {
	title := s.Title
	if title == "" {
		title = s.Name
	}
	if title == "" {
		title = "(untitled)"
	}
	line := fmt.Sprintf("%s  %s  %s", shortID(s), title, where(s))
	if follow {
		line += "  — following, ctrl-c to stop"
	}
	if colorEnabled() {
		return dimLine(line)
	}
	return line
}

// clock is the wall time a prompt was sent, or blank of the same width
// when the record carried none — the column stays put either way.
func clock(t time.Time) string {
	if t.IsZero() {
		return "     "
	}
	return t.Local().Format("15:04")
}

// gap says how long you thought before this prompt.
//
// The gaps are what turn a list into a progression: a minute means you were
// correcting, an hour means you left and came back to it. The first prompt
// has nothing to measure against and gets a bullet.
func gap(prev, at time.Time) string {
	if prev.IsZero() || at.IsZero() || at.Before(prev) {
		return "·"
	}
	d := at.Sub(prev)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("+%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("+%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("+%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("+%dd", int(d.Hours()/24))
	}
}

// wrap breaks text into lines of at most width runes, splitting on spaces.
//
// Newlines already in the prompt are kept — you typed them, so they are
// paragraph breaks — and each source line's leading whitespace is carried
// onto its continuations, so a pasted code block keeps its shape instead of
// being flattened into prose. A single word longer than width, a URL or a
// path, is left to overflow: breaking it would make it uncopyable, which is
// worse than a ragged edge.
func wrap(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}

		cur := indent + words[0]
		for _, word := range words[1:] {
			if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) > width {
				out = append(out, cur)
				cur = indent + word
				continue
			}
			cur += " " + word
		}
		out = append(out, cur)
	}
	return out
}

// textWidth is how wide a prompt may run before wrapping: the terminal,
// less the gutter and a little air on the right.
//
// The width is asked of stdout rather than assumed, because not truncating
// is the entire point of this view. A pipe or a file has no width to give,
// and 80 is the conventional answer for those.
func textWidth() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80 - gutter
	}
	return textWidthFor(int(ws.Col))
}

// textWidthFor is the text width of a pane cols wide.
//
// A pane narrower than the gutter plus a few words is not a pane worth
// reflowing for; wrapping to something unreadably thin is worse than
// running over the edge.
func textWidthFor(cols int) int {
	return max(cols-gutter-2, 24)
}
