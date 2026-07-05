package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type appConfig struct {
	Path         string
	Model        string                     `json:"model,omitempty"`
	DefaultMode  string                     `json:"default_mode,omitempty"`
	Instructions []string                   `json:"instructions,omitempty"`
	Tools        map[string]bool            `json:"tools,omitempty"`
	Permission   json.RawMessage            `json:"permission,omitempty"`
	Command      map[string]inlineCommand   `json:"command,omitempty"`
	Formatters   map[string][]string        `json:"formatters,omitempty"`
	MCP          map[string]mcpServerConfig `json:"mcp,omitempty"`
	MaxTurns     int                        `json:"max_turns,omitempty"`
	CompactAfter int                        `json:"compact_after,omitempty"`
	ToolTimeout  durationSeconds            `json:"tool_timeout_seconds,omitempty"`
}

type inlineCommand struct {
	Description string `json:"description,omitempty"`
	Template    string `json:"template"`
	Mode        string `json:"mode,omitempty"`
}

type mcpServerConfig struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	CWD         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	TimeoutMS   int               `json:"timeout,omitempty"`
}

type durationSeconds time.Duration

func (d *durationSeconds) UnmarshalJSON(raw []byte) error {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		*d = durationSeconds(time.Duration(n) * time.Second)
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = durationSeconds(parsed)
	return nil
}

func (d durationSeconds) Duration() time.Duration {
	return time.Duration(d)
}

func loadConfig(root, explicit string) (appConfig, error) {
	var cfg appConfig
	path, err := findConfigPath(root, explicit)
	if err != nil {
		return cfg, err
	}
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	raw = stripJSONComments(raw)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

func findConfigPath(root, explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}
	for _, name := range []string{".gage-coding.json", ".gage-coding.jsonc"} {
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

func stripJSONComments(src []byte) []byte {
	var out bytes.Buffer
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(src) {
			switch src[i+1] {
			case '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				if i < len(src) {
					out.WriteByte('\n')
				}
				continue
			case '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					if src[i] == '\n' {
						out.WriteByte('\n')
					}
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func relDisplay(root, path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
