package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, home, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-"+name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "sessions", "2026", "09", "17", "rollout-unrelated-name-"+name+".jsonl")
	putFile(t, path, string(data))
	return path
}

func TestCodexFormats(t *testing.T) {
	for _, tc := range []struct {
		name, origin, cwd string
		prompts, messages int
	}{
		{"legacy", "vscode", "/work/repo", 3, 3},
		{"paginated", "cli", "/work/original", 4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := copyFixture(t, t.TempDir(), tc.name)
			thread, err := foldCodex(path)
			if err != nil {
				t.Fatal(err)
			}
			s := thread.session
			if s.ID != tc.name || s.Provider != Codex || s.CWD != tc.cwd || s.Origin != tc.origin || s.Transcript != path || s.Messages != tc.messages {
				t.Fatalf("wrong session metadata: %+v", s)
			}
			if s.Attached() || s.Live() || s.LiveState != LiveUnknown {
				t.Fatal("saved source claimed liveness")
			}
			ps, err := NewSessionPromptReader(s).Next()
			if err != nil || len(ps) != tc.prompts {
				t.Fatalf("prompts=%+v, err=%v", ps, err)
			}
			for i, p := range ps {
				if p.N != i+1 || p.At.IsZero() {
					t.Errorf("lost numbering or timestamp: %+v", p)
				}
				for _, forbidden := range []string{"INJECTED", "ASSISTANT", "TOOL", "NOT A PROMPT"} {
					if strings.Contains(p.Text, forbidden) {
						t.Fatalf("leaked %s", forbidden)
					}
				}
			}
			if tc.name == "legacy" {
				if ps[0].Text != "Find the needle\n  keep indentation" || !ps[0].Image || ps[1].Kind != KindInterrupt || ps[2].Text != ps[0].Text {
					t.Fatalf("lost legacy prompt data: %+v", ps)
				}
			} else if ps[0].Text != "continue" || ps[1].Text != "continue" || !ps[2].Image || ps[3].Kind != KindInterrupt {
				t.Fatalf("deduplication lost turns or images: %+v", ps)
			}
		})
	}
}

func TestCodexIndexAndSubagents(t *testing.T) {
	home := t.TempDir()
	copyFixture(t, home, "legacy")
	copyFixture(t, home, "paginated")
	putFile(t, filepath.Join(home, "session_index.jsonl"), `{"id":"paginated","thread_name":"first name","updated_at":"2026-09-17T10:00:00Z"}
{"id":"paginated","thread_name":"latest name","updated_at":"2026-09-17T12:00:00Z"}
{"id":"paginated","thread_name":"stale name","updated_at":"2026-09-17T11:00:00Z"}
{"id":"paginated","thread_name":"partial`)
	for _, child := range []struct{ id, parent, source string }{
		{"child", "paginated", `{"subagent":{"other":"guardian"}}`},
		{"grandchild", "", `{"subagent":{"thread_spawn":{"parent_thread_id":"child","depth":2}}}`},
		{"orphan", "missing", `{"subagent":"review"}`},
		{"cycle1", "cycle2", `"cli"`}, {"cycle2", "cycle1", `"cli"`},
	} {
		putFile(t, filepath.Join(home, "sessions", "rollout-"+child.id+".jsonl"),
			`{"type":"session_meta","payload":{"id":"`+child.id+`","parent_thread_id":"`+child.parent+`","source":`+child.source+`}}`+"\n"+
				`{"type":"event_msg","payload":{"type":"user_message","message":"child task"}}`+"\n")
	}
	all, err := ScanCodex(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "paginated" || all[0].Title != "latest name" {
		t.Fatalf("catalog: %+v", all)
	}
	want := []Subagent{
		{ID: "child", Type: "guardian", Description: "child task", SpawnDepth: 1},
		{ID: "grandchild", Description: "child task", SpawnDepth: 2},
	}
	if !reflect.DeepEqual(all[0].Subagents, want) {
		t.Fatalf("children: %+v", all[0].Subagents)
	}
	if all[1].Title != "Find the needle keep indentation" {
		t.Fatalf("fallback title: %q", all[1].Title)
	}
}

func TestCodexFollowPairsAcrossReadsAndRetriesPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	putFile(t, path, `{"timestamp":"2026-09-17T10:00:00Z","type":"event_msg","payload":{"type":"user_message","turn_id":"1","message":"again"}}`+"\n")
	r := NewSessionPromptReader(Session{Provider: Codex, Transcript: path})
	ps, err := r.Next()
	if err != nil || len(ps) != 1 || ps[0].N != 1 {
		t.Fatalf("first: %+v %v", ps, err)
	}
	appendText := func(text string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	appendText(`{"timestamp":"2026-09-17T10:00:00.001Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"1","item":{"type":"UserMessage","id":"1","content":[{"type":"text","text":"again"}]}}}` + "\n")
	if ps, err := r.Next(); err != nil || len(ps) != 0 {
		t.Fatalf("duplicate after follow: %+v %v", ps, err)
	}
	appendText(`{"timestamp":"2026-09-17T11:00:00Z","type":"event_msg","payload":{"type":"user_message","turn_id":"2","message":"aga`)
	if ps, err := r.Next(); err != nil || len(ps) != 0 {
		t.Fatalf("partial: %+v %v", ps, err)
	}
	appendText("in\"}}\n")
	ps, err = r.Next()
	if err != nil || len(ps) != 1 || ps[0].Text != "again" || ps[0].N != 2 {
		t.Fatalf("new repeated turn: %+v %v", ps, err)
	}
}

func TestCatalogIsolationSearchAndJSON(t *testing.T) {
	dir := t.TempDir()
	c := Catalog{ClaudeRoot: filepath.Join(dir, "claude", "projects"), ClaudeHistory: filepath.Join(dir, "claude", "history.jsonl"), CodexHome: filepath.Join(dir, "codex")}
	copyFixture(t, c.CodexHome, "legacy")
	putFile(t, filepath.Join(c.ClaudeRoot, "project", "legacy.jsonl"), `{"type":"user","timestamp":"2026-09-18T10:00:00Z","cwd":"/work/claude","message":{"content":"needle from Claude"}}`+"\n")
	putFile(t, c.ClaudeHistory, `{"sessionId":"legacy","display":"needle from Claude"}`+"\n")
	all, err := c.Discover(All)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Tool() != Claude || all[1].Tool() != Codex || all[0].Key() == all[1].Key() {
		t.Fatalf("mixed sort/identity: %+v", all)
	}
	hits, err := c.Search(all, "NEEDLE")
	if err != nil || len(hits) != 2 || hits[0].Session.Tool() != Claude || hits[1].Matches != 2 {
		t.Fatalf("mixed search: %+v %v", hits, err)
	}
	for _, noise := range []string{"INJECTED", "ASSISTANT", "TOOL"} {
		hits, err := c.Search(all, noise)
		if err != nil || len(hits) != 0 {
			t.Fatalf("search leaked %s: %+v %v", noise, hits, err)
		}
	}
	data, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"provider":"codex"`) || strings.Contains(string(data), "rollout-") {
		t.Fatalf("JSON: %s", data)
	}
	for _, provider := range []Provider{Claude, Codex} {
		found, err := c.Discover(provider)
		if err != nil || len(found) != 1 || found[0].Tool() != provider {
			t.Fatalf("filter %s: %+v %v", provider, found, err)
		}
	}
	// An absent provider is normal, a broken provider is a warning with useful results.
	c.ClaudeRoot = filepath.Join(dir, "absent")
	found, err := c.Discover(All)
	if err != nil || len(found) != 1 {
		t.Fatalf("absent provider: %+v %v", found, err)
	}
	putFile(t, c.ClaudeRoot, "not a directory")
	found, err = c.Discover(All)
	if err == nil || len(found) != 1 || found[0].Tool() != Codex {
		t.Fatalf("broken provider: %+v %v", found, err)
	}
}

func TestMalformedCodexFileDoesNotHideValidSessions(t *testing.T) {
	home := t.TempDir()
	copyFixture(t, home, "legacy")
	putFile(t, filepath.Join(home, "sessions", "rollout-broken.jsonl"), "not json\n")
	all, err := ScanCodex(home)
	if err == nil || len(all) != 1 || all[0].ID != "legacy" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	got, err := DefaultCodexHome()
	if err != nil || got != filepath.Join(home, ".codex") {
		t.Fatalf("home=%s err=%v", got, err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, "custom"))
	got, err = DefaultCodexHome()
	if err != nil || got != filepath.Join(home, "custom") {
		t.Fatalf("override=%s err=%v", got, err)
	}
}

func TestMixedLiveOrderingUsesProviderToBreakTies(t *testing.T) {
	now := time.Now()
	all := []Session{{ID: "same", Provider: Codex, PID: 1, LiveSince: now}, {ID: "same", Provider: Claude, PID: 2, LiveSince: now}}
	got := LiveFirst(all)
	if got[0].Tool() != Claude || got[1].Tool() != Codex {
		t.Fatalf("unstable order: %+v", got)
	}
}

func TestCodexSharedMessageIDDoesNotSwallowLaterSteering(t *testing.T) {
	var events codexEvents
	lines := []string{
		`{"type":"event_msg","payload":{"type":"user_message","message_id":"1","turn_id":"turn","message":"again"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage","id":"1","content":[{"type":"text","text":"again"}]}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage","id":"2","content":[{"type":"text","text":"again"}]}}}`,
	}
	var count int
	for _, line := range lines {
		if events.read([]byte(line)).isPrompt {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("kept %d prompts, want the original and distinct steering message", count)
	}
}
