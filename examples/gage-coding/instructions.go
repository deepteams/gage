package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type instructionBundle struct {
	Files []string
	Text  string
}

func loadInstructions(root string, extra []string) (instructionBundle, error) {
	patterns := []string{
		"AGENTS.md",
		"CLAUDE.md",
		".agents/rules/*.md",
	}
	patterns = append(patterns, extra...)

	seen := map[string]bool{}
	var files []string
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "http://") || strings.HasPrefix(pattern, "https://") {
			// Keep the example deterministic and offline; real products can fetch
			// remote instructions with an authenticated HTTP client.
			continue
		}
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(root, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return instructionBundle{}, err
		}
		if len(matches) == 0 {
			if _, err := os.Stat(pattern); err == nil {
				matches = []string{pattern}
			}
		}
		for _, path := range matches {
			abs, err := filepath.Abs(path)
			if err != nil {
				return instructionBundle{}, err
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true
			files = append(files, abs)
		}
	}
	sort.Strings(files)

	var b strings.Builder
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return instructionBundle{}, err
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## %s\n\n%s", relDisplay(root, file), strings.TrimSpace(string(data)))
	}
	return instructionBundle{Files: files, Text: b.String()}, nil
}

func initAgentsFile(root string) (string, error) {
	path := filepath.Join(root, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return "AGENTS.md already exists; leaving it untouched.", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	template := `# Project Instructions

## Working Style

- Inspect the relevant code before changing it.
- Prefer small, targeted edits over broad rewrites.
- Keep generated output and unrelated formatting churn out of commits.
- After code changes, run the smallest meaningful verification command and report the result.

## Repository Notes

- Add project-specific architecture, test, style, and release notes here.
- Keep this file committed so coding agents share the same project context.
`
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return "", err
	}
	return "created AGENTS.md with starter project instructions.", nil
}
