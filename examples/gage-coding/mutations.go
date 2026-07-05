package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

type fileState struct {
	Exists  bool
	Content []byte
}

type fileChange struct {
	Path   string
	Before fileState
	After  fileState
}

type changeSet struct {
	Prompt  string
	Changes map[string]*fileChange
}

type snapshotManager struct {
	root    string
	mu      sync.Mutex
	current *changeSet
	undo    []*changeSet
	redo    []*changeSet
}

func newSnapshotManager(root string) *snapshotManager {
	return &snapshotManager{root: root}
}

func (s *snapshotManager) Begin(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = &changeSet{Prompt: prompt, Changes: map[string]*fileChange{}}
}

func (s *snapshotManager) Discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
}

func (s *snapshotManager) Commit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || len(s.current.Changes) == 0 {
		s.current = nil
		return 0
	}
	s.undo = append(s.undo, s.current)
	s.redo = nil
	n := len(s.current.Changes)
	s.current = nil
	return n
}

func (s *snapshotManager) RecordBefore(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	abs, rel, err := s.resolve(path)
	if err != nil {
		return err
	}
	if _, ok := s.current.Changes[rel]; ok {
		return nil
	}
	before, err := readFileState(abs)
	if err != nil {
		return err
	}
	s.current.Changes[rel] = &fileChange{Path: rel, Before: before}
	return nil
}

func (s *snapshotManager) RecordAfter(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	abs, rel, err := s.resolve(path)
	if err != nil {
		return err
	}
	change, ok := s.current.Changes[rel]
	if !ok {
		before, err := readFileState(abs)
		if err != nil {
			return err
		}
		change = &fileChange{Path: rel, Before: before}
		s.current.Changes[rel] = change
	}
	after, err := readFileState(abs)
	if err != nil {
		return err
	}
	change.After = after
	return nil
}

func (s *snapshotManager) Undo() (string, error) {
	return s.applyTop(false)
}

func (s *snapshotManager) Redo() (string, error) {
	return s.applyTop(true)
}

func (s *snapshotManager) List() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.undo) == 0 && len(s.redo) == 0 {
		return "no tracked changes"
	}
	var b strings.Builder
	if len(s.undo) > 0 {
		fmt.Fprintln(&b, "undo stack:")
		for i := len(s.undo) - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "  %d. %s (%s)\n", len(s.undo)-i, s.undo[i].Prompt, strings.Join(changePaths(s.undo[i]), ", "))
		}
	}
	if len(s.redo) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintln(&b, "redo stack:")
		for i := len(s.redo) - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "  %d. %s (%s)\n", len(s.redo)-i, s.redo[i].Prompt, strings.Join(changePaths(s.redo[i]), ", "))
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *snapshotManager) applyTop(redo bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stack := s.undo
	action := "undo"
	past := "undid"
	if redo {
		stack = s.redo
		action = "redo"
		past = "redid"
	}
	if len(stack) == 0 {
		return "nothing to " + action, nil
	}
	cs := stack[len(stack)-1]
	if err := s.checkCurrentState(cs, redo); err != nil {
		return "", err
	}
	// Apply before mutating the stacks: if a write fails partway (e.g. a file
	// was made read-only), leave the changeset on the stack so the user can fix
	// the cause and retry instead of losing the recorded states.
	if err := s.apply(cs, redo); err != nil {
		return "", err
	}
	if redo {
		s.redo = s.redo[:len(s.redo)-1]
		s.undo = append(s.undo, cs)
	} else {
		s.undo = s.undo[:len(s.undo)-1]
		s.redo = append(s.redo, cs)
	}
	return fmt.Sprintf("%s %d file change(s) from: %s\n%s", past, len(cs.Changes), cs.Prompt, formatChangePaths(cs)), nil
}

func (s *snapshotManager) checkCurrentState(cs *changeSet, redo bool) error {
	for _, change := range cs.Changes {
		want := change.After
		if redo {
			want = change.Before
		}
		abs, _, err := s.resolve(change.Path)
		if err != nil {
			return err
		}
		got, err := readFileState(abs)
		if err != nil {
			return err
		}
		if !sameFileState(got, want) {
			return fmt.Errorf("refusing to apply snapshot: %s changed since it was recorded", change.Path)
		}
	}
	return nil
}

func changePaths(cs *changeSet) []string {
	paths := make([]string, 0, len(cs.Changes))
	for p := range cs.Changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func formatChangePaths(cs *changeSet) string {
	paths := changePaths(cs)
	if len(paths) == 0 {
		return ""
	}
	return "files: " + strings.Join(paths, ", ")
}

func sameFileState(a, b fileState) bool {
	return a.Exists == b.Exists && bytes.Equal(a.Content, b.Content)
}

func (s *snapshotManager) apply(cs *changeSet, redo bool) error {
	for _, change := range cs.Changes {
		state := change.Before
		if redo {
			state = change.After
		}
		abs, _, err := s.resolve(change.Path)
		if err != nil {
			return err
		}
		if !state.Exists {
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, state.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// resolve confines a path to the workspace root, evaluating symlinks the way
// tools.FSConfig does: a purely lexical check would let an in-root symlink (or a
// symlinked parent) redirect a snapshot write/delete outside the root on undo.
// It resolves the deepest existing ancestor and rejects anything whose real
// path escapes the real root.
func (s *snapshotManager) resolve(path string) (abs string, rel string, err error) {
	if path == "" {
		return "", "", fmt.Errorf("empty path")
	}
	root, err := realPath(s.root)
	if err != nil {
		return "", "", err
	}
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Join(root, path)
	}
	real := abs
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		real = resolved
	} else if resolved, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
		real = filepath.Join(resolved, filepath.Base(abs))
	}
	rel, err = filepath.Rel(root, real)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("path %q escapes root", path)
	}
	return real, rel, nil
}

func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func readFileState(path string) (fileState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	return fileState{Exists: true, Content: data}, nil
}

type formatterManager struct {
	root  string
	rules map[string][]string
}

func newFormatterManager(root string, cfg map[string][]string) *formatterManager {
	rules := map[string][]string{}
	if len(cfg) == 0 {
		rules[".go"] = []string{"gofmt", "-w", "$FILE"}
	} else {
		for ext, cmd := range cfg {
			if ext != "" && len(cmd) > 0 {
				if !strings.HasPrefix(ext, ".") {
					ext = "." + ext
				}
				rules[ext] = append([]string(nil), cmd...)
			}
		}
	}
	return &formatterManager{root: root, rules: rules}
}

func (f *formatterManager) Format(ctx context.Context, path string) (string, error) {
	if f == nil {
		return "", nil
	}
	ext := filepath.Ext(path)
	cmdline, ok := f.rules[ext]
	if !ok || len(cmdline) == 0 {
		return "", nil
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(f.root, path)
	}
	args := make([]string, 0, len(cmdline)-1)
	usedFile := false
	for _, arg := range cmdline[1:] {
		if strings.Contains(arg, "$FILE") || strings.Contains(arg, "{file}") {
			usedFile = true
			arg = strings.ReplaceAll(arg, "$FILE", abs)
			arg = strings.ReplaceAll(arg, "{file}", abs)
		}
		args = append(args, arg)
	}
	if !usedFile {
		args = append(args, abs)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, cmdline[0], args...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("formatter %s failed: %s: %w", cmdline[0], string(out), err)
	}
	return strings.Join(append([]string{cmdline[0]}, args...), " "), nil
}

type mutationTool struct {
	inner     gage.Tool
	snapshots *snapshotManager
	format    *formatterManager
}

func wrapMutations(ts []gage.Tool, snapshots *snapshotManager, format *formatterManager) []gage.Tool {
	out := make([]gage.Tool, 0, len(ts))
	for _, t := range ts {
		switch t.Name() {
		case "write_file", "edit":
			out = append(out, &mutationTool{inner: t, snapshots: snapshots, format: format})
		default:
			out = append(out, t)
		}
	}
	return out
}

func (t *mutationTool) Name() string            { return t.inner.Name() }
func (t *mutationTool) Description() string     { return t.inner.Description() }
func (t *mutationTool) Schema() gage.JSONSchema { return t.inner.Schema() }
func (t *mutationTool) Metadata() gage.ToolMetadata {
	return gage.MetadataOf(t.inner)
}
func (t *mutationTool) DescribeCall(input json.RawMessage) string {
	return gage.CallSummaryOf(t.inner, input)
}

func (t *mutationTool) Execute(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
	path := inputPath(input)
	if path != "" && t.snapshots != nil {
		if err := t.snapshots.RecordBefore(path); err != nil {
			return gage.ErrorResult("", err.Error()), nil
		}
	}
	res, err := t.inner.Execute(ctx, input)
	if err != nil || res.IsError || path == "" {
		return res, err
	}
	if formatted, ferr := t.format.Format(ctx, path); ferr != nil {
		res.Content = append(res.Content, gage.TextPart("\nformatter error: "+ferr.Error()))
	} else if formatted != "" {
		res.Content = append(res.Content, gage.TextPart("\nformatted with "+formatted))
	}
	if t.snapshots != nil {
		if err := t.snapshots.RecordAfter(path); err != nil {
			res.Content = append(res.Content, gage.TextPart("\nsnapshot error: "+err.Error()))
		}
	}
	return res, nil
}

func inputPath(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &args)
	return args.Path
}
