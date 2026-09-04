package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newOpenCmd() *cobra.Command {
	var (
		window bool
		yolo   bool
		fork   bool
	)

	cmd := &cobra.Command{
		Use:   "open <session>",
		Short: "Open a session: focus its window, or resume it in this terminal",
		Long: "Takes an id prefix or part of a title — whatever is enough to " +
			"name one session.\n\n" +
			"A running session already has a window, so it is focused, " +
			"switching workspace if needed. A finished one has no window, so " +
			"this terminal becomes it: the process moves to the session's " +
			"directory and hands over to `claude --resume`.\n\n" +
			"With --window a finished session opens in a new ghostty window " +
			"instead, leaving this terminal where it is — which is what you " +
			"want when reopening several sessions off one listing.\n\n" +
			"With --fork the session is branched rather than opened: the " +
			"history replays into a new session id and the original is left " +
			"untouched. Because the fork writes its own file, this is the one " +
			"form of open that also works on a running session — the original " +
			"keeps its window or pid, the fork gets this terminal (or a new " +
			"window with --window).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := sessions.DefaultRoot()
			if err != nil {
				return err
			}
			all, err := sessions.Scan(root)
			if err != nil {
				return err
			}
			sessions.Enrich(cmd.Context(), all)

			s, err := resolve(all, args[0])
			if err != nil {
				return err
			}

			// A fork skips the gates below. Both exist to keep two processes
			// from appending to one session file — the focus instead of a
			// second window, the refusal on a headless pid. A fork writes a
			// file of its own, so the collision they defend against cannot
			// happen, and a running session becomes the most useful thing to
			// fork: it is the one state open cannot otherwise hand you a
			// second copy of.
			if fork {
				if window {
					return openWindow(cmd, s, yolo, fork)
				}
				return resume(cmd, s, fork)
			}
			if s.Attached() {
				fmt.Fprintf(cmd.ErrOrStderr(), "focusing %q on workspace %d\n",
					s.Title, s.Workspace)
				return sessions.Focus(cmd.Context(), s)
			}
			if s.Live() {
				return fmt.Errorf("session %s is running headless (pid %d) — "+
					"there is no window to open", s.ID[:8], s.PID)
			}
			if window {
				return openWindow(cmd, s, yolo, fork)
			}
			return resume(cmd, s, fork)
		},
	}

	cmd.Flags().BoolVarP(&window, "window", "w", false,
		"open in a new ghostty window instead of taking over this terminal")
	cmd.Flags().BoolVar(&yolo, "yolo", true,
		"pass --dangerously-skip-permissions to the resumed session (with --window)")
	cmd.Flags().BoolVarP(&fork, "fork", "f", false,
		"branch into a new session id instead of opening — works even on a running session")

	return cmd
}

// resolve picks the one session a query names.
//
// The listing shows both, so either is to hand: a title is what you
// remember, but an id prefix stays unambiguous when two sessions share a
// name, which happens whenever you ask the same question twice.
func resolve(all []sessions.Session, query string) (sessions.Session, error) {
	var byID, byTitle []sessions.Session
	q := strings.ToLower(query)
	for _, s := range all {
		switch {
		case strings.HasPrefix(s.ID, query):
			byID = append(byID, s)
		case strings.Contains(strings.ToLower(s.Title), q):
			byTitle = append(byTitle, s)
		}
	}

	// An id prefix is a deliberate act; a title match is a guess. Never let
	// the guess outvote the deliberate one.
	matches := byID
	if len(matches) == 0 {
		matches = byTitle
	}

	switch len(matches) {
	case 0:
		return sessions.Session{}, fmt.Errorf(
			"no session matching %q — try `sessions list -a`", query)
	case 1:
		return matches[0], nil
	default:
		// Matches are newest-first. A query loose enough to hit dozens is
		// answered by the recent few plus a count — printing all of them
		// buries the ones you probably meant.
		const show = 8
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d sessions:\n", query, len(matches))
		for _, s := range matches[:min(len(matches), show)] {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "  %s  %-4s  %s\n", s.ID[:8], age(s.EndedAt), title)
		}
		if len(matches) > show {
			fmt.Fprintf(&b, "  … and %d older\n", len(matches)-show)
		}
		b.WriteString("name one by its id prefix")
		return sessions.Session{}, fmt.Errorf("%s", b.String())
	}
}

// openWindow resumes a finished session in a new ghostty window, leaving
// this terminal alone, and says so on stderr.
func openWindow(cmd *cobra.Command, s sessions.Session, yolo, fork bool) error {
	if err := spawnWindow(cmd.Context(), s, yolo, fork); err != nil {
		return err
	}
	verb := "opened"
	if fork {
		verb = "forked"
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s %q in a new window at %s\n", verb, s.Title, s.CWD)
	return nil
}

// spawnWindow does the opening and returns without a word. The picker
// calls it too, and has a screen of its own that stderr would write over.
//
// `+new-window` asks the ghostty already running to open the window, over
// its D-Bus interface, and returns at once — the window belongs to that
// process, not to this one, and outlives it without being detached or
// reparented. Plain `ghostty -e` is the wrong door: it forks a second GTK
// process, which would block here for as long as the session stayed open
// and hold this terminal hostage to the window it just spawned.
//
// -e takes everything after it as the command, so claude's flags reach
// claude rather than ghostty's own parser, unquoted and intact.
//
// --resume <id>, never --continue: this command has already resolved which
// session you meant, and --continue would discard that and take whatever is
// newest in the directory. With two sessions in one checkout — a worktree,
// or the same question asked twice — that is reliably the wrong one.
func spawnWindow(ctx context.Context, s sessions.Session, yolo, fork bool) error {
	if s.CWD == "" {
		return fmt.Errorf("session %s records no directory to resume in", shortID(s))
	}
	if _, err := exec.LookPath("ghostty"); err != nil {
		return fmt.Errorf("ghostty is not on PATH: %w", err)
	}

	// claude is resolved to an absolute path here rather than handed to
	// ghostty as a bare name for it to look up, because the two of us do not
	// share a PATH. The window inherits its environment from the ghostty
	// daemon, which was started at login; this process inherits yours. A
	// claude installed under ~/.local/bin is on one and not the other.
	//
	// Getting this wrong is not a visible failure, which is why it is worth
	// the sentence: when -e cannot be executed ghostty falls back to a plain
	// shell, and +new-window has already returned success by then. The window
	// opens in the right directory, with no session in it, and this command
	// congratulates you.
	//
	// Resolving here also makes the lookup honest. Our PATH is a poor guess
	// at the daemon's, but an absolute path needs no guessing from anyone.
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude is not on PATH: %w", err)
	}

	claude := []string{bin, "--resume", s.ID}
	if fork {
		claude = append(claude, "--fork-session")
	}
	if yolo {
		claude = append(claude, "--dangerously-skip-permissions")
	}
	argv := append([]string{
		"+new-window", "--working-directory=" + s.CWD, "-e",
	}, claude...)

	out, err := exec.CommandContext(ctx, "ghostty", argv...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("opening window: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resume hands this terminal over to a finished session.
//
// A process cannot change its parent shell's directory — but it does not
// need to. It moves itself, then execs claude in place, so the terminal
// you typed in becomes the session and your shell's own directory is
// untouched when you exit.
//
// The move matters: --resume resolves a session id under the project
// directory derived from the cwd, so running it anywhere else reports the
// session as missing.
func resume(cmd *cobra.Command, s sessions.Session, fork bool) error {
	if s.CWD == "" {
		return fmt.Errorf("session %s records no directory to resume in", s.ID[:8])
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude is not on PATH: %w", err)
	}
	if err := os.Chdir(s.CWD); err != nil {
		return fmt.Errorf("entering %s: %w", s.CWD, err)
	}

	argv := []string{"claude", "--resume", s.ID}
	verb := "resuming"
	if fork {
		argv = append(argv, "--fork-session")
		verb = "forking"
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "%s %q in %s\n", verb, s.Title, s.CWD)
	return syscall.Exec(bin, argv, os.Environ())
}
