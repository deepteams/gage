package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/deepteams/gage"
)

const fileExt = ".json"

// NewFileStore returns a SessionStore that writes one JSON file per session
// under dir (created with 0o700 if missing). Files are written atomically
// (temp file + rename) with 0o600 permissions. Session ids are restricted to
// [A-Za-z0-9._-] so they map safely onto file names.
func NewFileStore(dir string) (gage.SessionStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sessions: create dir: %w", err)
	}
	return &fileStore{dir: dir}, nil
}

type fileStore struct {
	dir string
	mu  sync.Mutex
}

func validID(id string) error {
	if id == "" {
		return fmt.Errorf("sessions: empty session id")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("sessions: invalid session id %q: only [A-Za-z0-9._-] allowed", id)
		}
	}
	// "." / ".." would escape or collide with directory entries.
	if strings.Trim(id, ".") == "" {
		return fmt.Errorf("sessions: invalid session id %q", id)
	}
	return nil
}

func (f *fileStore) path(id string) string { return filepath.Join(f.dir, id+fileExt) }

func (f *fileStore) SaveSession(_ context.Context, id string, s gage.Session) error {
	if err := validID(id); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("sessions: encode: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tmp, err := os.CreateTemp(f.dir, id+".tmp-*")
	if err != nil {
		return fmt.Errorf("sessions: write: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sessions: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sessions: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("sessions: write: %w", err)
	}
	if err := os.Rename(tmpName, f.path(id)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("sessions: write: %w", err)
	}
	return nil
}

func (f *fileStore) LoadSession(_ context.Context, id string) (gage.Session, error) {
	if err := validID(id); err != nil {
		return gage.Session{}, err
	}
	data, err := os.ReadFile(f.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return gage.Session{}, fmt.Errorf("sessions: %q: %w", id, gage.ErrSessionNotFound)
	}
	if err != nil {
		return gage.Session{}, fmt.Errorf("sessions: read: %w", err)
	}
	var s gage.Session
	if err := json.Unmarshal(data, &s); err != nil {
		return gage.Session{}, fmt.Errorf("sessions: decode %q: %w", id, err)
	}
	return s, nil
}

func (f *fileStore) DeleteSession(_ context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	err := os.Remove(f.path(id))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sessions: delete: %w", err)
	}
	return nil
}

func (f *fileStore) ListSessions(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, fileExt) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, fileExt))
	}
	return ids, nil
}
