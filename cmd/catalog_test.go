package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commandFixture(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "custom-codex"))
	t.Setenv("NO_COLOR", "1")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", "projects", "project", "shared-id.jsonl"), `{"type":"user","timestamp":"2026-09-15T10:00:00Z","cwd":"/tmp","message":{"content":"Claude needle"}}`+"\n")
	write(filepath.Join(home, ".claude", "history.jsonl"), `{"sessionId":"shared-id","display":"Claude needle"}`+"\n")
	write(filepath.Join(home, "custom-codex", "sessions", "2026", "09", "17", "rollout-arbitrary-name.jsonl"), `{"type":"session_meta","timestamp":"2026-09-17T10:00:00Z","payload":{"id":"shared-id","cwd":"/tmp","source":"vscode"}}`+"\n"+
		`{"type":"event_msg","timestamp":"2026-09-17T10:00:01Z","payload":{"type":"user_message","message":"Codex needle"}}`+"\n")
}

func runCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.ExecuteContext(t.Context())
	return out.String(), stderr.String(), err
}

func TestMixedCommandCatalog(t *testing.T) {
	commandFixture(t)
	for _, tc := range []struct {
		args     []string
		count    int
		provider string
	}{
		{[]string{"list", "-a", "-n", "0", "--json"}, 2, ""},
		{[]string{"--provider", "codex", "list", "-a", "--json"}, 1, "codex"},
		{[]string{"list", "--provider", "claude", "-a", "--json"}, 1, "claude"},
		{[]string{"list", "--since", "2026-09-16", "--json"}, 1, "codex"},
		{[]string{"list", "-a", "-n", "1", "--json"}, 1, "codex"},
	} {
		out, stderr, err := runCommand(t, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v (%s)", tc.args, err, stderr)
		}
		var got struct {
			Source    string           `json:"source"`
			FetchedAt string           `json:"fetched_at"`
			Items     []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("non-JSON output: %s", out)
		}
		if got.Source != "sessions" || got.FetchedAt == "" || len(got.Items) != tc.count {
			t.Fatalf("envelope: %+v", got)
		}
		if tc.provider != "" && got.Items[0]["provider"] != tc.provider {
			t.Fatalf("provider: %+v", got.Items)
		}
		for _, s := range got.Items {
			for _, field := range []string{"id", "title", "cwd", "branch", "version", "started_at", "ended_at", "messages", "provider", "live_state"} {
				if _, ok := s[field]; !ok {
					t.Errorf("lost JSON field %s", field)
				}
			}
		}
	}
	out, _, err := runCommand(t, "list", "-a")
	if err != nil || !strings.Contains(out, "TOOL") || !strings.Contains(out, "codex") || !strings.Contains(out, "claude") {
		t.Fatalf("text listing: %s (%v)", out, err)
	}
}

func TestFindAndPromptsProviderFilter(t *testing.T) {
	commandFixture(t)
	for _, provider := range []string{"claude", "codex"} {
		out, _, err := runCommand(t, "--provider", provider, "find", "needle", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Items []struct {
				Session struct {
					Provider string `json:"provider"`
				} `json:"session"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 1 || got.Items[0].Session.Provider != provider {
			t.Fatalf("find filter: %s", out)
		}
		out, _, err = runCommand(t, "prompts", provider+":shared", "--json")
		if err != nil || !strings.Contains(strings.ToLower(out), provider+" needle") {
			t.Fatalf("prompts: %s %v", out, err)
		}
	}
	if _, _, err := runCommand(t, "prompts", "shared", "--json"); err == nil {
		t.Fatal("unqualified collision selected a provider")
	}
	if _, _, err := runCommand(t, "--provider", "claude", "prompts", "codex:shared"); err == nil {
		t.Fatal("selector bypassed provider filter")
	}
	for _, verb := range []string{"list", "find", "prompts", "open"} {
		args := []string{"--provider", "invalid", verb}
		if verb == "find" || verb == "open" {
			args = append(args, "query")
		}
		if _, _, err := runCommand(t, args...); err == nil || !strings.Contains(err.Error(), "invalid provider") {
			t.Fatalf("invalid filter for %s: %v", verb, err)
		}
	}
}

func TestPartialProviderFailureStillEmitsCleanJSON(t *testing.T) {
	commandFixture(t)
	root := os.Getenv("CODEX_HOME")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runCommand(t, "list", "-a", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"provider": "claude"`) || !strings.Contains(stderr, "codex:") {
		t.Fatalf("partial result: out=%s stderr=%s err=%v", out, stderr, err)
	}
}
