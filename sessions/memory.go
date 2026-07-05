// Package sessions provides gage.SessionStore implementations: an in-memory
// store for tests and single-process use, and a JSON file store for simple
// durable persistence. Anything beyond that (databases, TTLs, encryption) is
// the consumer's concern behind the same port.
package sessions

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/deepteams/gage"
)

// Memory returns an in-memory SessionStore. It is safe for concurrent use and
// loses everything when the process exits.
func Memory() gage.SessionStore {
	return &memoryStore{sessions: map[string]gage.Session{}}
}

type memoryStore struct {
	mu       sync.RWMutex
	sessions map[string]gage.Session
}

func (m *memoryStore) SaveSession(_ context.Context, id string, s gage.Session) error {
	if id == "" {
		return fmt.Errorf("sessions: empty session id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[id] = s
	return nil
}

func (m *memoryStore) LoadSession(_ context.Context, id string) (gage.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return gage.Session{}, fmt.Errorf("sessions: %q: %w", id, gage.ErrSessionNotFound)
	}
	return s, nil
}

func (m *memoryStore) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

func (m *memoryStore) ListSessions(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
