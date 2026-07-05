package main

import (
	"strings"
	"testing"
)

func TestLineDiffMarksChangedLines(t *testing.T) {
	diff, ok := lineDiff("a\nb\nc", "a\nB\nc", 100)
	if !ok {
		t.Fatal("lineDiff should handle small inputs")
	}
	lines := strings.Split(diff, "\n")
	if len(lines) != 4 {
		t.Fatalf("diff has %d lines; want 4 (ctx, -, +, ctx):\n%s", len(lines), diff)
	}
	if !strings.Contains(lines[1], "- b") || !strings.Contains(lines[2], "+ B") {
		t.Fatalf("diff did not mark the changed line:\n%s", diff)
	}
}

func TestLineDiffRejectsOversizedInput(t *testing.T) {
	big := strings.Repeat("x\n", 500)
	if _, ok := lineDiff(big, "y", 400); ok {
		t.Fatal("lineDiff must refuse inputs above maxLines")
	}
}

func TestWrapLinePreservesIndent(t *testing.T) {
	got := wrapLine("    one two three four", 12)
	if len(got) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	for i, line := range got {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("line %d lost its indent: %q", i, line)
		}
		if len([]rune(line)) > 12 {
			t.Fatalf("line %d exceeds width: %q", i, line)
		}
	}
}

func TestWrapLineHardBreaksLongWords(t *testing.T) {
	got := wrapLine(strings.Repeat("a", 50), 20)
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %q", len(got), got)
	}
}

func TestRenderMarkdownKeepsCodeFencesUnstyledInline(t *testing.T) {
	out := renderMarkdown("# Title\n```\ncode line\n```\nplain **bold**", 80)
	if len(out) != 5 {
		t.Fatalf("expected 5 rendered lines, got %d: %q", len(out), out)
	}
	// Styling is ANSI-wrapped, but the raw content must survive.
	joined := strings.Join(out, "\n")
	for _, want := range []string{"Title", "code line", "plain", "bold"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered output lost %q:\n%s", want, joined)
		}
	}
}

func TestPromptHistoryRoundTrip(t *testing.T) {
	root := t.TempDir()
	entries := []string{"first prompt", "multi\nline\nprompt", "/mode plan"}
	savePromptHistory(root, entries)
	got := loadPromptHistory(root)
	if len(got) != len(entries) {
		t.Fatalf("loaded %d entries; want %d", len(got), len(entries))
	}
	for i := range entries {
		if got[i] != entries[i] {
			t.Fatalf("entry %d = %q; want %q", i, got[i], entries[i])
		}
	}
}

func TestPromptHistoryMissingFileIsNil(t *testing.T) {
	if got := loadPromptHistory(t.TempDir()); got != nil {
		t.Fatalf("expected nil history, got %q", got)
	}
}
