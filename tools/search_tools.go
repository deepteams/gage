package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
)

// NewSearchTools returns the grep and glob tools confined to cfg.Root.
func NewSearchTools(cfg FSConfig) []gage.Tool {
	return []gage.Tool{&grepTool{cfg}, &globTool{cfg}}
}

const maxMatches = 200

// ---- grep ----

type grepTool struct{ cfg FSConfig }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search file contents for a regular expression and return matching lines with file:line prefixes."
}
func (t *grepTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"pattern":          jsonschema.Str("Regular expression (RE2 syntax)."),
		"path":             jsonschema.Str("Directory or file to search (defaults to root)."),
		"glob":             jsonschema.Str("Optional filename glob filter, e.g. *.go."),
		"case_insensitive": jsonschema.Bool("Match case-insensitively (like grep -i)."),
		"context":          jsonschema.Int("Number of context lines to include before and after each match (like grep -C)."),
	}, "pattern")
}

const maxContextLines = 10

func (t *grepTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Glob            string `json:"glob"`
		CaseInsensitive bool   `json:"case_insensitive"`
		Context         int    `json:"context"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	pattern := args.Pattern
	if args.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(fmt.Errorf("invalid pattern: %w", err)), nil
	}
	if args.Context < 0 {
		args.Context = 0
	}
	if args.Context > maxContextLines {
		args.Context = maxContextLines
	}
	if args.Path == "" {
		args.Path = "."
	}
	base, err := t.cfg.resolveExisting(args.Path)
	if err != nil {
		return errResult(err), nil
	}

	var matches []string
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if args.Glob != "" {
			if ok, _ := filepath.Match(args.Glob, d.Name()); !ok {
				return nil
			}
		}
		rel, _ := filepath.Rel(base, path)
		matchFile(path, rel, re, args.Context, &matches)
		if len(matches) >= maxMatches {
			return errStop
		}
		return nil
	})
	if walkErr != nil && walkErr != errStop {
		if ctx.Err() != nil {
			return gage.ToolResult{}, ctx.Err()
		}
		return errResult(walkErr), nil
	}
	if len(matches) == 0 {
		return gage.TextResult("", "no matches"), nil
	}
	return gage.TextResult("", strings.Join(matches, "\n")), nil
}

// matchFile appends matching lines of one file to matches. Match lines use a
// "file:line:text" prefix; when contextLines > 0, surrounding lines use
// "file:line-text" and non-contiguous groups are separated by "--", like
// grep -nC.
func matchFile(path, rel string, re *regexp.Regexp, contextLines int, matches *[]string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	if contextLines <= 0 {
		line := 0
		for sc.Scan() {
			line++
			if re.Match(sc.Bytes()) {
				*matches = append(*matches, fmt.Sprintf("%s:%d:%s", rel, line, sc.Text()))
				if len(*matches) >= maxMatches {
					return
				}
			}
		}
		return
	}

	// Context requested: buffer the file's lines so windows can be assembled.
	var lines []string
	hit := map[int]bool{}
	for sc.Scan() {
		text := sc.Text()
		if re.MatchString(text) {
			hit[len(lines)] = true
		}
		lines = append(lines, text)
	}
	if len(hit) == 0 {
		return
	}
	include := map[int]bool{}
	for i := range hit {
		lo := i - contextLines
		if lo < 0 {
			lo = 0
		}
		hi := i + contextLines
		if hi > len(lines)-1 {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			include[j] = true
		}
	}
	prev := -2
	for i := 0; i < len(lines); i++ {
		if !include[i] {
			continue
		}
		if prev >= 0 && i != prev+1 {
			*matches = append(*matches, "--")
		}
		sep := "-"
		if hit[i] {
			sep = ":"
		}
		*matches = append(*matches, fmt.Sprintf("%s:%d%s%s", rel, i+1, sep, lines[i]))
		prev = i
		if len(*matches) >= maxMatches {
			return
		}
	}
}

// ---- glob ----

type globTool struct{ cfg FSConfig }

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "Find files matching a glob pattern (e.g. **/*.go) and return their paths."
}
func (t *globTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"pattern": jsonschema.Str("Glob pattern; ** matches any number of directories."),
		"path":    jsonschema.Str("Base directory (defaults to root)."),
	}, "pattern")
}

func (t *globTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if args.Path == "" {
		args.Path = "."
	}
	base, err := t.cfg.resolveExisting(args.Path)
	if err != nil {
		return errResult(err), nil
	}

	var found []string
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if matchGlob(args.Pattern, rel) {
			found = append(found, rel)
			if len(found) >= maxMatches {
				return errStop
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != errStop {
		if ctx.Err() != nil {
			return gage.ToolResult{}, ctx.Err()
		}
		return errResult(walkErr), nil
	}
	sort.Strings(found)
	if len(found) == 0 {
		return gage.TextResult("", "no files matched"), nil
	}
	return gage.TextResult("", strings.Join(found, "\n")), nil
}

// matchGlob supports ** (any depth) in addition to filepath.Match semantics.
func matchGlob(pattern, name string) bool {
	if strings.Contains(pattern, "**") {
		// Convert ** to a regexp fragment; * stays single-segment.
		re := globToRegexp(pattern)
		return re.MatchString(name)
	}
	// Try both the full relative path and the base name.
	if ok, _ := filepath.Match(pattern, name); ok {
		return true
	}
	ok, _ := filepath.Match(pattern, filepath.Base(name))
	return ok
}

func globToRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				// consume an optional following slash so **/x matches x
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(pattern[i])
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteByte(pattern[i])
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile("^$")
	}
	return re
}

var errStop = fmt.Errorf("stop")

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".idea", ".vscode":
		return true
	}
	return false
}
