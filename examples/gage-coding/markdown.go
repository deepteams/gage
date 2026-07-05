package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	mdHeadingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	mdCodeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	mdFenceStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	mdBoldStyle    = lipgloss.NewStyle().Bold(true)

	mdInlineCodeRe = regexp.MustCompile("`([^`]+)`")
	mdBoldRe       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// renderMarkdown renders assistant text with lightweight markdown styling
// (headings, fenced code blocks, inline code, bold) and soft-wraps everything
// to width. It intentionally avoids a real markdown engine: the TUI only needs
// the common cases readable, not full CommonMark, and wrapping must happen on
// plain text before ANSI styling so widths stay correct.
func renderMarkdown(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	inCode := false
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			out = append(out, mdFenceStyle.Render(line))
			continue
		}
		if inCode {
			for _, w := range wrapLine(line, width) {
				out = append(out, mdCodeStyle.Render(w))
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") && strings.HasPrefix(strings.TrimLeft(trimmed, "#"), " ") {
			for _, w := range wrapLine(line, width) {
				out = append(out, mdHeadingStyle.Render(w))
			}
			continue
		}
		for _, w := range wrapLine(line, width) {
			out = append(out, styleInline(w))
		}
	}
	return out
}

// styleInline applies inline code and bold styling to one already-wrapped
// line. Markers spanning a wrap boundary are left as-is, which is acceptable
// for a demo renderer.
func styleInline(line string) string {
	line = mdInlineCodeRe.ReplaceAllStringFunc(line, func(m string) string {
		return mdCodeStyle.Render(strings.Trim(m, "`"))
	})
	line = mdBoldRe.ReplaceAllStringFunc(line, func(m string) string {
		return mdBoldStyle.Render(strings.TrimSuffix(strings.TrimPrefix(m, "**"), "**"))
	})
	return line
}

// wrapLine soft-wraps a single plain-text line to width runes, preserving the
// line's leading indentation on continuation lines and hard-breaking words
// longer than the width.
func wrapLine(line string, width int) []string {
	if len([]rune(line)) <= width {
		return []string{line}
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	avail := width - len([]rune(indent))
	// Deeply indented lines on a narrow terminal: drop the indent rather than
	// wrapping into unreadable slivers.
	if avail < 4 {
		indent = ""
		avail = width
	}

	var out []string
	current := ""
	flush := func() {
		out = append(out, indent+current)
		current = ""
	}
	for _, word := range strings.Fields(strings.TrimLeft(line, " \t")) {
		for len([]rune(word)) > avail {
			if current != "" {
				flush()
			}
			runes := []rune(word)
			current = string(runes[:avail])
			flush()
			word = string(runes[avail:])
		}
		switch {
		case current == "":
			current = word
		case len([]rune(current))+1+len([]rune(word)) <= avail:
			current += " " + word
		default:
			flush()
			current = word
		}
	}
	if current != "" || len(out) == 0 {
		flush()
	}
	return out
}
