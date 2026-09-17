//go:build !linux

package sessions

import "fmt"

func processVisibility() error { return nil }

func codexLiveProcs(home string) (map[string]liveProc, error) {
	return nil, fmt.Errorf("Codex writer-lock detection requires Linux; saved history is still available")
}
