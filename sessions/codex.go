package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexMeta struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	CWD            string          `json:"cwd"`
	Version        string          `json:"cli_version"`
	Timestamp      time.Time       `json:"timestamp"`
	Source         json.RawMessage `json:"source"`
	ParentThreadID string          `json:"parent_thread_id"`
	Git            struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

// Codex records source either as a string or as a tagged subagent object.
func (m codexMeta) origin() (source, parent, agentType string) {
	if json.Unmarshal(m.Source, &source) == nil {
		return source, m.ParentThreadID, ""
	}
	var structured struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(m.Source, &structured) != nil || len(structured.Subagent) == 0 {
		return "", m.ParentThreadID, ""
	}
	var sub struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
		Other string `json:"other"`
	}
	_ = json.Unmarshal(structured.Subagent, &sub)
	parent = m.ParentThreadID
	if parent == "" {
		parent = sub.ThreadSpawn.ParentThreadID
	}
	agentType = sub.Other
	if agentType == "" {
		_ = json.Unmarshal(structured.Subagent, &agentType)
	}
	return "subagent", parent, agentType
}

type codexThread struct {
	session   Session
	parent    string
	agentType string
}

// ScanCodex discovers CLI and editor threads. Child threads are labels on their
// top-level ancestor, including grandchildren; orphaned children stay hidden.
func ScanCodex(home string) ([]Session, error) {
	threads := make(map[string]codexThread)
	var problems []error
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, err)
			}
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		thread, err := foldCodex(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("reading %s: %w", path, err))
			return nil
		}
		// A duplicate transcript cannot create a second catalog identity.
		previous, exists := threads[thread.session.ID]
		if !exists || thread.session.EndedAt.After(previous.session.EndedAt) {
			threads[thread.session.ID] = thread
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}
	names, err := codexNames(filepath.Join(home, "session_index.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		problems = append(problems, err)
	}
	for id, thread := range threads {
		if name := names[id]; name != "" {
			thread.session.Title = name
			threads[id] = thread
		}
	}
	for id, thread := range threads {
		if thread.parent == "" {
			continue
		}
		parent := thread.parent
		seen := map[string]bool{id: true}
		depth := 1
		for parent != "" && !seen[parent] {
			seen[parent] = true
			ancestor, ok := threads[parent]
			if !ok {
				break
			}
			if ancestor.parent == "" && ancestor.session.Origin != "subagent" {
				ancestor.session.Subagents = append(ancestor.session.Subagents, Subagent{
					ID: id, Type: thread.agentType, Description: thread.session.Title, SpawnDepth: depth,
				})
				threads[parent] = ancestor
				break
			}
			parent = ancestor.parent
			depth++
		}
	}
	all := make([]Session, 0, len(threads))
	for _, thread := range threads {
		if thread.parent == "" && thread.session.Origin != "subagent" {
			// Map iteration must not shuffle child labels between refreshes.
			sortSubagents(thread.session.Subagents)
			all = append(all, thread.session)
		}
	}
	sortSessions(all)
	return all, errors.Join(problems...)
}

func foldCodex(path string) (codexThread, error) {
	s := Session{Provider: Codex, Transcript: path, LiveState: LiveUnknown}
	var meta codexMeta
	var events codexEvents
	err := completeLines(path, func(line []byte) {
		var r codexRecord
		if json.Unmarshal(line, &r) != nil {
			return
		}
		if r.Type == "session_meta" && s.ID == "" {
			if json.Unmarshal(r.Payload, &meta) != nil {
				return
			}
			s.ID = meta.ID
			if s.ID == "" {
				s.ID = meta.SessionID
			}
			s.CWD, s.Branch, s.Version = meta.CWD, meta.Git.Branch, meta.Version
			s.StartedAt = meta.Timestamp
		}
		if !r.Timestamp.IsZero() {
			if s.StartedAt.IsZero() {
				s.StartedAt = r.Timestamp
			}
			if r.Timestamp.After(s.EndedAt) {
				s.EndedAt = r.Timestamp
			}
		}
		event := events.fromRecord(r)
		if event.message {
			s.Messages++
		}
		if event.isPrompt && event.prompt.Kind == KindPrompt && s.Title == "" {
			s.Title = oneLine(event.prompt.Text)
			if s.Title == "" && event.prompt.Image {
				s.Title = "[image]"
			}
			if title := []rune(s.Title); len(title) > 120 {
				s.Title = string(title[:119]) + "…"
			}
		}
	})
	if err != nil {
		return codexThread{}, err
	}
	if s.ID == "" {
		return codexThread{}, fmt.Errorf("missing session metadata")
	}
	origin, parent, agentType := meta.origin()
	s.Origin = origin
	return codexThread{session: s, parent: parent, agentType: agentType}, nil
}

func codexNames(path string) (map[string]string, error) {
	type name struct {
		ID        string    `json:"id"`
		Name      string    `json:"thread_name"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	latest := make(map[string]name)
	err := completeLines(path, func(line []byte) {
		var entry name
		if json.Unmarshal(line, &entry) != nil || entry.ID == "" {
			return
		}
		old, ok := latest[entry.ID]
		if !ok || !entry.UpdatedAt.Before(old.UpdatedAt) {
			latest[entry.ID] = entry
		}
	})
	names := make(map[string]string, len(latest))
	for id, entry := range latest {
		names[id] = entry.Name
	}
	return names, err
}

// completeLines ignores a partial final write, just like PromptReader.Next.
func completeLines(path string, consume func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		consume(line)
	}
}

type codexEvent struct {
	prompt   Prompt
	isPrompt bool
	message  bool
}

type codexItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Pair representations within a turn (or at an identical timestamp when no
// turn ID exists). Never deduplicate by text across turns: "continue" repeated
// tomorrow is another prompt. Counts pair one-for-one, including steering.
type codexEvents struct {
	turn     string
	seenIDs  map[string]string
	unpaired map[codexPair]int
}

type codexPair struct {
	scope, role, text, format string
	image                     bool
}

func (e *codexEvents) read(line []byte) codexEvent {
	if !bytes.Contains(line, []byte(`"event_msg"`)) {
		return codexEvent{}
	}
	var r codexRecord
	if json.Unmarshal(line, &r) != nil {
		return codexEvent{}
	}
	return e.fromRecord(r)
}

func (e *codexEvents) fromRecord(r codexRecord) codexEvent {
	if r.Type != "event_msg" {
		return codexEvent{}
	}
	var p struct {
		Type        string            `json:"type"`
		ID          string            `json:"id"`
		MessageID   string            `json:"message_id"`
		TurnID      string            `json:"turn_id"`
		Message     string            `json:"message"`
		Images      []json.RawMessage `json:"images"`
		LocalImages []json.RawMessage `json:"local_images"`
		Item        codexItem         `json:"item"`
	}
	if json.Unmarshal(r.Payload, &p) != nil {
		return codexEvent{}
	}
	if p.TurnID != "" && p.TurnID != e.turn {
		e.turn = p.TurnID
		e.unpaired = nil
	}
	if p.Type == "turn_aborted" {
		e.turn, e.unpaired = "", nil
		return codexEvent{isPrompt: true, prompt: Prompt{Kind: KindInterrupt, Text: interrupted, At: r.Timestamp}}
	}
	if p.Type == "task_complete" || p.Type == "turn_complete" {
		e.turn, e.unpaired = "", nil
		return codexEvent{}
	}
	role, format, id := "", "legacy", p.MessageID
	if id == "" {
		id = p.ID
	}
	text, image := p.Message, len(p.Images)+len(p.LocalImages) > 0
	switch p.Type {
	case "user_message":
		role = "user"
	case "agent_message":
		role = "assistant"
	case "item_completed":
		format, id = "item", p.Item.ID
		switch p.Item.Type {
		case "UserMessage":
			role = "user"
		case "AgentMessage":
			role = "assistant"
		default:
			return codexEvent{}
		}
		var parts []string
		for _, content := range p.Item.Content {
			switch strings.ToLower(content.Type) {
			case "text", "input_text", "output_text":
				parts = append(parts, content.Text)
			case "image", "input_image", "localimage", "local_image":
				image = true
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return codexEvent{}
	}
	if strings.TrimSpace(text) == "" && !image {
		return codexEvent{}
	}
	scope := e.turn
	if scope == "" && !r.Timestamp.IsZero() {
		scope = r.Timestamp.Format(time.RFC3339Nano)
	}
	other := "item"
	if format == "item" {
		other = "legacy"
	}
	key := codexPair{scope: scope, role: role, text: text, image: image, format: other}
	if e.seenIDs == nil {
		e.seenIDs = make(map[string]string)
	}
	if id != "" {
		messageID := role + ":" + id
		if previous, seen := e.seenIDs[messageID]; seen {
			// Also consume the paired representation. Otherwise its pending
			// text could swallow a later, distinct message in this turn.
			if previous != format && e.unpaired[key] > 0 {
				e.unpaired[key]--
			}
			return codexEvent{}
		}
		e.seenIDs[messageID] = format
	}
	if scope != "" {
		if e.unpaired == nil {
			e.unpaired = make(map[codexPair]int)
		}
		if e.unpaired[key] > 0 {
			e.unpaired[key]--
			return codexEvent{}
		}
		key.format = format
		e.unpaired[key]++
	}
	return codexEvent{
		message: true, isPrompt: role == "user",
		prompt: Prompt{Kind: KindPrompt, Text: text, Image: image, At: r.Timestamp},
	}
}
