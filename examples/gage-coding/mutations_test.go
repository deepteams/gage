package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotListAndRefusesStaleUndo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s := newSnapshotManager(root)
	s.Begin("change file")
	if err := s.RecordBefore("file.txt"); err != nil {
		t.Fatalf("RecordBefore: %v", err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatalf("write after: %v", err)
	}
	if err := s.RecordAfter("file.txt"); err != nil {
		t.Fatalf("RecordAfter: %v", err)
	}
	if got := s.Commit(); got != 1 {
		t.Fatalf("Commit() = %d, want 1", got)
	}

	if got := s.List(); !strings.Contains(got, "change file") || !strings.Contains(got, "file.txt") {
		t.Fatalf("List() missing change details:\n%s", got)
	}
	if err := os.WriteFile(path, []byte("manual edit"), 0o644); err != nil {
		t.Fatalf("manual edit: %v", err)
	}
	if _, err := s.Undo(); err == nil || !strings.Contains(err.Error(), "changed since it was recorded") {
		t.Fatalf("Undo() error = %v, want stale-file refusal", err)
	}
}

func TestSnapshotUndoRedoReportsFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s := newSnapshotManager(root)
	s.Begin("change file")
	if err := s.RecordBefore("file.txt"); err != nil {
		t.Fatalf("RecordBefore: %v", err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatalf("write after: %v", err)
	}
	if err := s.RecordAfter("file.txt"); err != nil {
		t.Fatalf("RecordAfter: %v", err)
	}
	s.Commit()

	out, err := s.Undo()
	if err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if !strings.Contains(out, "files: file.txt") {
		t.Fatalf("Undo() output missing file list:\n%s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after undo: %v", err)
	}
	if string(data) != "before" {
		t.Fatalf("after undo = %q, want before", data)
	}

	out, err = s.Redo()
	if err != nil {
		t.Fatalf("Redo() error = %v", err)
	}
	if !strings.Contains(out, "files: file.txt") {
		t.Fatalf("Redo() output missing file list:\n%s", out)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after redo: %v", err)
	}
	if string(data) != "after" {
		t.Fatalf("after redo = %q, want after", data)
	}
}
