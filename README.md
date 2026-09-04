# sessions

Claude Code sessions: what you were doing, and where it is.

Claude Code leaves one transcript per session under `~/.claude/projects`.
This tool reads them back as sessions you can list, search by what you
typed, replay, and return to. Run bare, it is a picker; the verbs below are
the same thing without the UI.

## Install

    go install github.com/ibihim/sessions@latest

## Requirements

Works anywhere Claude Code writes `~/.claude/projects` (Linux, macOS): the
picker, `list`, `find`, `prompts`, and `open` on a finished session, which
hands this terminal over to `claude --resume`.

Two features lean on a specific desktop:

- **Hyprland** (`hyprctl`): the workspace column in `list`, and focusing the
  window of a session that is still running. Without it, running sessions
  still show up; they just have no window to jump to.
- **ghostty**: `open --window`, which resumes a session in a new terminal
  window instead of this one.

`open --window` passes `--dangerously-skip-permissions` to the resumed
session by default. `--yolo=false` turns that off.

## Usage

### Picker

    sessions

The last three days of sessions, running ones first. Enter opens the one
under the cursor: focuses its window if it has one, opens a new one if it
does not. `f` forks it into a new window. `/` filters by title or path. The
selected session's prompts show underneath.

### list

    sessions list                    # running now, with their workspace
    sessions list -a                 # finished and headless sessions too
    sessions list --since yesterday  # what you worked on; also 4h, 2d, 2006-01-02
    sessions list -w                 # redraw every 2s until ctrl-c

### find

    sessions find "rate limiter"

Searches the prompts you typed, not what Claude replied. Plain substring,
case-insensitive, no regex.

### prompts

    sessions prompts                 # the session started in this directory
    sessions prompts 3f2a            # by id prefix, or a piece of the title
    sessions prompts -f              # keep printing as you type

Your side of a conversation, oldest first: prompts, slash commands,
interruptions. Nothing is truncated.

### open

    sessions open 3f2a               # focus its window, or resume it here
    sessions open 3f2a -w            # resume in a new ghostty window instead
    sessions open 3f2a -f            # fork: new session id, original untouched

An id prefix or part of a title, whatever names one session.

## JSON

`list`, `find`, and `prompts` take `--json` and emit one shape:

    {
      "source": "sessions",
      "fetched_at": "2026-09-04T10:00:00Z",
      "items": [ ... ]
    }

## License

MIT
