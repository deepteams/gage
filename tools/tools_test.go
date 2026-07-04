package tools

import (
	"context"
	"encoding/json"
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
	deny := gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Decision, error) {
		return gage.Deny, nil
	})
	guarded := Guard(write, deny, "agent")
	res := run(t, guarded, `{"path":"x.txt","content":"data"}`)
	if !res.IsError || !strings.Contains(res.Text(), "permission denied") {
		t.Fatalf("expected denial, got %q", res.Text())
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("file should not have been written")
	}
}

func TestToolMetadataAndSummaryPropagateThroughWrappers(t *testing.T) {
	write := toolByName(NewFSTools(FSConfig{Root: t.TempDir()}), "write_file")
	guarded := Guard(write, gage.ApproverFunc(func(ctx context.Context, req gage.PermissionRequest) (gage.Decision, error) {
		if !req.Metadata.Filesystem || !req.Metadata.RequiresApproval {
			t.Fatalf("permission metadata = %+v", req.Metadata)
		}
		if !strings.Contains(req.Summary, "a.txt") {
			t.Fatalf("permission summary = %q", req.Summary)
		}
		return gage.Deny, nil
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
	res, err := limited.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "concurrency wait cancelled") {
		t.Fatalf("expected concurrency cancellation, got %+v", res)
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
