package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultRoot returns Claude Code's transcript directory.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// maxLine caps a single transcript line. Records embed whole pasted files
// and tool results; the largest seen in practice is ~610KB, so 8MB is
// headroom rather than a real limit. bufio.Scanner defaults to 64KB and
// would fail outright on roughly one file in fifteen.
const maxLine = 8 << 20

// Scan folds every transcript under root into a Session, newest first.
//
// Layout, where <slug> is the slugified working directory:
//
//	<root>/<slug>/<id>.jsonl                        session transcript
//	<root>/<slug>/<id>/subagents/agent-<x>.jsonl      subagent transcript
//	<root>/<slug>/<id>/subagents/agent-<x>.meta.json  subagent label
//
// Every call re-reads everything. At ~150 sessions that costs tens of
// milliseconds, because foldFile skips JSON parsing for lines that cannot
// contribute; an index would buy little and could go stale.
//
// A transcript that cannot be read is skipped rather than failing the
// scan — a half-written file from a live session should not break the
// listing.
func Scan(root string) ([]Session, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("globbing transcripts: %w", err)
	}

	sessions := make([]Session, 0, len(paths))
	for _, path := range paths {
		s, err := foldFile(path)
		if err != nil {
			continue
		}
		s.Subagents = readSubagents(path)
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].EndedAt.After(sessions[j].EndedAt)
	})
	return sessions, nil
}

// record is the slice of a transcript line this package cares about.
// Unknown fields — message bodies, tool calls, attachments — are dropped
// by encoding/json.
type record struct {
	Type        string `json:"type"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Timestamp   string `json:"timestamp"`
	CWD         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	Version     string `json:"version"`
}

// Lines are compact JSON from a single writer, so a substring check for
// the type tag is a reliable prefilter and avoids unmarshalling the
// megabyte-sized attachment records that dominate a transcript's bytes.
var wanted = [][]byte{
	[]byte(`"type":"ai-title"`),
	[]byte(`"type":"custom-title"`),
	[]byte(`"type":"user"`),
	[]byte(`"type":"assistant"`),
}

func contributes(line []byte) bool {
	for _, w := range wanted {
		if bytes.Contains(line, w) {
			return true
		}
	}
	return false
}

// foldFile reduces one transcript to a Session.
func foldFile(path string) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()

	s := Session{ID: sessionID(path)}

	// The two title kinds are folded separately because order across kinds
	// means nothing: "ai-title" is re-emitted on every resume, so after a
	// rename it keeps appearing behind the "custom-title" it lost to. A
	// plain last-record-wins would hand the session back to the AI title.
	var aiTitle, customTitle string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !contributes(line) {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}

		if r.Type == "ai-title" {
			aiTitle = r.AITitle
			continue
		}
		if r.Type == "custom-title" {
			// Written by /rename and `claude -n`.
			customTitle = r.CustomTitle
			continue
		}

		s.Messages++
		// First record wins. Later records track the shell's working
		// directory, which drifts as tools cd around — but the session's
		// project dir is fixed at start, and that is what --resume scopes
		// to. Taking the last value yields a path resume would reject.
		if s.CWD == "" {
			s.CWD = r.CWD
		}
		if s.Branch == "" {
			s.Branch = r.GitBranch
		}
		if s.Version == "" {
			s.Version = r.Version
		}
		if ts, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
			if s.StartedAt.IsZero() {
				s.StartedAt = ts
			}
			s.EndedAt = ts
		}
	}
	if err := scanner.Err(); err != nil {
		return Session{}, fmt.Errorf("reading %s: %w", path, err)
	}

	s.Title = customTitle
	if s.Title == "" {
		s.Title = aiTitle
	}
	return s, nil
}

// sessionID strips the directory and the .jsonl suffix.
func sessionID(path string) string {
	return filepath.Base(path[:len(path)-len(".jsonl")])
}

// readSubagents collects the labels of agents spawned by the session whose
// transcript is at path. Absent or unreadable sidecars yield no subagents
// — they are decoration on a listing, never a reason to drop a session.
func readSubagents(path string) []Subagent {
	dir := path[:len(path)-len(".jsonl")]
	metas, err := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.meta.json"))
	if err != nil || len(metas) == 0 {
		return nil
	}

	subs := make([]Subagent, 0, len(metas))
	for _, m := range metas {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var meta struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
			SpawnDepth  int    `json:"spawnDepth"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		base := filepath.Base(m)
		subs = append(subs, Subagent{
			ID:          base[len("agent-") : len(base)-len(".meta.json")],
			Type:        meta.AgentType,
			Description: meta.Description,
			SpawnDepth:  meta.SpawnDepth,
		})
	}
	return subs
}

// TranscriptPath returns the file a session was folded from.
//
// The id is globbed for rather than the directory name being rebuilt from
// the session's CWD. Slugification is lossy — /x/github.com and
// /x/github-com collapse to one slug — so the reverse mapping is a guess,
// while an id is unique across every project and makes the glob exact.
func TranscriptPath(root, id string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "*", id+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("locating transcript: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no transcript for session %s", id)
	}
	return matches[0], nil
}
