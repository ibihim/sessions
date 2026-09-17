package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ibihim/sessions/sessions"
)

func newOpenCmd() *cobra.Command {
	var window, yolo, fork bool
	cmd := &cobra.Command{
		Use:   "open <session>",
		Short: "Open a session: focus its window, or resume it in this terminal",
		Long: "Select by ID prefix, title, or provider:id-prefix (codex:3f2a). " +
			"Running sessions focus their uniquely identified terminal window. " +
			"Saved sessions resume in their original directory using claude --resume " +
			"or codex resume. A running session without an identified window is an error.\n\n" +
			"--window opens a new Ghostty window for a saved session. --fork creates " +
			"a new session, leaving the original intact, and also works on live sessions. " +
			"Permission bypass flags apply only with --window; use --yolo=false to disable them.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, all, err := loadCatalog(cmd)
			if err != nil {
				return err
			}
			s, err := resolve(all, args[0])
			if err != nil {
				return err
			}
			action, err := openingAction(s, fork)
			if err != nil {
				return err
			}
			if action == "focus" {
				fmt.Fprintf(cmd.ErrOrStderr(), "focusing %q on workspace %d\n", s.Title, s.Workspace)
				return sessions.Focus(cmd.Context(), s)
			}
			if window {
				return openWindow(cmd, s, yolo, fork)
			}
			return resume(cmd, s, fork)
		},
	}
	cmd.Flags().BoolVarP(&window, "window", "w", false, "open in a new ghostty window instead of taking over this terminal")
	cmd.Flags().BoolVar(&yolo, "yolo", true, "bypass the selected tool's permissions and sandbox (with --window)")
	cmd.Flags().BoolVarP(&fork, "fork", "f", false, "branch into a new session ID; also works on a running session")
	return cmd
}

// openingAction is shared with the picker so neither can start a second writer.
func openingAction(s sessions.Session, fork bool) (string, error) {
	switch {
	case fork:
		return "fork", nil
	case s.Live():
		if s.Attached() && s.WindowAddress != "" {
			return "focus", nil
		}
		return "", fmt.Errorf("session %s is running (pid %d), but its terminal window cannot be identified; use --fork for a new session", s.Key(), s.PID)
	case s.LiveState == sessions.LiveUnknown:
		return "", fmt.Errorf("live status for %s is unavailable; cannot safely resume it; use --fork for a new session", s.Key())
	default:
		return "resume", nil
	}
}

func resolve(all []sessions.Session, query string) (sessions.Session, error) {
	original := query
	provider := sessions.All
	if prefix, rest, ok := strings.Cut(query, ":"); ok && (prefix == "claude" || prefix == "codex" || prefix == "all") {
		var err error
		provider, err = sessions.ParseProvider(prefix)
		if err != nil || provider == sessions.All {
			return sessions.Session{}, fmt.Errorf("invalid selector %q: use claude:<id-prefix> or codex:<id-prefix>", original)
		}
		query = rest
	}
	if strings.TrimSpace(query) == "" {
		return sessions.Session{}, fmt.Errorf("empty session selector")
	}
	var byID, byTitle []sessions.Session
	q := strings.ToLower(query)
	for _, s := range all {
		if provider != sessions.All && s.Tool() != provider {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToLower(s.ID), q):
			byID = append(byID, s)
		case strings.Contains(strings.ToLower(s.Title), q):
			byTitle = append(byTitle, s)
		}
	}
	matches := byID
	if len(matches) == 0 {
		matches = byTitle
	}
	switch len(matches) {
	case 0:
		return sessions.Session{}, fmt.Errorf("no session matching %q — try `sessions list -a`", original)
	case 1:
		return matches[0], nil
	default:
		return sessions.Session{}, ambiguous(original, matches)
	}
}

func ambiguous(query string, matches []sessions.Session) error {
	const show = 8
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d sessions:\n", query, len(matches))
	for _, s := range matches[:min(len(matches), show)] {
		fmt.Fprintf(&b, "  %s  %-4s  %s\n", s.Key(), age(s.EndedAt), title(s))
	}
	if len(matches) > show {
		fmt.Fprintf(&b, "  … and %d older\n", len(matches)-show)
	}
	b.WriteString("select one with provider:id-prefix")
	return fmt.Errorf("%s", b.String())
}

// launch keeps executable, arguments and CWD separate: session text is never
// interpreted by a shell. Construction can be tested without starting a CLI.
type launch struct {
	Executable    string
	Args          []string
	CWD           string
	Env           []string // explicit per-window overrides; Ghostty has its own environment
	EnvExecutable string
}

func buildLaunch(s sessions.Session, window, yolo, fork bool) (launch, error) {
	if s.ID == "" || strings.HasPrefix(s.ID, "-") {
		return launch{}, fmt.Errorf("invalid session ID %q", s.ID)
	}
	if s.CWD == "" {
		return launch{}, fmt.Errorf("session %s records no directory to resume in", s.Key())
	}
	var args []string
	var permissionFlag string
	switch s.Tool() {
	case sessions.Claude:
		args = []string{"--resume", s.ID}
		if fork {
			args = append(args, "--fork-session")
		}
		permissionFlag = "--dangerously-skip-permissions"
	case sessions.Codex:
		verb := "resume"
		if fork {
			verb = "fork"
		}
		args = []string{verb, s.ID}
		permissionFlag = "--dangerously-bypass-approvals-and-sandbox"
	default:
		return launch{}, fmt.Errorf("unsupported provider %q", s.Provider)
	}
	if window && yolo {
		args = append(args, permissionFlag)
	}
	return launch{Executable: string(s.Tool()), Args: args, CWD: s.CWD}, nil
}

func prepareLaunch(s sessions.Session, window, yolo, fork bool) (launch, error) {
	l, err := buildLaunch(s, window, yolo, fork)
	if err != nil {
		return l, err
	}
	info, err := os.Stat(l.CWD)
	if err != nil {
		return l, fmt.Errorf("session directory %s: %w", l.CWD, err)
	}
	if !info.IsDir() {
		return l, fmt.Errorf("session directory %s is not a directory", l.CWD)
	}
	bin, err := exec.LookPath(l.Executable)
	if err != nil {
		return l, fmt.Errorf("%s is not on PATH: %w", l.Executable, err)
	}
	// Ghostty's daemon may have a different PATH. A relative executable would
	// also change meaning after we enter the session directory.
	l.Executable, err = filepath.Abs(bin)
	if err != nil {
		return l, err
	}
	if window && s.Tool() == sessions.Codex {
		home, err := sessions.DefaultCodexHome()
		if err != nil {
			return l, err
		}
		l.Env = []string{"CODEX_HOME=" + home}
		env, err := exec.LookPath("env")
		if err != nil {
			return l, fmt.Errorf("env is not on PATH: %w", err)
		}
		l.EnvExecutable, err = filepath.Abs(env)
		if err != nil {
			return l, err
		}
	}
	return l, err
}

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

func windowArgs(l launch) []string {
	args := []string{"+new-window", "--working-directory=" + l.CWD, "-e"}
	if len(l.Env) > 0 {
		// +new-window does not forward Ghostty's --env configuration to
		// the daemon. env is part of the actual executed argv instead.
		args = append(args, l.EnvExecutable)
		args = append(args, l.Env...)
	}
	args = append(args, l.Executable)
	return append(args, l.Args...)
}

func spawnWindow(ctx context.Context, s sessions.Session, yolo, fork bool) error {
	l, err := prepareLaunch(s, true, yolo, fork)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath("ghostty")
	if err != nil {
		return fmt.Errorf("ghostty is not on PATH: %w", err)
	}
	out, err := exec.CommandContext(ctx, bin, windowArgs(l)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("opening window: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resume(cmd *cobra.Command, s sessions.Session, fork bool) error {
	l, err := prepareLaunch(s, false, false, fork)
	if err != nil {
		return err
	}
	if err := os.Chdir(l.CWD); err != nil {
		return fmt.Errorf("entering %s: %w", l.CWD, err)
	}
	verb := "resuming"
	if fork {
		verb = "forking"
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s %q in %s\n", verb, s.Title, l.CWD)
	return syscall.Exec(l.Executable, append([]string{l.Executable}, l.Args...), os.Environ())
}
