package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepteams/gage"
)

func run(t *testing.T, tool gage.Tool, input string) gage.ToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("%s execute error: %v", tool.Name(), err)
	}
	return res
}

func toolByName(tools []gage.Tool, name string) gage.Tool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func TestFSToolsRoundTrip(t *testing.T) {
	root := t.TempDir()
	fs := NewFSTools(FSConfig{Root: root})
	write := toolByName(fs, "write_file")
	read := toolByName(fs, "read_file")
	edit := toolByName(fs, "edit")
	list := toolByName(fs, "list_dir")

	run(t, write, `{"path":"sub/a.txt","content":"hello world"}`)
	if got := run(t, read, `{"path":"sub/a.txt"}`).Text(); got != "hello world" {
		t.Fatalf("read = %q", got)
	}
	run(t, edit, `{"path":"sub/a.txt","old_string":"world","new_string":"gage"}`)
	if got := run(t, read, `{"path":"sub/a.txt"}`).Text(); got != "hello gage" {
		t.Fatalf("after edit = %q", got)
	}
	if got := run(t, list, `{"path":"sub"}`).Text(); got != "a.txt" {
		t.Fatalf("list = %q", got)
	}
}

func TestEditNonUnique(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("x x x"), 0o644)
	edit := toolByName(NewFSTools(FSConfig{Root: root}), "edit")
	res := run(t, edit, `{"path":"f.txt","old_string":"x","new_string":"y"}`)
	if !res.IsError {
		t.Fatal("expected error for non-unique match")
	}
	// replace_all succeeds.
	res = run(t, edit, `{"path":"f.txt","old_string":"x","new_string":"y","replace_all":true}`)
	if res.IsError {
		t.Fatalf("replace_all failed: %s", res.Text())
	}
}

func TestEditRejectsEmptyOldString(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("abc"), 0o644)
	edit := toolByName(NewFSTools(FSConfig{Root: root}), "edit")
	res := run(t, edit, `{"path":"f.txt","old_string":"","new_string":"x","replace_all":true}`)
	if !res.IsError || !strings.Contains(res.Text(), "must not be empty") {
		t.Fatalf("expected empty old_string error, got %q", res.Text())
	}
}

func TestFSRootConfinement(t *testing.T) {
	root := t.TempDir()
	read := toolByName(NewFSTools(FSConfig{Root: root}), "read_file")
	res := run(t, read, `{"path":"../../../etc/passwd"}`)
	if !res.IsError || !strings.Contains(res.Text(), "escapes root") {
		t.Fatalf("expected confinement error, got %q", res.Text())
	}
}

func TestFSRootConfinementRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	fs := NewFSTools(FSConfig{Root: root})
	read := toolByName(fs, "read_file")
	write := toolByName(fs, "write_file")

	res := run(t, read, `{"path":"link/secret.txt"}`)
	if !res.IsError || !strings.Contains(res.Text(), "escapes root") {
		t.Fatalf("expected symlink read confinement error, got %q", res.Text())
	}
	res = run(t, write, `{"path":"link/new.txt","content":"oops"}`)
	if !res.IsError || !strings.Contains(res.Text(), "escapes root") {
		t.Fatalf("expected symlink write confinement error, got %q", res.Text())
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("write escaped through symlink")
	}
}

func TestBashTool(t *testing.T) {
	bash := NewBashTool(BashConfig{})
	res := run(t, bash, `{"command":"echo hi"}`)
	if strings.TrimSpace(res.Text()) != "hi" {
		t.Fatalf("bash = %q", res.Text())
	}
}

func TestBashTimeout(t *testing.T) {
	bash := NewBashTool(BashConfig{})
	res := run(t, bash, `{"command":"sleep 5","timeout_seconds":1}`)
	if !res.IsError || !strings.Contains(res.Text(), "timed out") {
		t.Fatalf("expected timeout, got %q", res.Text())
	}
}

func TestGrepAndGlob(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\nfunc Foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("nothing here\n"), 0o644)
	st := NewSearchTools(FSConfig{Root: root})
	grep := toolByName(st, "grep")
	glob := toolByName(st, "glob")

	res := run(t, grep, `{"pattern":"func \\w+","glob":"*.go"}`)
	if !strings.Contains(res.Text(), "a.go:2") {
		t.Fatalf("grep = %q", res.Text())
	}
	res = run(t, glob, `{"pattern":"**/*.go"}`)
	if !strings.Contains(res.Text(), "a.go") || strings.Contains(res.Text(), "b.txt") {
		t.Fatalf("glob = %q", res.Text())
	}
}

func TestPermissionGuard(t *testing.T) {
	root := t.TempDir()
	write := toolByName(NewFSTools(FSConfig{Root: root}), "write_file")
	deny := gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
		return gage.Denied("policy says no"), nil
	})
	guarded := Guard(write, deny, "agent")
	res := run(t, guarded, `{"path":"x.txt","content":"data"}`)
	if !res.IsError || !strings.Contains(res.Text(), "permission denied") || !strings.Contains(res.Text(), "policy says no") {
		t.Fatalf("expected denial with reason, got %q", res.Text())
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("file should not have been written")
	}
}

func TestPermissionGuardUpdatedInput(t *testing.T) {
	root := t.TempDir()
	write := toolByName(NewFSTools(FSConfig{Root: root}), "write_file")
	rewrite := gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
		a := gage.Allowed()
		a.UpdatedInput = json.RawMessage(`{"path":"safe.txt","content":"data"}`)
		return a, nil
	})
	guarded := Guard(write, rewrite, "agent")
	res := run(t, guarded, `{"path":"orig.txt","content":"data"}`)
	if res.IsError {
		t.Fatalf("expected success, got %q", res.Text())
	}
	if _, err := os.Stat(filepath.Join(root, "safe.txt")); err != nil {
		t.Fatal("rewritten path should have been written")
	}
	if _, err := os.Stat(filepath.Join(root, "orig.txt")); !os.IsNotExist(err) {
		t.Fatal("original path should not have been written")
	}
}

func TestToolMetadataAndSummaryPropagateThroughWrappers(t *testing.T) {
	write := toolByName(NewFSTools(FSConfig{Root: t.TempDir()}), "write_file")
	guarded := Guard(write, gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
		if !req.Metadata.Filesystem || !req.Metadata.RequiresApproval {
			t.Fatalf("permission metadata = %+v", req.Metadata)
		}
		if !strings.Contains(req.Summary, "a.txt") {
			t.Fatalf("permission summary = %q", req.Summary)
		}
		return gage.Denied(""), nil
	}), "agent")
	limited := LimitConcurrency(guarded, 1)

	meta := gage.MetadataOf(limited)
	if !meta.Filesystem || !meta.Destructive || !meta.RequiresApproval {
		t.Fatalf("wrapped metadata = %+v", meta)
	}
	summary := gage.CallSummaryOf(limited, json.RawMessage(`{"path":"a.txt","content":"hello"}`))
	if !strings.Contains(summary, "a.txt") || !strings.Contains(summary, "5 bytes") {
		t.Fatalf("wrapped summary = %q", summary)
	}
	res := run(t, limited, `{"path":"a.txt","content":"hello"}`)
	if !res.IsError || !strings.Contains(res.Text(), "permission denied") {
		t.Fatalf("expected denial, got %q", res.Text())
	}
}

func TestLimitConcurrency(t *testing.T) {
	var running int32
	release := make(chan struct{})
	base := ToolFuncMust("limited", "limited", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		atomic.AddInt32(&running, 1)
		defer atomic.AddInt32(&running, -1)
		select {
		case <-release:
			return gage.TextResult("", "done"), nil
		case <-ctx.Done():
			return gage.ErrorResult("", ctx.Err().Error()), nil
		}
	})
	limited := LimitConcurrency(base, 1)

	firstDone := make(chan gage.ToolResult, 1)
	firstErr := make(chan error, 1)
	go func() {
		res, err := limited.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			firstErr <- err
			return
		}
		firstDone <- res
	}()
	deadline := time.After(500 * time.Millisecond)
	for atomic.LoadInt32(&running) == 0 {
		select {
		case <-deadline:
			t.Fatal("first execution did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := limited.Execute(ctx, json.RawMessage(`{}`))
	// Cancellation while waiting is an infrastructure failure: a Go error, not
	// a model-visible error result.
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context error, got %v", err)
	}

	close(release)
	select {
	case err := <-firstErr:
		t.Fatal(err)
	case res := <-firstDone:
		if got := res.Text(); got != "done" {
			t.Fatalf("first result = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first execution did not finish")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	fs := NewFSTools(FSConfig{})
	r.MustRegister(fs...)
	if err := r.Register(fs[0]); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, ok := r.Get("read_file"); !ok {
		t.Fatal("read_file not found")
	}
	if len(r.Schemas()) != len(fs) {
		t.Fatalf("schemas = %d", len(r.Schemas()))
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(NewBashTool(BashConfig{}))
	if !r.Unregister("bash") {
		t.Fatal("Unregister should report the tool was present")
	}
	if _, ok := r.Get("bash"); ok {
		t.Fatal("bash still present after Unregister")
	}
	if r.Unregister("bash") {
		t.Fatal("second Unregister should report absence")
	}
	// Re-registering after Unregister must work.
	if err := r.Register(NewBashTool(BashConfig{})); err != nil {
		t.Fatal(err)
	}
}

func TestBashEnvSanitizedByDefault(t *testing.T) {
	t.Setenv("GAGE_CANARY_SECRET", "leak-me")
	bash := NewBashTool(BashConfig{})
	res := run(t, bash, `{"command":"env"}`)
	if strings.Contains(res.Text(), "GAGE_CANARY_SECRET") {
		t.Fatalf("canary env var leaked into sanitized environment:\n%s", res.Text())
	}
	if !strings.Contains(res.Text(), "PATH=") {
		t.Fatalf("sanitized environment should keep PATH:\n%s", res.Text())
	}
}

func TestBashEnvExplicit(t *testing.T) {
	t.Setenv("GAGE_CANARY_SECRET", "leak-me")
	bash := NewBashTool(BashConfig{Env: []string{"FOO=bar"}})
	res := run(t, bash, `{"command":"echo \"$FOO\"; env"}`)
	if !strings.HasPrefix(res.Text(), "bar\n") {
		t.Fatalf("explicit env not applied: %q", res.Text())
	}
	// Exactly the configured environment: no parent vars, not even the
	// sanitized defaults.
	if strings.Contains(res.Text(), "GAGE_CANARY_SECRET") || strings.Contains(res.Text(), "HOME=") {
		t.Fatalf("explicit env leaked parent variables:\n%s", res.Text())
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	bash := NewBashTool(BashConfig{})
	start := time.Now()
	// The background grandchild inherits the output pipe; without process-group
	// kill + WaitDelay this hangs until the grandchild exits.
	res := run(t, bash, `{"command":"sleep 30 & sleep 30","timeout_seconds":1}`)
	if !res.IsError || !strings.Contains(res.Text(), "timed out") {
		t.Fatalf("expected timeout, got %q", res.Text())
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("timeout took %s; grandchildren likely not killed", elapsed)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("0123456789"), 0o644)
	read := toolByName(NewFSTools(FSConfig{Root: root}), "read_file")

	res := run(t, read, `{"path":"f.txt","offset":2,"limit":3}`)
	if got := res.Text(); got != "234"+readTruncationMarker {
		t.Fatalf("offset+limit read = %q", got)
	}
	// A window ending exactly at EOF carries no truncation marker.
	res = run(t, read, `{"path":"f.txt","offset":5,"limit":5}`)
	if got := res.Text(); got != "56789" {
		t.Fatalf("tail read = %q", got)
	}
	res = run(t, read, `{"path":"f.txt","offset":-1}`)
	if !res.IsError {
		t.Fatal("negative offset should error")
	}
}

func TestReadFileTruncationMarkerAtCap(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("x", 100)), 0o644)
	read := toolByName(NewFSTools(FSConfig{Root: root, MaxReadBytes: 64}), "read_file")
	res := run(t, read, `{"path":"big.txt"}`)
	if got := res.Text(); got != strings.Repeat("x", 64)+readTruncationMarker {
		t.Fatalf("capped read = %q", got)
	}
	// limit above the cap is clamped to it.
	res = run(t, read, `{"path":"big.txt","limit":1000}`)
	if !strings.HasSuffix(res.Text(), readTruncationMarker) {
		t.Fatalf("expected truncation marker, got %q", res.Text())
	}
}

func TestGrepCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("Hello World\n"), 0o644)
	grep := toolByName(NewSearchTools(FSConfig{Root: root}), "grep")

	res := run(t, grep, `{"pattern":"hello"}`)
	if res.Text() != "no matches" {
		t.Fatalf("case-sensitive grep = %q", res.Text())
	}
	res = run(t, grep, `{"pattern":"hello","case_insensitive":true}`)
	if !strings.Contains(res.Text(), "a.txt:1:Hello World") {
		t.Fatalf("case-insensitive grep = %q", res.Text())
	}
}

func TestGrepContextLines(t *testing.T) {
	root := t.TempDir()
	content := "one\ntwo\nthree\nfour\nfive\nsix\nseven\nMATCH2\nnine\n"
	os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Replace(content, "three", "MATCH1", 1)), 0o644)
	grep := toolByName(NewSearchTools(FSConfig{Root: root}), "grep")

	res := run(t, grep, `{"pattern":"MATCH","context":1}`)
	want := strings.Join([]string{
		"a.txt:2-two",
		"a.txt:3:MATCH1",
		"a.txt:4-four",
		"--",
		"a.txt:7-seven",
		"a.txt:8:MATCH2",
		"a.txt:9-nine",
	}, "\n")
	if res.Text() != want {
		t.Fatalf("grep context = %q, want %q", res.Text(), want)
	}
}

func TestLimitResultSize(t *testing.T) {
	big := ToolFuncMust("big", "big", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", strings.Repeat("a", 100)), nil
	})
	limited := LimitResultSize(big, 10)
	res := run(t, limited, `{}`)
	if got := res.Text(); got != strings.Repeat("a", 10)+"\n...(result truncated)" {
		t.Fatalf("truncated result = %q", got)
	}
	if res.IsError {
		t.Fatal("truncation must not mark the result as an error")
	}

	// Under the cap: untouched.
	small := ToolFuncMust("small", "small", func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	})
	res = run(t, LimitResultSize(small, 10), `{}`)
	if res.Text() != "ok" {
		t.Fatalf("small result = %q", res.Text())
	}
}

func TestLimitResultSizeForwardsCapabilities(t *testing.T) {
	write := toolByName(NewFSTools(FSConfig{Root: t.TempDir()}), "write_file")
	wrapped := LimitResultSize(write, 1<<20)
	meta := gage.MetadataOf(wrapped)
	if !meta.Filesystem || !meta.Destructive {
		t.Fatalf("wrapped metadata = %+v", meta)
	}
	summary := gage.CallSummaryOf(wrapped, json.RawMessage(`{"path":"a.txt","content":"hello"}`))
	if !strings.Contains(summary, "a.txt") {
		t.Fatalf("wrapped summary = %q", summary)
	}
}
