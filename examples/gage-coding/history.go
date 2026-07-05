package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const promptHistoryLimit = 100

func promptHistoryPath(root string) string {
	return filepath.Join(root, ".gage-coding", "history")
}

// loadPromptHistory restores the prompt history persisted by a previous
// session. Entries are stored one JSON string per line so multi-line prompts
// survive the round-trip. Best effort: a missing or corrupt file yields nil.
func loadPromptHistory(root string) []string {
	data, err := os.ReadFile(promptHistoryPath(root))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s string
		if json.Unmarshal([]byte(line), &s) == nil && s != "" {
			out = append(out, s)
		}
	}
	if len(out) > promptHistoryLimit {
		out = out[len(out)-promptHistoryLimit:]
	}
	return out
}

// savePromptHistory persists the prompt history. Best effort: the TUI must
// never fail a turn because history could not be written.
func savePromptHistory(root string, entries []string) {
	if len(entries) > promptHistoryLimit {
		entries = entries[len(entries)-promptHistoryLimit:]
	}
	dir := filepath.Join(root, ".gage-coding")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var b strings.Builder
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			continue
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(promptHistoryPath(root), []byte(b.String()), 0o600)
}
