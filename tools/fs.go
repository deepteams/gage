package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
)

// FSConfig confines filesystem tools to a root directory. A zero Root means the
// process working directory with no confinement.
type FSConfig struct {
	// Root, when set, is the base directory. Paths are resolved against it and
	// may not escape it.
	Root string
	// MaxReadBytes caps read_file output (default 1 MiB).
	MaxReadBytes int64
}

func (c FSConfig) maxRead() int64 {
	if c.MaxReadBytes > 0 {
		return c.MaxReadBytes
	}
	return 1 << 20
}

// resolve joins a user path against Root and prevents lexical escapes. Call
// resolveExisting or resolveForWrite when the target may involve symlinks.
func (c FSConfig) resolve(p string) (string, error) {
	if c.Root == "" {
		return p, nil
	}
	root, err := filepath.Abs(c.Root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(root, p)
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes root", p)
	}
	return joined, nil
}

func (c FSConfig) resolveExisting(p string) (string, error) {
	joined, err := c.resolve(p)
	if err != nil || c.Root == "" {
		return joined, err
	}
	return c.realPathInRoot(joined, p)
}

func (c FSConfig) resolveForWrite(p string) (string, error) {
	joined, err := c.resolve(p)
	if err != nil || c.Root == "" {
		return joined, err
	}
	root, err := c.realRoot()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(joined); err == nil {
		if !withinRoot(root, real) {
			return "", fmt.Errorf("path %q escapes root", p)
		}
		return joined, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	parent := filepath.Dir(joined)
	ancestor, err := existingAncestor(parent)
	if err != nil {
		return "", err
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, realAncestor) {
		return "", fmt.Errorf("path %q escapes root", p)
	}
	return joined, nil
}

func (c FSConfig) realPathInRoot(path, userPath string) (string, error) {
	root, err := c.realRoot()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, real) {
		return "", fmt.Errorf("path %q escapes root", userPath)
	}
	return real, nil
}

func (c FSConfig) realRoot() (string, error) {
	root, err := filepath.Abs(c.Root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return real, nil
}

func existingAncestor(path string) (string, error) {
	for {
		if _, err := os.Lstat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		path = parent
	}
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// NewFSTools returns the read_file, write_file, edit and list_dir tools.
func NewFSTools(cfg FSConfig) []gage.Tool {
	return []gage.Tool{
		&readFileTool{cfg}, &writeFileTool{cfg}, &editTool{cfg}, &listDirTool{cfg},
	}
}

// ---- read_file ----

type readFileTool struct{ cfg FSConfig }

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read the contents of a file at the given path. Large files are returned in chunks: use offset/limit (in bytes) to page through them."
}
func (t *readFileTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"path":   jsonschema.Str("Path to the file to read."),
		"offset": jsonschema.Int("Byte offset to start reading from (default 0)."),
		"limit":  jsonschema.Int("Maximum number of bytes to return (default: the tool's read cap)."),
	}, "path")
}

const readTruncationMarker = "\n...(truncated, use offset to read more)"

func (t *readFileTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Limit  int64  `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if args.Offset < 0 {
		return errResult(fmt.Errorf("offset must be >= 0")), nil
	}
	limit := t.cfg.maxRead()
	if args.Limit > 0 && args.Limit < limit {
		limit = args.Limit
	}
	p, err := t.cfg.resolveExisting(args.Path)
	if err != nil {
		return errResult(err), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return errResult(err), nil
	}
	defer f.Close()
	if args.Offset > 0 {
		if _, err := f.Seek(args.Offset, io.SeekStart); err != nil {
			return errResult(err), nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return errResult(err), nil
	}
	text := string(data)
	// Peek one byte past the window to report truncation explicitly.
	var one [1]byte
	if n, _ := f.Read(one[:]); n > 0 {
		text += readTruncationMarker
	}
	return gage.TextResult("", text), nil
}

// ---- write_file ----

type writeFileTool struct{ cfg FSConfig }

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Description() string {
	return "Write content to a file, creating or overwriting it and any parent directories."
}
func (t *writeFileTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"path":    jsonschema.Str("Path to the file to write."),
		"content": jsonschema.Str("The full content to write."),
	}, "path", "content")
}
func (t *writeFileTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	p, err := t.cfg.resolveForWrite(args.Path)
	if err != nil {
		return errResult(err), nil
	}
	if dir := filepath.Dir(p); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errResult(err), nil
		}
	}
	if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil {
		return errResult(err), nil
	}
	return gage.TextResult("", fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)), nil
}

// ---- edit ----

type editTool struct{ cfg FSConfig }

func (t *editTool) Name() string { return "edit" }
func (t *editTool) Description() string {
	return "Replace an exact occurrence of old_string with new_string in a file. Fails if old_string is missing or (unless replace_all) not unique."
}
func (t *editTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"path":        jsonschema.Str("Path to the file to edit."),
		"old_string":  jsonschema.Str("Exact text to replace."),
		"new_string":  jsonschema.Str("Replacement text."),
		"replace_all": jsonschema.Bool("Replace all occurrences instead of requiring uniqueness."),
	}, "path", "old_string", "new_string")
}
func (t *editTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return gage.ToolResult{}, err
	}
	if args.OldString == "" {
		return errResult(fmt.Errorf("old_string must not be empty")), nil
	}
	p, err := t.cfg.resolveExisting(args.Path)
	if err != nil {
		return errResult(err), nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return errResult(err), nil
	}
	content := string(data)
	count := strings.Count(content, args.OldString)
	if count == 0 {
		return errResult(fmt.Errorf("old_string not found in %s", args.Path)), nil
	}
	if count > 1 && !args.ReplaceAll {
		return errResult(fmt.Errorf("old_string is not unique in %s (%d matches); set replace_all or add context", args.Path, count)), nil
	}
	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return errResult(err), nil
	}
	return gage.TextResult("", fmt.Sprintf("edited %s (%d replacement(s))", args.Path, count)), nil
}

// ---- list_dir ----

type listDirTool struct{ cfg FSConfig }

func (t *listDirTool) Name() string        { return "list_dir" }
func (t *listDirTool) Description() string { return "List the entries of a directory." }
func (t *listDirTool) Schema() gage.JSONSchema {
	return jsonschema.Object(map[string]jsonschema.Property{
		"path": jsonschema.Str("Directory path (defaults to root)."),
	})
}
func (t *listDirTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &args)
	if args.Path == "" {
		args.Path = "."
	}
	p, err := t.cfg.resolveExisting(args.Path)
	if err != nil {
		return errResult(err), nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return errResult(err), nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return gage.TextResult("", strings.Join(names, "\n")), nil
}

func errResult(err error) gage.ToolResult {
	return gage.ErrorResult("", err.Error())
}
