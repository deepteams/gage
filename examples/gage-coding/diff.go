package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	diffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	diffCtxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// lineDiff renders a unified-style line diff of two small texts, for approval
// previews of edit calls. It reports ok=false when either side exceeds
// maxLines, so callers can fall back to plain old/new blocks instead of
// running the quadratic LCS on large inputs.
func lineDiff(oldText, newText string, maxLines int) (string, bool) {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	if len(oldLines) > maxLines || len(newLines) > maxLines {
		return "", false
	}

	// Classic LCS table; inputs are capped so O(n*m) stays trivial.
	n, m := len(oldLines), len(newLines)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, diffCtxStyle.Render("  "+oldLines[i]))
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffDelStyle.Render("- "+oldLines[i]))
			i++
		default:
			out = append(out, diffAddStyle.Render("+ "+newLines[j]))
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffDelStyle.Render("- "+oldLines[i]))
	}
	for ; j < m; j++ {
		out = append(out, diffAddStyle.Render("+ "+newLines[j]))
	}
	return strings.Join(out, "\n"), true
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
