# sessions

Claude Code and Codex sessions: what you were doing, and where it is.

Reads Claude Code transcripts from `~/.claude/projects` and Codex rollouts
from `$CODEX_HOME/sessions` (default `~/.codex/sessions`). List both tools,
search what you typed, replay your prompts, and return to a conversation.
Run bare for the picker; the commands below work without the UI.

Both providers are enabled by default. Every command accepts
`--provider all|claude|codex`. Missing stores are fine; failures in one
provider produce warnings while the other remains usable.

## Install

    go install github.com/ibihim/sessions@latest

## Requirements

Saved history can be read on Linux and macOS. Opening a session requires
its provider's CLI (`claude` or `codex`) on `PATH` and its recorded working
directory to still exist.

- **Linux `/proc`**: Codex live detection verifies held writer locks and
  process start times. Saved `source: "cli"` metadata does not imply a live
  terminal. Codex busy/idle status is left unset because locks establish
  only that the process is running.
- **Hyprland** (`hyprctl`): workspace display and focusing existing terminal
  windows. Duplicate or unmatched titles have no focus target. Codex titles
  can be `session title | project name`, with an optional activity glyph;
  a project name alone cannot identify a session.
- **Ghostty**: `open --window` and the picker's resume/fork actions. The CLI
  is passed as an absolute path, and Codex's home is passed to the new window.

On unsupported platforms or with restricted process access, live status is
`unknown`, not `stopped`. History remains readable. `open` refuses to resume
when liveness is unknown, or when a known running session has no identified
window; `--fork` can still create a separate conversation. Focusing an editor
conversation is outside this version's scope; saved editor sessions resume
through the Codex CLI.

`open --window` enables permission bypass by default: Claude receives
`--dangerously-skip-permissions`; Codex receives
`--dangerously-bypass-approvals-and-sandbox`. `--yolo=false` disables this.
Opening in the current terminal adds neither flag.

## Usage

### Picker

    sessions
    sessions --interval 5s           # rescan every 5s rather than 30s; 0 never
    sessions --provider codex        # only Codex sessions

The last three days of sessions, running ones first, in the order you
launched them, so they hold still while they work. Enter opens the one
under the cursor: focuses a running session's identified window, or resumes
a stopped session in a new window. `f` forks it into a new window. `/` filters by title or path. The
selected session's prompts show underneath. The `TOOL` column distinguishes
Claude from Codex, and Codex subagents are grouped under their parent.

### list

    sessions list                    # running now, with their workspace
    sessions list -a                 # finished and headless sessions too
    sessions list --since yesterday  # what you worked on; also 4h, 2d, 2006-01-02
    sessions list -w                 # redraw every 2s until ctrl-c
    sessions list -a --provider codex

### find

    sessions find "rate limiter"

Searches the prompts you typed. Plain substring, case-insensitive, no regex.
Claude uses its history log; Codex uses user-message events in rollouts,
including editor sessions. Injected instructions and tool output are excluded.

### prompts

    sessions prompts                 # the session started in this directory
    sessions prompts 3f2a            # by id prefix, or a piece of the title
    sessions prompts codex:3f2a      # provider-qualified selector
    sessions prompts -f              # keep printing as you type

Your side of a conversation, oldest first: prompts, recorded slash commands,
image markers, and interruptions. Nothing is truncated. Without a selector,
the command prefers a live session in this directory; several live matches
require an explicit selector. Following retries incomplete final lines and
preserves separate turns even when their text is identical.

### open

    sessions open 3f2a               # focus its window, or resume it here
    sessions open 3f2a -w            # resume in a new ghostty window instead
    sessions open 3f2a -f            # fork: new session id, original untouched

An ID prefix or part of a title must name one session. Use `codex:3f2a` or
`claude:3f2a` to disambiguate providers; ambiguous matches show qualified IDs.

| Action | Claude Code | Codex |
|---|---|---|
| Resume | `claude --resume ID` | `codex resume ID` |
| Fork | `claude --resume ID --fork-session` | `codex fork ID` |

See the [official Codex command reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli)
for resume and fork behavior.

## JSON

`list`, `find`, and `prompts` take `--json` and emit one shape:

    {
      "source": "sessions",
      "fetched_at": "2026-09-04T10:00:00Z",
      "items": [ ... ]
    }

Each session adds `provider` (`claude` or `codex`) and `live_state`
(`running`, `stopped`, or `unknown`). Existing session fields and the envelope
are retained. `find` returns sessions inside its hits; `prompts` retains its
existing prompt-item format. Diagnostics go to stderr, leaving stdout as JSON.

## Development

    go test ./...
    go test -race ./...
    go vet ./...

Tests use synthetic transcripts and disposable files. Coverage includes both
Codex prompt formats, duplicate IDs across providers, incremental following,
released writer locks, ambiguous windows, picker refresh, and exact CLI
arguments without launching conversations.

## License

MIT
