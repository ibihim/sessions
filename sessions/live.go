package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// liveTimeout bounds the hyprctl call. Enrich runs on the path of every
// listing, and a compositor that is wedged should cost a listing its
// workspace column, not its output.
const liveTimeout = 2 * time.Second

// Enrich refreshes the default stores. Catalog.Enrich also supports isolated stores.
func Enrich(ctx context.Context, all []Session) error {
	c, err := DefaultCatalog()
	if err != nil {
		return err
	}
	return c.Enrich(ctx, all)
}

// Enrich distinguishes a stopped process from an unavailable detector. Window
// discovery is optional; its failure never changes the process evidence.
func (c Catalog) Enrich(ctx context.Context, all []Session) error {
	needed := map[Provider]bool{}
	for i := range all {
		needed[all[i].Tool()] = true
		all[i].PID, all[i].Workspace = 0, 0
		all[i].LiveSince = time.Time{}
		all[i].Name, all[i].Status, all[i].Entrypoint, all[i].WindowAddress = "", "", "", ""
		all[i].LiveState = LiveUnknown
	}
	var problems []error
	visibilityErr := processVisibility()
	if visibilityErr != nil && len(all) > 0 {
		problems = append(problems, fmt.Errorf("live status incomplete: %w", visibilityErr))
	}
	for _, provider := range []Provider{Claude, Codex} {
		if !needed[provider] {
			continue
		}
		var procs map[string]liveProc
		var err error
		if provider == Claude {
			// Registry PIDs belong to the host namespace. A restricted view
			// cannot establish that its same-numbered PID is that process.
			if visibilityErr == nil {
				procs, err = claudeLiveProcs(filepath.Join(filepath.Dir(c.ClaudeRoot), "sessions"))
			}
		} else {
			procs, err = codexLiveProcs(c.CodexHome)
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s live status unavailable: %w", provider, err))
		}
		for i := range all {
			s := &all[i]
			if s.Tool() != provider {
				continue
			}
			if err == nil && visibilityErr == nil {
				s.LiveState = LiveStopped
			}
			p, ok := procs[s.ID]
			if !ok {
				continue
			}
			s.PID, s.LiveState = p.PID, LiveRunning
			if p.StartedAt > 0 {
				s.LiveSince = time.UnixMilli(p.StartedAt)
			}
			s.Name, s.Status, s.Entrypoint = p.Name, p.Status, p.Entrypoint
		}
	}
	for _, s := range all {
		if s.Attached() {
			if byTitle, err := windowsByTitle(ctx); err == nil {
				assignWindows(all, byTitle)
			}
			break
		}
	}
	return errors.Join(problems...)
}

// Matching a unique window title is not enough when two live sessions claim
// that title. Both joins must be unambiguous before a focus target is exposed.
func assignWindows(all []Session, byTitle map[string]win) {
	claims := make(map[string]int)
	for _, s := range all {
		// An ambiguous session still claims every possible window. Dropping
		// its claims could falsely make another session look unambiguous.
		seen := make(map[string]bool)
		for _, title := range windowTitles(s) {
			if w := byTitle[stripStatusGlyph(title)]; w.Address != "" && !seen[w.Address] {
				claims[w.Address]++
				seen[w.Address] = true
			}
		}
	}
	for i := range all {
		all[i].Workspace, all[i].WindowAddress = 0, ""
		if w, ok := matchWindow(byTitle, all[i]); ok && claims[w.Address] == 1 {
			all[i].Workspace, all[i].WindowAddress = w.Workspace, w.Address
		}
	}
}

func windowTitles(s Session) []string {
	if !s.Attached() {
		return nil
	}
	titles := []string{s.Name, s.Title}
	if s.Tool() == Codex {
		titles = []string{s.Title}
		if s.Title != "" && s.CWD != "" {
			titles = append(titles, s.Title+" | "+filepath.Base(s.CWD))
		}
	}
	return titles
}

func matchWindow(byTitle map[string]win, s Session) (win, bool) {
	var found win
	for _, title := range windowTitles(s) {
		if title == "" {
			continue
		}
		if w, ok := byTitle[stripStatusGlyph(title)]; ok {
			if w.Address == "" {
				return win{}, false
			} // duplicate title
			if found.Address != "" && found.Address != w.Address {
				return win{}, false
			}
			found = w
		}
	}
	return found, found.Address != ""
}

// Focus raises a session's window, switching workspace if it is on
// another one.
//
// Dispatch is by address rather than title: a session name is free text,
// and hyprctl matches titles as a regex, so any "(" or "?" in it would
// either fail or match the wrong window.
func Focus(ctx context.Context, s Session) error {
	if !s.Attached() || s.WindowAddress == "" {
		return fmt.Errorf("running session %s has no uniquely identified terminal window", s.Key())
	}
	byTitle, err := windowsByTitle(ctx)
	if err != nil {
		return err
	}
	w, ok := matchWindow(byTitle, s)
	if !ok || w.Address != s.WindowAddress {
		return fmt.Errorf("window for %s changed or is ambiguous; refresh the listing", s.Key())
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
		return live[i].Key().String() < live[j].Key().String()
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

// claudeLiveProcs reads Claude's registry and verifies that each PID still exists.
func claudeLiveProcs(root string) (map[string]liveProc, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	procs := make(map[string]liveProc)
	var problems []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if os.IsNotExist(err) {
			continue // process exited during this scan
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		var p liveProc
		if err := json.Unmarshal(data, &p); err != nil || p.SessionID == "" || p.PID <= 0 {
			problems = append(problems, fmt.Errorf("invalid registry entry %s", entry.Name()))
			continue
		}
		alive, err := processAlive(p.PID)
		if err != nil {
			problems = append(problems, err)
		}
		if alive {
			procs[p.SessionID] = p
		}
	}
	return procs, errors.Join(problems...)
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
// Duplicate titles retain an empty target: they are ambiguous, including
// when a session has another matching alias. A wrong workspace is worse
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

	return uniqueWindows(clients), nil
}

func uniqueWindows(clients []window) map[string]win {
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
		seen[title] = win{} // retain ambiguity so another alias cannot hide it
	}
	return seen
}

// Strip only recognized activity glyphs, preserving punctuation in real titles.
func stripStatusGlyph(title string) string {
	title = strings.TrimSpace(title)
	for _, r := range title {
		if (r >= '⠀' && r <= '⣿') || strings.ContainsRune("✳✻✽✶✢●◐◓◑◒•", r) {
			return strings.TrimSpace(strings.TrimPrefix(title, string(r)))
		}
		break
	}
	return title
}
