package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// liveTimeout bounds the hyprctl call. Enrich runs on the path of every
// listing, and a compositor that is wedged should cost a listing its
// workspace column, not its output.
const liveTimeout = 2 * time.Second

// Enrich fills the live fields — PID, Name, Status, Workspace — of any
// session whose process is still running, and leaves the rest untouched.
//
// Both sources are best-effort. Neither Claude Code's process registry nor
// Hyprland is documented as an API, and a machine may not run Hyprland at
// all; a session without live data is still a valid session, so failures
// here are dropped rather than returned.
func Enrich(ctx context.Context, all []Session) {
	procs, err := liveProcs()
	if err != nil || len(procs) == 0 {
		return
	}

	for i := range all {
		p, ok := procs[all[i].ID]
		if !ok {
			continue
		}
		all[i].PID = p.PID
		if p.StartedAt > 0 { // absent, it would read as 1970
			all[i].LiveSince = time.UnixMilli(p.StartedAt)
		}
		all[i].Name = p.Name
		all[i].Status = p.Status
		all[i].Entrypoint = p.Entrypoint
	}

	byTitle, err := windowsByTitle(ctx)
	if err != nil {
		return
	}
	// Only an attached session owns a window; a headless run has no title
	// on screen to match, and would at best collide with someone else's.
	for i := range all {
		if !all[i].Attached() {
			continue
		}
		if w, ok := matchWindow(byTitle, all[i]); ok {
			all[i].Workspace = w.Workspace
		}
	}
}

// matchWindow finds the window a session owns, trying the registry name
// and the ai-title. Which of the two Claude Code titles the window with
// is recorded nowhere — windows have been observed showing the ai-title
// even for sessions carrying a (derived) name — so both are tried rather
// than betting on one.
func matchWindow(byTitle map[string]win, s Session) (win, bool) {
	for _, title := range []string{s.Name, s.Title} {
		if title == "" {
			continue
		}
		if w, ok := byTitle[title]; ok {
			return w, true
		}
	}
	return win{}, false
}

// Focus raises a session's window, switching workspace if it is on
// another one.
//
// Dispatch is by address rather than title: a session name is free text,
// and hyprctl matches titles as a regex, so any "(" or "?" in it would
// either fail or match the wrong window.
func Focus(ctx context.Context, s Session) error {
	if s.Name == "" && s.Title == "" {
		return fmt.Errorf("session %s has no name or title to match a window by", s.ID)
	}
	byTitle, err := windowsByTitle(ctx)
	if err != nil {
		return err
	}
	w, ok := matchWindow(byTitle, s)
	if !ok {
		return fmt.Errorf("no window titled %q or %q", s.Name, s.Title)
	}

	// Hyprland 0.56 evaluates dispatch arguments as Lua: hl.dispatch(<arg>).
	// A window it cannot find is only a warning with exit 0, so success is
	// read from the reply, not the exit status.
	cctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()
	expr := fmt.Sprintf(`hl.dsp.focus({ window = "address:%s" })`, w.Address)
	out, err := exec.CommandContext(cctx, "hyprctl", "dispatch", expr).CombinedOutput()
	reply := strings.TrimSpace(string(out))
	if err != nil {
		return fmt.Errorf("focusing window: %w: %s", err, reply)
	}
	if reply != "ok" {
		return fmt.Errorf("focusing window: %s", reply)
	}
	return nil
}

// LiveFirst returns all with running sessions moved to the front. Live
// sessions are the ones you can walk to; everything else is history,
// however recent, and keeps the newest-first order it came in.
//
// The running ones are ordered by when their process started, oldest
// first. Recency is the obvious order and the wrong one: a busy session
// writes a record per tool call, so two busy sessions would trade places
// on every rescan, under the cursor. Process start holds still while a
// session runs, and one that goes live — new or resumed — joins at the
// bottom without moving the rows above it. StartedAt would not do either:
// a resumed session carries its old records, and with them a start that
// may be weeks old.
func LiveFirst(all []Session) []Session {
	var live, rest []Session
	for _, s := range all {
		if s.Live() {
			live = append(live, s)
		} else {
			rest = append(rest, s)
		}
	}
	// Ties go to the id, so the order depends on the sessions alone and
	// not on the order they arrived in.
	sort.Slice(live, func(i, j int) bool {
		if !live[i].LiveSince.Equal(live[j].LiveSince) {
			return live[i].LiveSince.Before(live[j].LiveSince)
		}
		return live[i].ID < live[j].ID
	})

	ordered := make([]Session, 0, len(all))
	ordered = append(ordered, live...)
	return append(ordered, rest...)
}

// liveProc is one entry of Claude Code's live-process registry, at
// ~/.claude/sessions/<pid>.json. The file exists only while the process
// does — it is a liveness signal, not a history.
type liveProc struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	StartedAt  int64  `json:"startedAt"`  // process start, unix milliseconds
	Name       string `json:"name"`       // what Claude Code titles the terminal window
	Status     string `json:"status"`     // "busy" or "idle"; absent for headless runs
	Entrypoint string `json:"entrypoint"` // "cli" for a terminal session, "sdk-cli" headless
}

// liveProcs returns the running sessions, keyed by session id.
func liveProcs() (map[string]liveProc, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	if err != nil {
		return nil, err
	}

	procs := make(map[string]liveProc, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p liveProc
		if err := json.Unmarshal(data, &p); err != nil || p.SessionID == "" {
			continue
		}
		procs[p.SessionID] = p
	}
	return procs, nil
}

// window is the part of a Hyprland client this package reads.
type window struct {
	Title     string `json:"title"`
	Address   string `json:"address"`
	Workspace struct {
		ID int `json:"id"`
	} `json:"workspace"`
}

// win is what a matched window tells us: where it is, and how to name it
// back to Hyprland unambiguously.
type win struct {
	Workspace int
	Address   string
}

// windowsByTitle maps window titles to the window showing them.
//
// The join key is the title because the process tree cannot answer this.
// A terminal like ghostty serves every window from one process, so a
// session's ancestry dead-ends at a PID shared by unrelated windows on
// unrelated workspaces. Claude Code sets a per-session terminal title —
// the ai-title or the registry name — which survives that multiplexing
// intact.
//
// Titles that appear more than once are dropped: two sessions named the
// same thing cannot be told apart this way, and a wrong workspace is worse
// than none.
func windowsByTitle(ctx context.Context) (map[string]win, error) {
	ctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "hyprctl", "clients", "-j").Output()
	if err != nil {
		return nil, fmt.Errorf("querying hyprland: %w", err)
	}
	var clients []window
	if err := json.Unmarshal(out, &clients); err != nil {
		return nil, fmt.Errorf("parsing hyprland clients: %w", err)
	}

	seen := make(map[string]win, len(clients))
	dupes := make(map[string]bool)
	for _, c := range clients {
		title := stripStatusGlyph(c.Title)
		if title == "" {
			continue
		}
		if _, ok := seen[title]; ok {
			dupes[title] = true
			continue
		}
		seen[title] = win{Workspace: c.Workspace.ID, Address: c.Address}
	}
	for title := range dupes {
		delete(seen, title)
	}
	return seen, nil
}

// stripStatusGlyph removes the activity indicator Claude Code prefixes to
// the terminal title: a braille spinner frame while a turn runs ("⠂ Fix
// the bug"), "✳" while the session waits. Rather than enumerate glyphs
// that are free to change between releases, everything up to the first
// letter or digit goes — an ai-title always starts with one.
func stripStatusGlyph(title string) string {
	return strings.TrimLeftFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
