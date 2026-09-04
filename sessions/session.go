// Package sessions reports on Claude Code conversations: what you were
// doing, which ones are still running, and where their windows are.
//
// Nothing here owns state. Every field is read from an authority that
// already tracks it — the transcript for what was said, Claude Code's
// live-process registry for what is running, Hyprland for where the
// window sits. Sessions are folded fresh on every call, so nothing can
// go stale.
package sessions

import "time"

// Session is one Claude Code conversation, folded from its transcript.
//
// Transcripts live at ~/.claude/projects/<slug>/<id>.jsonl, where <slug>
// is the working directory with every non-alphanumeric byte replaced by
// "-". That mapping is lossy (/x/github.com and /x/github-com produce the
// same slug), so CWD is read from the records rather than recovered from
// the directory name.
//
// Claude Code deletes transcripts older than cleanupPeriodDays (default
// 30) at startup, so a Session exists here only as long as its file does.
type Session struct {
	ID        string     `json:"id"`         // uuid, from the transcript filename
	Title     string     `json:"title"`      // "custom-title" record (/rename, claude -n) if any, else last "ai-title"
	CWD       string     `json:"cwd"`        // absolute path the session was started in
	Branch    string     `json:"branch"`     // git branch the session started on
	Version   string     `json:"version"`    // Claude Code version that wrote the transcript
	StartedAt time.Time  `json:"started_at"` // first timestamped record
	EndedAt   time.Time  `json:"ended_at"`   // last timestamped record
	Messages  int        `json:"messages"`   // user + assistant records
	Subagents []Subagent `json:"subagents,omitempty"`

	// Live fields. Set only while the session's process is running, and
	// zero-valued otherwise — a Session is complete without them, so they
	// are omitted rather than emitted as nulls.
	PID        int    `json:"pid,omitempty"`
	Name       string `json:"name,omitempty"`       // session name from the registry; titles the window
	Status     string `json:"status,omitempty"`     // "busy" or "idle"; absent for headless runs
	Entrypoint string `json:"entrypoint,omitempty"` // "cli" for a terminal session
	Workspace  int    `json:"workspace,omitempty"`  // Hyprland workspace; 0 when unknown
}

// Live reports whether the session's process is currently running.
func (s Session) Live() bool { return s.PID != 0 }

// Attached reports whether the session is one you can return to: a real
// terminal session with a window, rather than a headless `claude -p`
// subprocess.
//
// The two are told apart by entrypoint, not by the registry's "kind" —
// which reads "interactive" for both. A headless run reports "sdk-cli"
// and no status at all, because there is no window to be busy or idle in.
func (s Session) Attached() bool { return s.Entrypoint == "cli" }

// Subagent is one agent spawned by a Session.
//
// The label comes from the .meta.json sidecar written next to the agent's
// transcript, so listing subagents costs a ~120-byte read instead of a
// parse of the agent's whole conversation.
type Subagent struct {
	ID          string `json:"id"`          // agent id, from the filename
	Type        string `json:"type"`        // agent type, e.g. "Explore"
	Description string `json:"description"` // the one-line task the agent was given
	SpawnDepth  int    `json:"spawn_depth"`
}
