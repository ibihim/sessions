package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Kind tells apart the three things a transcript records as coming from
// you. A prompt is what you said; the other two are what you did. They
// read differently on screen, so the difference is kept rather than
// flattened into one stream of text.
type Kind string

const (
	KindPrompt    Kind = "prompt"    // text you typed
	KindSlash     Kind = "slash"     // a slash command you invoked
	KindInterrupt Kind = "interrupt" // where you pressed escape
)

// Prompt is one turn you took, in the order the transcript records it.
type Prompt struct {
	N     int       `json:"n"` // 1-based position in the thread
	Kind  Kind      `json:"kind"`
	Text  string    `json:"text"`            // as typed, newlines intact
	At    time.Time `json:"at"`              // zero if the record carried no timestamp
	Image bool      `json:"image,omitempty"` // the prompt carried a pasted image
}

// interrupted is the marker Claude Code writes in your slot when you stop
// a turn. It is not something you typed, but it is something you did, and
// it is where the thread changed direction.
const interrupted = "[Request interrupted by user]"

// PromptReader yields the prompts a transcript has gained since the last
// read, so a --follow loop costs one read of the tail rather than a reparse
// of the whole file.
//
// It reads the transcript, not ~/.claude/history.jsonl where Search looks.
// The prompt log attributes a prompt to the session it was typed in and to
// no other — but --resume and --fork copy the whole conversation into a new
// file under a new id, rewriting the session id inside every copied record.
// A resumed session therefore has a full thread on screen and no entries of
// its own in the log until you type again. Asked about it, the log would
// answer with everything since the resume and nothing before. The
// transcript is what the session actually sees.
type PromptReader struct {
	path     string
	provider Provider
	codex    codexEvents
	offset   int64 // bytes of complete lines already consumed
	n        int   // prompts emitted so far, which numbers the next one
}

// NewPromptReader reads path from the beginning.
func NewPromptReader(path string) *PromptReader {
	return &PromptReader{path: path, provider: Claude}
}

// NewSessionPromptReader follows the actual discovered transcript of either tool.
func NewSessionPromptReader(s Session) *PromptReader {
	return &PromptReader{path: s.Transcript, provider: s.Tool()}
}

// Next returns the prompts appended since the previous call; the first call
// returns the whole thread.
//
// Only whole lines are consumed. A live session appends while this reads,
// so the file routinely ends mid-record — the offset stops short of the
// partial line, and the next call picks it up once it is terminated.
//
// The file is reopened per call rather than held: a follow loop may run for
// days, and 500ms between opens costs nothing against holding a descriptor
// open that long.
func (r *PromptReader) Next() ([]Prompt, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking transcript: %w", err)
	}

	var out []Prompt
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF {
			// Whatever came back has no newline, so it is either nothing or
			// a half-written record. Leaving the offset before it is what
			// makes the next call see it whole.
			break
		}
		if err != nil {
			return out, fmt.Errorf("reading transcript: %w", err)
		}
		r.offset += int64(len(line))

		var p Prompt
		var ok bool
		if r.provider == Codex {
			event := r.codex.read(line)
			p, ok = event.prompt, event.isPrompt
		} else {
			p, ok = promptFrom(line)
		}
		if !ok {
			continue
		}
		r.n++
		p.N = r.n
		out = append(out, p)
	}
	return out, nil
}

// userRecord is the slice of a transcript line that could carry something
// you typed. Content is polymorphic — a bare string for a typed prompt, an
// array of blocks for anything with structure — so it is held raw and
// decoded by shape below.
type userRecord struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Timestamp        string `json:"timestamp"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// promptFrom reads one transcript line, reporting whether it was yours.
func promptFrom(line []byte) (Prompt, bool) {
	// The same prefilter idea as contributes(): four records in five are
	// tool results or assistant turns, and none of them can be a prompt.
	if !bytes.Contains(line, []byte(`"type":"user"`)) {
		return Prompt{}, false
	}
	var r userRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return Prompt{}, false
	}
	// isMeta marks context Claude Code injected into your slot on your
	// behalf — skill bodies, CLAUDE.md, the caveat above command output.
	// isSidechain is a subagent's own conversation, isCompactSummary is a
	// compaction. You typed none of them.
	if r.Type != "user" || r.IsMeta || r.IsSidechain || r.IsCompactSummary {
		return Prompt{}, false
	}

	content := bytes.TrimSpace(r.Message.Content)
	if len(content) == 0 {
		return Prompt{}, false
	}

	var (
		p  Prompt
		ok bool
	)
	switch content[0] {
	case '"':
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return Prompt{}, false
		}
		p, ok = fromString(s)
	case '[':
		p, ok = fromBlocks(content)
	}
	if !ok {
		return Prompt{}, false
	}

	// A record with an unparseable timestamp keeps a zero time. Callers show
	// the text either way — losing a clock is not a reason to lose a prompt.
	if ts, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
		p.At = ts
	}
	return p, true
}

// fromString reads bare-string content: a typed prompt, or one of the
// XML-ish envelopes Claude Code writes into your slot.
//
// The envelopes are told apart by a leading "<". That works because the
// tags never appear inside a prompt — each injection is written as a record
// of its own, never appended to yours — so the only way to lose a real
// prompt here is to open one with a literal "<". A whitelist of known tag
// names would trade that for the opposite failure: every envelope a future
// release invents would leak into the listing as if you had typed it. Noise
// is the worse of the two, because you would not know to distrust it.
func fromString(s string) (Prompt, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "<") {
		return Prompt{Kind: KindPrompt, Text: t}, true
	}

	// A slash command is the one envelope worth keeping: you invoked it.
	// The tags have been seen in either order, so each is found by name
	// rather than by position.
	name := between(t, "<command-name>", "</command-name>")
	if name == "" {
		return Prompt{}, false
	}
	if args := between(t, "<command-args>", "</command-args>"); args != "" {
		name += " " + args
	}
	return Prompt{Kind: KindSlash, Text: name}, true
}

// fromBlocks reads array content. Three things arrive this way: tool
// results, which are not yours; the marker left where you pressed escape;
// and a prompt carrying a pasted image, whose text is beside the image
// rather than instead of it.
func fromBlocks(raw []byte) (Prompt, bool) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return Prompt{}, false
	}

	var (
		text  []string
		image bool
	)
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			return Prompt{}, false
		case "image":
			image = true
		case "text":
			text = append(text, b.Text)
		}
	}

	joined := strings.TrimSpace(strings.Join(text, "\n"))
	switch {
	case joined == interrupted:
		return Prompt{Kind: KindInterrupt, Text: joined}, true
	case joined == "" && !image:
		return Prompt{}, false
	}
	return Prompt{Kind: KindPrompt, Text: joined, Image: image}, true
}

// between returns what openTag and closeTag enclose, or "" if either is
// missing.
func between(s, openTag, closeTag string) string {
	_, rest, ok := strings.Cut(s, openTag)
	if !ok {
		return ""
	}
	inner, _, ok := strings.Cut(rest, closeTag)
	if !ok {
		return ""
	}
	return strings.TrimSpace(inner)
}
