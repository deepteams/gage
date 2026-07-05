package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

func sampleSession() gage.Session {
	return gage.Session{
		Messages: []gage.Message{
			gage.UserText("hello"),
			gage.AssistantText("hi"),
		},
		Checkpoint: &gage.Checkpoint{
			Messages:   []gage.Message{gage.UserText("hello")},
			Turn:       1,
			Usage:      gage.Usage{InputTokens: 10, OutputTokens: 2},
			StopReason: gage.StopToolUse,
			Calls:      []gage.ToolCall{{ID: "c1", Name: "bash", Input: []byte(`{"command":"ls"}`)}},
			Results:    []gage.ToolResult{gage.TextResult("c0", "ok")},
		},
	}
}

func testStore(t *testing.T, store gage.SessionStore) {
	t.Helper()
	ctx := context.Background()

	if _, err := store.LoadSession(ctx, "missing"); !errors.Is(err, gage.ErrSessionNotFound) {
		t.Fatalf("load missing: err = %v, want ErrSessionNotFound", err)
	}
	if err := store.SaveSession(ctx, "", gage.Session{}); err == nil {
		t.Fatal("empty id accepted")
	}

	want := sampleSession()
	if err := store.SaveSession(ctx, "s1", want); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, "s2", gage.Session{Messages: []gage.Message{gage.UserText("other")}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	ids, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	if err := store.DeleteSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("double delete: %v", err)
	}
	if _, err := store.LoadSession(ctx, "s1"); !errors.Is(err, gage.ErrSessionNotFound) {
		t.Fatalf("load deleted: err = %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	testStore(t, Memory())
}

func TestFileStore(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testStore(t, store)
}

func TestEncryptedFileStore(t *testing.T) {
	dir := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	store, err := NewEncryptedFileStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	want := gage.Session{Messages: []gage.Message{gage.UserText("super secret session")}}
	if err := store.SaveSession(context.Background(), "s1", want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super secret session") {
		t.Fatalf("encrypted session file contains plaintext: %s", raw)
	}
	got, err := store.LoadSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestFileStoreRejectsUnsafeIDs(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"../escape", "a/b", "a\\b", ".", "..", "with space"} {
		if err := store.SaveSession(ctx, id, gage.Session{}); err == nil {
			t.Fatalf("id %q accepted", id)
		}
		if _, err := store.LoadSession(ctx, id); err == nil || errors.Is(err, gage.ErrSessionNotFound) {
			t.Fatalf("load %q: err = %v, want invalid-id error", id, err)
		}
	}
}
