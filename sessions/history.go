package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultHistory returns Claude Code's prompt log.
func DefaultHistory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "history.jsonl"), nil
}

// Hit is one session whose prompts matched a search.
type Hit struct {
	Session Session `json:"session"`
	Prompt  string  `json:"prompt"` // the first matching prompt
	Matches int     `json:"matches"`
}

// entry is the part of a history.jsonl line this package reads. The log
// also carries the project path and a timestamp, but the Session it joins
// to already has both, from a source that cannot disagree with itself.
type entry struct {
	Display   string `json:"display"` // the prompt as typed
	SessionID string `json:"sessionId"`
}

// Search finds sessions whose prompts contain q, case-insensitively.
//
// The prompt log is searched rather than the transcripts: it holds what
// you typed, which is what you remember, and it is a megabyte against the
// transcripts' seventy-odd. It also reaches back further than the
// transcripts do — Claude Code prunes those on cleanupPeriodDays but
// appears to keep this forever.
//
// Sessions in the log whose transcript is gone are dropped. They cannot be
// titled, read, or resumed, so listing them would only offer something
// that no longer exists.
func Search(historyPath string, all []Session, q string) ([]Hit, error) {
	if strings.TrimSpace(q) == "" {
		return nil, fmt.Errorf("empty search")
	}

	known := make(map[string]Session, len(all))
	for _, s := range all {
		known[s.ID] = s
	}

	f, err := os.Open(historyPath)
	if err != nil {
		return nil, fmt.Errorf("opening history: %w", err)
	}
	defer f.Close()

	needle := strings.ToLower(q)
	hits := make(map[string]*Hit)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLine)
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !strings.Contains(strings.ToLower(e.Display), needle) {
			continue
		}
		s, ok := known[e.SessionID]
		if !ok {
			continue
		}

		if h, seen := hits[e.SessionID]; seen {
			h.Matches++
			continue
		}
		hits[e.SessionID] = &Hit{Session: s, Prompt: oneLine(e.Display), Matches: 1}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}

	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Session.EndedAt.After(out[j].Session.EndedAt)
	})
	return out, nil
}

// oneLine flattens a prompt for display. Prompts are multi-line often
// enough that printing them raw would break a listing apart.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
