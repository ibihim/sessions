package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ibihim/sessions/sessions"
)

func TestResolveQualifiedAndAmbiguousSelectors(t *testing.T) {
	all := []sessions.Session{
		{Provider: sessions.Claude, ID: "same-id", Title: "Fix auth"},
		{Provider: sessions.Codex, ID: "same-id", Title: "Fix auth"},
		{Provider: sessions.Codex, ID: "unique", Title: "Different"},
	}
	for _, q := range []string{"same", "Fix auth"} {
		_, err := resolve(all, q)
		if err == nil || !strings.Contains(err.Error(), "claude:same-id") || !strings.Contains(err.Error(), "codex:same-id") {
			t.Fatalf("ambiguous %q: %v", q, err)
		}
	}
	for _, tc := range []struct {
		q        string
		provider sessions.Provider
		id       string
	}{
		{"claude:same", sessions.Claude, "same-id"}, {"codex:same", sessions.Codex, "same-id"},
		{"uni", sessions.Codex, "unique"}, {"different", sessions.Codex, "unique"},
	} {
		s, err := resolve(all, tc.q)
		if err != nil || s.Tool() != tc.provider || s.ID != tc.id {
			t.Fatalf("resolve(%q)=%+v %v", tc.q, s, err)
		}
	}
	for _, q := range []string{"", "codex:", "all:same", "unknown:same", "missing"} {
		if _, err := resolve(all, q); err == nil {
			t.Fatalf("accepted selector %q", q)
		}
	}
	// An intentional ID prefix takes precedence over an unrelated title match.
	if s, err := resolve(append(all, sessions.Session{ID: "title", Title: "unique"}), "unique"); err != nil || s.ID != "unique" {
		t.Fatalf("ID priority: %+v %v", s, err)
	}
}

func TestLaunchArguments(t *testing.T) {
	for _, provider := range []sessions.Provider{sessions.Claude, sessions.Codex} {
		for _, fork := range []bool{false, true} {
			for _, window := range []bool{false, true} {
				for _, yolo := range []bool{false, true} {
					s := sessions.Session{Provider: provider, ID: "fixed-id", CWD: "/tmp/work tree"}
					got, err := buildLaunch(s, window, yolo, fork)
					if err != nil {
						t.Fatal(err)
					}
					var want []string
					if provider == sessions.Claude {
						want = []string{"--resume", "fixed-id"}
						if fork {
							want = append(want, "--fork-session")
						}
						if window && yolo {
							want = append(want, "--dangerously-skip-permissions")
						}
					} else {
						verb := "resume"
						if fork {
							verb = "fork"
						}
						want = []string{verb, "fixed-id"}
						if window && yolo {
							want = append(want, "--dangerously-bypass-approvals-and-sandbox")
						}
					}
					if got.Executable != string(provider) || got.CWD != s.CWD || !reflect.DeepEqual(got.Args, want) {
						t.Fatalf("launch (%s,%v,%v,%v): %+v want=%v", provider, fork, window, yolo, got, want)
					}
				}
			}
		}
	}
}

func TestPrepareLaunchAndGhosttyArguments(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "custom codex home"))
	s := sessions.Session{Provider: sessions.Codex, ID: "id", CWD: dir}
	l, err := prepareLaunch(s, true, false, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+new-window", "--working-directory=" + dir, "-e", filepath.Join(dir, "env"), "CODEX_HOME=" + filepath.Join(dir, "custom codex home"), cli, "fork", "id"}
	if got := windowArgs(l); !reflect.DeepEqual(got, want) {
		t.Fatalf("Ghostty argv=%q want=%q", got, want)
	}
	s.Provider = sessions.Claude
	if _, err := prepareLaunch(s, false, false, false); err == nil || !strings.Contains(err.Error(), "claude is not on PATH") {
		t.Fatalf("missing executable: %v", err)
	}
	s.Provider = sessions.Codex
	s.CWD = filepath.Join(dir, "missing")
	if _, err := prepareLaunch(s, false, false, false); err == nil || !strings.Contains(err.Error(), "session directory") {
		t.Fatalf("missing cwd: %v", err)
	}
	s.CWD = cli
	if _, err := prepareLaunch(s, false, false, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file cwd: %v", err)
	}
}

func TestOpeningNeverStartsAnotherWriter(t *testing.T) {
	for _, s := range []sessions.Session{
		{Provider: sessions.Codex, ID: "x", PID: 1},
		{Provider: sessions.Codex, ID: "x", PID: 1, Entrypoint: "cli"},
		{Provider: sessions.Claude, ID: "x", PID: 1, Entrypoint: "cli"},
		{Provider: sessions.Codex, ID: "x", LiveState: sessions.LiveUnknown},
	} {
		if action, err := openingAction(s, false); err == nil {
			t.Fatalf("unsafe action %s for %+v", action, s)
		}
		if action, err := openingAction(s, true); err != nil || action != "fork" {
			t.Fatalf("fork: %s %v", action, err)
		}
	}
	live := sessions.Session{PID: 1, Entrypoint: "cli", WindowAddress: "0x1"}
	if action, err := openingAction(live, false); err != nil || action != "focus" {
		t.Fatalf("focus: %s %v", action, err)
	}
	if action, err := openingAction(sessions.Session{LiveState: sessions.LiveStopped}, false); err != nil || action != "resume" {
		t.Fatalf("resume: %s %v", action, err)
	}
}

func TestPickRequiresSelectorForSeveralLiveSessionsInDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	all := []sessions.Session{{Provider: sessions.Claude, ID: "1", CWD: cwd, PID: 1, Entrypoint: "cli"}, {Provider: sessions.Codex, ID: "2", CWD: cwd, PID: 2}}
	if _, err := pick(all, nil); err == nil || !strings.Contains(err.Error(), "provider:id-prefix") {
		t.Fatalf("ambiguous directory: %v", err)
	}
	all[0].PID = 0
	if s, err := pick(all, nil); err != nil || s.ID != "2" {
		t.Fatalf("sole live session: %+v %v", s, err)
	}
	all[1].PID = 0
	if s, err := pick(all, nil); err != nil || s.ID != "1" {
		t.Fatalf("recent fallback: %+v %v", s, err)
	}
}

func TestTitleSelectorCanContainColon(t *testing.T) {
	all := []sessions.Session{{Provider: sessions.Codex, ID: "id", Title: "Review: provider boundaries"}}
	if s, err := resolve(all, "Review: provider"); err != nil || s.ID != "id" {
		t.Fatalf("title selector: %+v %v", s, err)
	}
}
