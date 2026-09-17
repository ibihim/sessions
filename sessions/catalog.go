package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Catalog describes stores, not a cache. Each scan reads their current state.
type Catalog struct {
	ClaudeRoot    string
	ClaudeHistory string
	CodexHome     string
}

func ParseProvider(value string) (Provider, error) {
	p := Provider(value)
	switch p {
	case All, Claude, Codex:
		return p, nil
	default:
		return "", fmt.Errorf("invalid provider %q: use all, claude, or codex", value)
	}
}

func DefaultCatalog() (Catalog, error) {
	root, err := DefaultRoot()
	if err != nil {
		return Catalog{}, err
	}
	history, err := DefaultHistory()
	if err != nil {
		return Catalog{}, err
	}
	home, err := DefaultCodexHome()
	return Catalog{ClaudeRoot: root, ClaudeHistory: history, CodexHome: home}, err
}

func DefaultCodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

// Scan returns usable sessions AND diagnostics. An absent store is normal;
// an unreadable one must not hide the other provider's sessions.
func (c Catalog) Scan(ctx context.Context, provider Provider) ([]Session, error) {
	all, discoveryErr := c.Discover(provider)
	liveErr := c.Enrich(ctx, all)
	return all, errors.Join(discoveryErr, liveErr)
}

// Discover reads only saved history; it is independent of the process table
// and compositor, and also works where live enrichment is unsupported.
func (c Catalog) Discover(provider Provider) ([]Session, error) {
	if _, err := ParseProvider(string(provider)); err != nil {
		return nil, err
	}
	var all []Session
	var problems []error
	if provider == All || provider == Claude {
		found, err := Scan(c.ClaudeRoot)
		all = append(all, found...)
		if err != nil {
			problems = append(problems, fmt.Errorf("claude: %w", err))
		}
	}
	if provider == All || provider == Codex {
		found, err := ScanCodex(c.CodexHome)
		all = append(all, found...)
		if err != nil {
			problems = append(problems, fmt.Errorf("codex: %w", err))
		}
	}
	sortSessions(all)
	return all, errors.Join(problems...)
}

func sortSessions(all []Session) {
	sort.Slice(all, func(i, j int) bool {
		if !all[i].EndedAt.Equal(all[j].EndedAt) {
			return all[i].EndedAt.After(all[j].EndedAt)
		}
		return all[i].Key().String() < all[j].Key().String()
	})
}
