package cmd

import (
	"encoding/json"
	"io"
	"time"
)

// envelope is the JSON wrapper every --json verb emits, so a script or an
// agent can rely on one shape across list, find and prompts.
//
// Source names what the items are ("sessions", "prompts"). FetchedAt is
// the moment the CLI finished gathering, in UTC. Items is typed per verb,
// so verb-specific fields survive without being shoved into a generic map.
type envelope[T any] struct {
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
	Items     []T       `json:"items"`
}

// writeJSON marshals an envelope to w with 2-space indent. FetchedAt is
// stamped at call time. A nil items slice is rewritten to an empty slice
// so the JSON is consistent across empty and populated result sets
// (`"items": []` rather than `"items": null`).
func writeJSON[T any](w io.Writer, source string, items []T) error {
	if items == nil {
		items = []T{}
	}
	env := envelope[T]{
		Source:    source,
		FetchedAt: time.Now().UTC(),
		Items:     items,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
