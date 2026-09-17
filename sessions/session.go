// Package sessions reports on Claude Code and Codex conversations: what you were
// doing, which ones are still running, and where their windows are.
//
// Transcripts supply history, provider-specific process evidence supplies
// liveness, and Hyprland supplies window locations. Each scan is a fresh
// snapshot; missing live evidence is kept distinct from a confirmed exit.
package sessions

import "time"

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
	All    Provider = "all"
)

// Identity is provider-scoped: the same UUID can exist in both stores.
type Identity struct {
	Provider Provider
	ID       string
}

func (id Identity) String() string { return string(id.Provider) + ":" + id.ID }

type LiveState string

const (
	LiveUnknown LiveState = "unknown"
	LiveStopped LiveState = "stopped"
	LiveRunning LiveState = "running"
)

// Session is one conversation folded from a discovered transcript. Identity
// includes the provider; CWD comes from metadata, never a lossy directory slug.
type Session struct {
	Provider   Provider   `json:"provider"`
	ID         string     `json:"id"`         // Claude filename or Codex session metadata
	Title      string     `json:"title"`      // provider title/name, or a Codex prompt fallback
	CWD        string     `json:"cwd"`        // absolute path the session was started in
	Branch     string     `json:"branch"`     // git branch the session started on
	Version    string     `json:"version"`    // provider version that wrote the transcript
	StartedAt  time.Time  `json:"started_at"` // first timestamped record
	EndedAt    time.Time  `json:"ended_at"`   // last timestamped record
	Messages   int        `json:"messages"`   // user + assistant records
	Subagents  []Subagent `json:"subagents,omitempty"`
	Origin     string     `json:"origin,omitempty"` // saved source, never proof of a live terminal
	Transcript string     `json:"-"`                // discovered path; do not reconstruct it from CWD or ID

	// Process/window fields are omitted when unavailable. LiveState explicitly
	// distinguishes that absence from a confirmed stopped session.
	PID           int       `json:"pid,omitempty"`
	LiveSince     time.Time `json:"live_since,omitzero"`  // when the process started; a resume resets it, unlike StartedAt
	Name          string    `json:"name,omitempty"`       // session name from the registry; titles the window
	Status        string    `json:"status,omitempty"`     // Claude "busy"/"idle"; unset for Codex
	Entrypoint    string    `json:"entrypoint,omitempty"` // "cli" for a terminal session
	Workspace     int       `json:"workspace,omitempty"`  // Hyprland workspace; 0 when unknown
	LiveState     LiveState `json:"live_state"`
	WindowAddress string    `json:"-"` // set only for an unambiguous live window
}

// Tool keeps callers of the original Claude-only API compatible.
func (s Session) Tool() Provider {
	if s.Provider == "" {
		return Claude
	}
	return s.Provider
}

func (s Session) Key() Identity { return Identity{Provider: s.Tool(), ID: s.ID} }

// Live reports whether the session's process is currently running.
func (s Session) Live() bool { return s.PID != 0 }

// Attached reports a live terminal attachment, independently of whether its
// window can be identified. Claude supplies entrypoint in its registry;
// Codex requires a controlling terminal and a terminal descriptor.
func (s Session) Attached() bool { return s.Live() && s.Entrypoint == "cli" }

// Subagent is one agent spawned by a Session.
//
// Claude labels come from .meta.json sidecars; Codex labels and ancestry come
// from child transcripts. Nested Codex agents are grouped under the root.
type Subagent struct {
	ID          string `json:"id"`          // agent id, from the filename
	Type        string `json:"type"`        // agent type, e.g. "Explore"
	Description string `json:"description"` // the one-line task the agent was given
	SpawnDepth  int    `json:"spawn_depth"`
}
