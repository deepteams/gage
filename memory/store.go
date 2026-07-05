// Package memory provides a small in-memory gage.MemoryStore implementation
// and agent tools for long-lived memories. It is intended for tests, local
// agents, and as a reference adapter; production apps can implement the same
// gage.MemoryStore port with a database or vector index.
package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

const defaultRecallLimit = 10

// Store is a concurrency-safe in-memory MemoryStore.
type Store struct {
	mu      sync.RWMutex
	nextID  uint64
	records map[string]gage.Memory
}

// New returns an empty in-memory MemoryStore.
func New() *Store {
	return &Store{records: map[string]gage.Memory{}}
}

// Remember stores a memory, assigning an ID and CreatedAt when omitted.
func (s *Store) Remember(ctx context.Context, m gage.Memory) (gage.Memory, error) {
	if err := ctx.Err(); err != nil {
		return gage.Memory{}, err
	}
	if strings.TrimSpace(m.Text) == "" {
		return gage.Memory{}, fmt.Errorf("memory: text is required")
	}
	m.Metadata = cloneMetadata(m.Metadata)
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		s.nextID++
		m.ID = fmt.Sprintf("mem_%d", s.nextID)
	}
	s.records[m.ID] = cloneMemory(m)
	return cloneMemory(m), nil
}

// Recall returns memories matching q, ordered by simple keyword relevance and
// recency. Metadata filters are exact matches.
func (s *Store) Recall(ctx context.Context, q gage.MemoryQuery) ([]gage.Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultRecallLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		m     gage.Memory
		score int
	}
	var found []scored
	for _, m := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !metadataMatches(m.Metadata, q.Metadata) {
			continue
		}
		score := memoryScore(q.Query, m)
		if strings.TrimSpace(q.Query) != "" && score == 0 {
			continue
		}
		found = append(found, scored{m: cloneMemory(m), score: score})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		if !found[i].m.CreatedAt.Equal(found[j].m.CreatedAt) {
			return found[i].m.CreatedAt.After(found[j].m.CreatedAt)
		}
		return found[i].m.ID < found[j].m.ID
	})
	if len(found) > limit {
		found = found[:limit]
	}
	out := make([]gage.Memory, len(found))
	for i, s := range found {
		out[i] = s.m
	}
	return out, nil
}

// Forget removes one memory. Missing IDs are ignored.
func (s *Store) Forget(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("memory: id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
	return nil
}

func memoryScore(query string, m gage.Memory) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 1
	}
	haystack := strings.ToLower(m.Text + " " + metadataString(m.Metadata))
	score := 0
	if strings.Contains(haystack, query) {
		score += 10
	}
	for _, term := range strings.Fields(query) {
		if strings.Contains(haystack, term) {
			score++
		}
	}
	return score
}

func metadataMatches(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func metadataString(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(meta[k])
		b.WriteByte(' ')
	}
	return b.String()
}

func cloneMemory(m gage.Memory) gage.Memory {
	m.Metadata = cloneMetadata(m.Metadata)
	return m
}

func cloneMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
