package cmd

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ibihim/sessions/sessions"
)

func TestRows(t *testing.T) {
	old := now.Add(-recent - time.Hour)
	fresh := now.Add(-time.Hour)
	all := []sessions.Session{
		{ID: "live-old", EndedAt: old, PID: 1},
		{ID: "fresh", EndedAt: fresh},
		{ID: "old-1", EndedAt: old},
		{ID: "old-2", EndedAt: old},
	}

	tests := []struct {
		name    string
		all     []sessions.Session
		showAll bool
		want    string
	}{
		{"window keeps live and recent, counts the rest", all, false, "live-old fresh …2"},
		{"all shows everything, no trailer", all, true, "live-old fresh old-1 old-2"},
		{"nothing hidden, no trailer", all[:2], false, "live-old fresh"},
		{"empty", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, it := range rows(tt.all, tt.showAll, now) {
				r := it.(row)
				if r.older > 0 {
					got = append(got, fmt.Sprintf("…%d", r.older))
					continue
				}
				got = append(got, r.s.ID)
			}
			if g := strings.Join(got, " "); g != tt.want {
				t.Errorf("rows() = %q, want %q", g, tt.want)
			}
		})
	}
}

func TestRowFilterValue(t *testing.T) {
	if v := (row{older: 3}).FilterValue(); v != "" {
		t.Errorf("trailer FilterValue = %q, want empty so a filter hides it", v)
	}
	s := sessions.Session{Title: "oidc cache", CWD: "/tmp/kubernetes"}
	if v := (row{s: s}).FilterValue(); !strings.Contains(v, "oidc cache") || !strings.Contains(v, "kubernetes") {
		t.Errorf("FilterValue = %q, want title and path", v)
	}
}

// Once the terminal has said what its background is, the cursor row lies
// on a band as wide as the screen. The band has to survive the whole row:
// every escape on it either lays the band or closes it at the very end,
// since a reset anywhere else — the green ●'s, say — would end it there.
func TestCursorBand(t *testing.T) {
	a := sessions.Session{ID: "aaaa", Title: "oidc cache", CWD: "/tmp/kubernetes", EndedAt: time.Now(), PID: 1, Entrypoint: "cli"}
	b := sessions.Session{ID: "bbbb", Title: "rate limiter", CWD: "/tmp/openshift", EndedAt: time.Now()}

	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: t.TempDir()}, sessions.Claude, 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(scannedMsg{all: []sessions.Session{a, b}})
	if cur := viewLine(t, m, "oidc cache"); strings.Contains(cur, "\x1b[48;") {
		t.Errorf("band before the terminal reported its background: %q", cur)
	}

	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.RGBA{0x19, 0x1d, 0x2b, 0xff}})
	cur := viewLine(t, m, "oidc cache")
	band := ansi.FindString(cur)
	if !strings.HasPrefix(band, "\x1b[48;") || !strings.HasPrefix(cur, band) {
		t.Fatalf("cursor row does not open on the band: %q", cur)
	}
	for _, at := range ansi.FindAllStringIndex(cur, -1) {
		seq, rest := cur[at[0]:at[1]], cur[at[1]:]
		closes := (seq == "\x1b[m" || seq == "\x1b[0m") && (rest == "" || strings.HasPrefix(rest, band))
		if seq != band && !closes {
			t.Errorf("%q at byte %d breaks the band: %q", seq, at[0], cur)
		}
	}
	if w := lipgloss.Width(cur); w != 120 {
		t.Errorf("band is %d columns wide, want all 120", w)
	}
	if other := viewLine(t, m, "rate limiter"); strings.Contains(other, "\x1b[48;") {
		t.Errorf("band under a row the cursor is not on: %q", other)
	}
}

// The band sits a shade off the background, toward the text: lighter on a
// dark theme, darker on a light one. Lightened, white would stay white.
func TestCursorBandShade(t *testing.T) {
	sum := func(c color.Color) uint32 { r, g, b, _ := c.RGBA(); return r + g + b }
	dark, light := color.RGBA{0x19, 0x1d, 0x2b, 0xff}, color.RGBA{0xff, 0xff, 0xff, 0xff}
	if got := cursorBand(tea.BackgroundColorMsg{Color: dark}); sum(got) <= sum(dark) {
		t.Errorf("band on %v is %v, want lighter", dark, got)
	}
	if got := cursorBand(tea.BackgroundColorMsg{Color: light}); sum(got) >= sum(light) {
		t.Errorf("band on %v is %v, want darker", light, got)
	}
}

// viewLine is the first line of the picker's screen that shows text. The
// list is drawn above the pane, so a title finds its row before its banner.
func viewLine(t *testing.T, m tea.Model, text string) string {
	t.Helper()
	for _, l := range strings.Split(m.(picker).View().Content, "\n") {
		if strings.Contains(l, text) {
			return l
		}
	}
	t.Fatalf("no line shows %q", text)
	return ""
}

// The pane follows the cursor, and only the answer for the row under the
// cursor may paint: reads finish in any order, and a late one for a row
// already left must go to the cache and nowhere else.
func TestPaneFollowsCursor(t *testing.T) {
	root := t.TempDir()
	a := writeTranscript(t, root, "aaaa", "first thread")
	b := writeTranscript(t, root, "bbbb", "second thread")

	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: root}, sessions.Claude, 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.Update(scannedMsg{all: []sessions.Session{a, b}})

	// The cursor landed on a, and the pane asked for its thread.
	var loaded promptsMsg
	for _, msg := range drain(cmd) {
		if pm, ok := msg.(promptsMsg); ok {
			loaded = pm
		}
	}
	if loaded.id != a.Key() {
		t.Fatalf("asked for %q, want the row under the cursor, aaaa", loaded.id)
	}
	m, _ = m.Update(loaded)
	if pane := m.(picker).pane.View(); !strings.Contains(pane, "first thread") {
		t.Fatalf("pane does not show a's thread:\n%s", pane)
	}

	// A late answer for a row the cursor is not on may not paint.
	m, _ = m.Update(promptsMsg{id: b.Key(), ps: []sessions.Prompt{
		{N: 1, Kind: sessions.KindPrompt, Text: "second thread"}}})
	if pane := m.(picker).pane.View(); strings.Contains(pane, "second thread") {
		t.Fatalf("a stale answer painted:\n%s", pane)
	}

	// Moving onto b is served from the cache: no read goes out.
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	for _, msg := range drain(cmd) {
		if _, ok := msg.(promptsMsg); ok {
			t.Error("read b's thread again although it was cached")
		}
	}
	if pane := m.(picker).pane.View(); !strings.Contains(pane, "second thread") {
		t.Fatalf("pane did not follow the cursor to b:\n%s", pane)
	}
}

// A rescan leaves the pane up. On a timer, blanking it until the re-read
// lands would make it blink every interval; the thread stays on screen
// and a re-read goes out for it instead.
func TestRescanKeepsPane(t *testing.T) {
	root := t.TempDir()
	a := writeTranscript(t, root, "aaaa", "first thread")

	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: root}, sessions.Claude, 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.Update(scannedMsg{all: []sessions.Session{a}})
	for _, msg := range drain(cmd) {
		m, _ = m.Update(msg)
	}

	m, cmd = m.Update(scannedMsg{all: []sessions.Session{a}, at: time.Now()})
	if pane := m.(picker).pane.View(); !strings.Contains(pane, "first thread") {
		t.Fatalf("rescan blanked the pane:\n%s", pane)
	}
	reread := false
	for _, msg := range drain(cmd) {
		if pm, ok := msg.(promptsMsg); ok && pm.id == a.Key() {
			reread = true
		}
	}
	if !reread {
		t.Error("rescan did not re-read the thread on screen")
	}
}

// Scans overlap when a tick lands beside an open. The one that began
// first read an older disk, and may not overwrite the other, whichever
// finishes last.
func TestStaleScanDropped(t *testing.T) {
	older := time.Now()
	newer := older.Add(time.Second)
	a := sessions.Session{ID: "aaaa", EndedAt: older}
	b := sessions.Session{ID: "bbbb", EndedAt: older}

	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: t.TempDir()}, sessions.Claude, 0)
	m, _ = m.Update(scannedMsg{all: []sessions.Session{a, b}, at: newer})
	m, _ = m.Update(scannedMsg{all: []sessions.Session{a}, at: older})
	if n := len(m.(picker).all); n != 2 {
		t.Errorf("a stale scan replaced a newer one: %d sessions, want 2", n)
	}
}

// Under a filter, a rescan keeps the cursor on the session it was on. The
// row's place in the full list is not its place on screen, and until the
// re-filter lands there is no place on screen at all.
func TestRefillKeepsCursorUnderFilter(t *testing.T) {
	t0 := time.Now()
	a := sessions.Session{ID: "aaaa", Title: "oidc cache", EndedAt: t0}
	b := sessions.Session{ID: "bbbb", Title: "rate limiter", EndedAt: t0}
	c := sessions.Session{ID: "cccc", Title: "oidc discovery", EndedAt: t0}

	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: t.TempDir()}, sessions.Claude, 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(scannedMsg{all: []sessions.Session{a, b, c}})

	p := m.(picker)
	p.list.SetFilterText("oidc")
	for i, it := range p.list.VisibleItems() {
		if it.(row).s.ID == "cccc" {
			p.list.Select(i)
		}
	}

	// The same sessions, reordered, so c's place in the full list moves.
	m, _ = p.Update(scannedMsg{all: []sessions.Session{c, b, a}, at: t0})
	if s, ok := m.(picker).selected(); !ok || s.ID != "cccc" {
		t.Errorf("cursor on %q after rescan, want cccc", s.ID)
	}
}

// writeTranscript puts a one-prompt transcript for id under root, and
// returns a session for it recent enough to show.
func writeTranscript(t *testing.T, root, id, text string) sessions.Session {
	t.Helper()
	dir := filepath.Join(root, "slug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-09-02T09:47:00Z","message":{"content":"` + text + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessions.Session{Provider: sessions.Claude, ID: id, Title: id, EndedAt: time.Now(), Transcript: filepath.Join(dir, id+".jsonl")}
}

// drain runs a command and flattens any batch it yields into the messages
// the program would have delivered.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestPickerIdentityAndStalePromptGeneration(t *testing.T) {
	root := t.TempDir()
	a := writeTranscript(t, root, "same", "Claude thread")
	b := a
	b.Provider = sessions.Codex
	b.Transcript = filepath.Join(root, "codex.jsonl")
	if err := os.WriteFile(b.Transcript, []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"Codex thread"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	var m tea.Model = newPicker(t.Context(), sessions.Catalog{ClaudeRoot: root}, sessions.All, 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.Update(scannedMsg{all: []sessions.Session{a, b}, at: at})
	for _, msg := range drain(cmd) {
		m, _ = m.Update(msg)
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	for _, msg := range drain(cmd) {
		m, _ = m.Update(msg)
	}
	if pane := m.(picker).pane.View(); !strings.Contains(pane, "Codex thread") || strings.Contains(pane, "Claude thread") {
		t.Fatalf("cross-provider cache collision: %s", pane)
	}
	m, cmd = m.Update(scannedMsg{all: []sessions.Session{b, a}, at: at.Add(time.Second)})
	if selected, _ := m.(picker).selected(); selected.Key() != b.Key() {
		t.Fatalf("refresh switched provider: %+v", selected)
	}
	for _, msg := range drain(cmd) {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(promptsMsg{id: b.Key(), at: at, ps: []sessions.Prompt{{N: 1, Kind: sessions.KindPrompt, Text: "obsolete read"}}})
	if pane := m.(picker).pane.View(); !strings.Contains(pane, "Codex thread") || strings.Contains(pane, "obsolete") {
		t.Fatalf("stale generation overwrote pane: %s", pane)
	}
	if ps := m.(picker).prompts[b.Key()]; len(ps) != 1 || ps[0].Text != "Codex thread" {
		t.Fatalf("stale generation refilled cache: %+v", ps)
	}
}
