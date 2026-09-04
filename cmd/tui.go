package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ibihim/sessions/sessions"
)

// recent is the window the picker opens on. Three days reaches back over
// a weekend; the month Claude Code keeps is a keypress away, behind the
// trailing "older" row.
const recent = 3 * 24 * time.Hour

// paneText caps how far a prompt may run in the pane. The pane is as wide
// as the terminal, and on a 200-column one prose would otherwise be laid
// out in lines no eye tracks back from the end of.
const paneText = 100

// runPicker is what bare `sessions` does: the listing as a picker, where
// enter opens the session under the cursor — focusing its window if it
// has one, opening a new one if it does not — and f forks it. Below the
// list, a pane shows the tail of the selected session's prompts.
//
// Every scan is a fresh read of disk and registry, as everywhere else in
// the package. The picker owns no state about sessions, only a cursor
// and a cache of what it has read for the pane.
func runPicker(ctx context.Context, root string) error {
	_, err := tea.NewProgram(newPicker(ctx, root), tea.WithContext(ctx)).Run()
	return err
}

type picker struct {
	ctx  context.Context
	root string
	list list.Model
	pane viewport.Model // the tail of the selected session's thread

	all     []sessions.Session // last scan, live first, newest first
	showAll bool               // the older row was taken; the window is gone

	width   int
	prompts map[string][]sessions.Prompt // by session id; dropped on every rescan
	shown   string                       // session id the pane is on; "" for none
}

func newPicker(ctx context.Context, root string) picker {
	l := list.New(nil, rowDelegate{}, 80, 24)
	l.SetStatusBarItemName("session", "sessions")
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{pickerKeys.Open, pickerKeys.Fork}
	}
	// A second is the default, which is long enough to see that something
	// flashed and not what it said. Errors in particular need reading.
	l.StatusMessageLifetime = 4 * time.Second
	// The stock title is a block of purple. Bold on the terminal's own
	// palette keeps to the reasoning in color.go: the theme that picked
	// your background picked something that reads on it.
	l.Styles.Title = lipgloss.NewStyle().Bold(true)
	return picker{
		ctx:     ctx,
		root:    root,
		list:    l,
		pane:    viewport.New(),
		prompts: map[string][]sessions.Prompt{},
	}
}

var pickerKeys = struct{ Open, Fork key.Binding }{
	Open: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	Fork: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fork")),
}

// Messages: what the commands below deliver back into Update.

type scannedMsg struct {
	all []sessions.Session
	err error
}

type openedMsg struct {
	note string // what was done, for the status line
	err  error
}

type promptsMsg struct {
	id  string // the session asked for, so a late answer can be told apart
	ps  []sessions.Prompt
	err error
}

// scan re-reads everything. It runs off the event loop, so the screen
// keeps drawing while the transcripts are folded.
func (p picker) scan() tea.Cmd {
	return func() tea.Msg {
		all, err := sessions.Scan(p.root)
		if err != nil {
			return scannedMsg{err: err}
		}
		sessions.Enrich(p.ctx, all)
		return scannedMsg{all: sessions.LiveFirst(all)}
	}
}

// open does what `sessions open` would, minus the takeover of this
// terminal: the picker has to survive the keypress, so a finished session
// always goes to a new window.
//
// yolo is on, as it is for `open --window`: a session reopened from a
// listing is one you were already running.
func (p picker) open(s sessions.Session, fork bool) tea.Cmd {
	return func() tea.Msg {
		switch {
		case fork:
			return openedMsg{
				note: fmt.Sprintf("forked %q in a new window", s.Title),
				err:  spawnWindow(p.ctx, s, true, true),
			}
		case s.Attached():
			return openedMsg{
				note: fmt.Sprintf("focused %q on workspace %d", s.Title, s.Workspace),
				err:  sessions.Focus(p.ctx, s),
			}
		case s.Live():
			return openedMsg{err: fmt.Errorf(
				"%s is running headless (pid %d) — there is no window to open",
				shortID(s), s.PID)}
		default:
			return openedMsg{
				note: fmt.Sprintf("opened %q in a new window", s.Title),
				err:  spawnWindow(p.ctx, s, true, false),
			}
		}
	}
}

// loadPrompts reads one session's thread, off the event loop. The answer
// carries the id it was asked for: holding j fires a read per row, and
// they finish in any order.
func (p picker) loadPrompts(id string) tea.Cmd {
	return func() tea.Msg {
		path, err := sessions.TranscriptPath(p.root, id)
		if err != nil {
			return promptsMsg{id: id, err: err}
		}
		ps, err := sessions.NewPromptReader(path).Next()
		return promptsMsg{id: id, ps: ps, err: err}
	}
}

func (p picker) Init() tea.Cmd { return p.scan() }

func (p picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.layout(msg.Width, msg.Height)

	case scannedMsg:
		if msg.err != nil {
			cmd = p.list.NewStatusMessage("scan failed: " + msg.err.Error())
			break
		}
		p.all = msg.all
		// The cache goes with the scan. A rescan follows an open, and the
		// session just opened is about to gain prompts; dropping every
		// entry rather than that one saves threading ids through
		// openedMsg, and a reload is tens of milliseconds.
		p.prompts = map[string][]sessions.Prompt{}
		p.shown = ""
		cmd = p.refill()

	case openedMsg:
		note := msg.note
		if msg.err != nil {
			note = msg.err.Error()
		}
		// Rescan either way: on success the row just opened turns live,
		// and on failure the listing is no worse for being fresh.
		cmd = tea.Batch(p.list.NewStatusMessage(note), p.scan())

	case promptsMsg:
		// Cached whichever row the cursor is on now; painted only if it
		// is the one the pane is waiting on. A late answer for a row you
		// have already left would otherwise show under the wrong banner.
		if msg.err == nil {
			p.prompts[msg.id] = msg.ps
		}
		if msg.id == p.shown {
			p.show(msg.ps, msg.err)
		}

	case tea.KeyPressMsg:
		switch {
		case p.list.FilterState() == list.Filtering:
			// While a filter is being typed, every key is text.
			p.list, cmd = p.list.Update(msg)
		case key.Matches(msg, pickerKeys.Open):
			p, cmd = p.take(false)
		case key.Matches(msg, pickerKeys.Fork):
			p, cmd = p.take(true)
		default:
			p.list, cmd = p.list.Update(msg)
		}

	default:
		p.list, cmd = p.list.Update(msg)
	}
	return p, tea.Batch(cmd, p.sync())
}

// take acts on the row under the cursor.
func (p picker) take(fork bool) (picker, tea.Cmd) {
	r, ok := p.list.SelectedItem().(row)
	switch {
	case !ok:
		return p, nil
	case r.older > 0 && fork:
		return p, nil
	case r.older > 0:
		p.showAll = true
		return p, p.refill()
	}
	return p, p.open(r.s, fork)
}

// refill rebuilds the rows from the last scan, keeping the cursor on the
// session it was on: live-first ordering moves a session you just opened
// to the top, and the cursor should follow it there.
func (p *picker) refill() tea.Cmd {
	var keep string
	if r, ok := p.list.SelectedItem().(row); ok {
		keep = r.s.ID
	}

	items := rows(p.all, p.showAll, time.Now())
	cmd := p.list.SetItems(items)
	for i, it := range items {
		if keep != "" && it.(row).s.ID == keep {
			p.list.Select(i)
			break
		}
	}

	p.list.Title = "sessions — last 3 days"
	if p.showAll {
		p.list.Title = "sessions — all"
	}
	return cmd
}

// sync points the pane at the row under the cursor. It runs after every
// message, because the cursor moves on keys, on filter edits and on
// refills alike, and the pane has to follow all three.
func (p *picker) sync() tea.Cmd {
	id := ""
	if s, ok := p.selected(); ok {
		id = s.ID
	}
	if id == p.shown {
		return nil
	}
	p.shown = id
	if id == "" {
		p.pane.SetContent("")
		return nil
	}
	if ps, ok := p.prompts[id]; ok {
		p.show(ps, nil)
		return nil
	}
	// Blank rather than the previous session's thread under this one's
	// banner. The read takes tens of milliseconds; a wrong thread would
	// be believed.
	p.pane.SetContent("")
	return p.loadPrompts(id)
}

// show fills the pane with the tail of a thread, laid out the way
// `sessions prompts` prints it, at the pane's width.
func (p *picker) show(ps []sessions.Prompt, err error) {
	var b strings.Builder
	switch {
	case err != nil:
		b.WriteString(err.Error())
	case len(ps) == 0:
		b.WriteString(faint.Render("no prompts"))
	default:
		writePrompts(&b, ps, time.Time{}, true, min(textWidthFor(p.width), paneText))
	}
	p.pane.SetContent(strings.TrimRight(b.String(), "\n"))
	p.pane.GotoBottom()
}

// layout splits the screen: the list above, the pane below. The pane
// takes a third of the height with a floor of six lines — the rule, the
// banner, and a prompt or two.
func (p *picker) layout(w, h int) {
	p.width = w
	paneH := max(h/3, 6)
	p.list.SetSize(w, h-paneH)
	p.pane.SetWidth(w)
	p.pane.SetHeight(paneH - 2)
	if ps, ok := p.prompts[p.shown]; ok {
		p.show(ps, nil) // rewrap to the new width
	}
}

// selected is the session under the cursor, if the cursor is on one.
func (p picker) selected() (sessions.Session, bool) {
	r, ok := p.list.SelectedItem().(row)
	if !ok || r.older > 0 {
		return sessions.Session{}, false
	}
	return r.s, true
}

func (p picker) View() tea.View {
	var header string
	if s, ok := p.selected(); ok {
		header = banner(s, false)
	}
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left,
		p.list.View(),
		faint.Render(strings.Repeat("─", p.width)),
		header,
		p.pane.View(),
	))
	v.AltScreen = true
	return v
}

// row is one list entry: a session, or the trailing "older" line that
// stands in for the ones outside the window.
type row struct {
	s     sessions.Session
	older int // >0 marks the trailer; it carries no session
}

// FilterValue is what "/" searches. The path is in because "the one in
// the kubernetes worktree" is as common a way to remember a session as its
// title. The trailer offers nothing, so any filter hides it.
func (r row) FilterValue() string {
	if r.older > 0 {
		return ""
	}
	return title(r.s) + " " + where(r.s)
}

// rows picks the sessions to show: everything within the window; every
// running one regardless, since a session you can walk to does not stop
// being so by idling; and, when the window hides any, a trailer that says
// how many.
func rows(all []sessions.Session, showAll bool, now time.Time) []list.Item {
	cutoff := now.Add(-recent)
	items := make([]list.Item, 0, len(all)+1)
	older := 0
	for _, s := range all {
		if showAll || s.Live() || !s.EndedAt.Before(cutoff) {
			items = append(items, row{s: s})
			continue
		}
		older++
	}
	if older > 0 {
		items = append(items, row{older: older})
	}
	return items
}

// title is what a row shows for a session: its title, else the registry
// name a live session carries before Claude titles it, else a placeholder.
// The same fallback writeSessionsText makes inline.
func title(s sessions.Session) string {
	switch {
	case s.Title != "":
		return s.Title
	case s.Name != "":
		return s.Name
	}
	return "(untitled)"
}

// rowDelegate draws a row as one line, in the columns of `sessions list`.
//
// Colour is by state, the mapping paintRow uses: a finished session is dim
// as a whole, a live one has its marker painted. The two never nest, which
// matters — an inner reset would end the outer dim early. The cursor row
// is drawn at full brightness whatever its state, so it stands out from
// the dim rows around it.
type rowDelegate struct{}

func (rowDelegate) Height() int                         { return 1 }
func (rowDelegate) Spacing() int                        { return 0 }
func (rowDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

var (
	faint  = lipgloss.NewStyle().Faint(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

func (rowDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	r, ok := item.(row)
	if !ok {
		return
	}
	selected := index == m.Index()

	var line string
	switch {
	case r.older > 0:
		line = fmt.Sprintf("… %d older — enter to show all", r.older)
		if !selected {
			line = faint.Render(line)
		}
	case !r.s.Live():
		line = rowText(r.s)
		if !selected {
			line = faint.Render(line)
		}
	case !r.s.Attached():
		line = strings.Replace(rowText(r.s), "●", yellow.Render("●"), 1)
	default:
		line = strings.Replace(rowText(r.s), "●", green.Render("●"), 1)
	}

	cursor := "  "
	if selected {
		cursor = "> "
	}
	fmt.Fprint(w, lipgloss.NewStyle().MaxWidth(m.Width()).Render(cursor+line))
}

// rowText lays the columns out at fixed widths rather than through
// tabwriter: each row is rendered on its own, so there is no table to
// align against. The widths are the listing's widest values — STATUS is
// "● headless", TITLE is cut to the same 52 runes. WHERE is dim as in the
// listing, and last, so its reset ends nothing but itself.
func rowText(s sessions.Session) string {
	return fmt.Sprintf("%-10s %2s %4s %5d  %-52s  %s",
		status(s), workspace(s), age(s.EndedAt), s.Messages,
		truncate(title(s), 52), faint.Render(where(s)))
}
