// Package memory provides a small in-memory gage.MemoryStore implementation
// and agent tools for long-lived memories. It is intended for tests, local
// agents, and as a reference adapter; production apps can implement the same
// gage.MemoryStore port with a database or vector index.
package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepteams/gage"
)

const defaultRecallLimit = 10

// Store is a concurrency-safe in-memory MemoryStore. Constructed with
// NewWithEmbedder, it embeds every remembered memory and ranks non-empty
// Recall queries by cosine similarity; otherwise it uses keyword scoring.
type Store struct {
	embedder gage.Embedder // optional; fixed at construction

	mu      sync.RWMutex
	nextID  uint64
	records map[string]gage.Memory
	vectors map[string][]float32 // keyed by memory ID; internal only
}

// New returns an empty in-memory MemoryStore using keyword recall.
func New() *Store {
	return &Store{
		records: map[string]gage.Memory{},
		vectors: map[string][]float32{},
	}
}

// NewWithEmbedder returns an empty in-memory MemoryStore that embeds every
// remembered memory with e and ranks non-empty Recall queries by cosine
// similarity. If embedding the query fails at recall time, the store degrades
// to keyword scoring instead of failing.
func NewWithEmbedder(e gage.Embedder) *Store {
	s := New()
	s.embedder = e
	return s
}

// Remember stores a memory, assigning an ID and CreatedAt when omitted. When
// the store has an embedder, the memory text is embedded and the vector kept
// alongside the record; embedding failures are returned to the caller.
func (s *Store) Remember(ctx context.Context, m gage.Memory) (gage.Memory, error) {
	if err := ctx.Err(); err != nil {
		return gage.Memory{}, err
	}
	if strings.TrimSpace(m.Text) == "" {
		return gage.Memory{}, fmt.Errorf("memory: text is required")
	}
	if m.Confidence < 0 || m.Confidence > 1 {
		return gage.Memory{}, fmt.Errorf("memory: confidence must be between 0 and 1")
	}
	// Embed outside the lock: Embed does I/O.
	var vec []float32
	if s.embedder != nil {
		vecs, err := s.embedder.Embed(ctx, []string{m.Text})
		if err != nil {
			return gage.Memory{}, fmt.Errorf("memory: embed: %w", err)
		}
		if len(vecs) != 1 {
			return gage.Memory{}, fmt.Errorf("memory: embed: got %d vectors for 1 input", len(vecs))
		}
		vec = vecs[0]
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
	if vec != nil {
		s.vectors[m.ID] = vec
	} else {
		delete(s.vectors, m.ID)
	}
	return cloneMemory(m), nil
}

// Recall returns memories matching q. With an embedder and a non-empty query,
// results are ranked by cosine similarity to the embedded query; otherwise by
// simple keyword relevance and recency. Metadata filters are exact matches.
func (s *Store) Recall(ctx context.Context, q gage.MemoryQuery) ([]gage.Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultRecallLimit
	}

	if s.embedder != nil && strings.TrimSpace(q.Query) != "" {
		vecs, err := s.embedder.Embed(ctx, []string{q.Query})
		if err == nil && len(vecs) == 1 {
			return s.recallSemantic(ctx, q, vecs[0], limit)
		}
		// Embedding the query failed: degrade to keyword scoring rather than
		// failing the recall.
	}
	return s.recallKeyword(ctx, q, limit)
}

// recallSemantic ranks metadata-matching memories by cosine similarity to
// qvec. Records without a stored vector (not expected: the embedder is fixed
// at construction) rank last.
func (s *Store) recallSemantic(ctx context.Context, q gage.MemoryQuery, qvec []float32, limit int) ([]gage.Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()

	type scored struct {
		m     gage.Memory
		score float64
	}
	var found []scored
	for id, m := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !memoryMatches(m, q, now) {
			continue
		}
		score := math.Inf(-1)
		if vec, ok := s.vectors[id]; ok {
			score = cosineSimilarity(qvec, vec)
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

// recallKeyword ranks memories by simple keyword relevance and recency.
func (s *Store) recallKeyword(ctx context.Context, q gage.MemoryQuery, limit int) ([]gage.Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()

	type scored struct {
		m     gage.Memory
		score int
	}
	var found []scored
	for _, m := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !memoryMatches(m, q, now) {
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
	delete(s.vectors, id)
	return nil
}

// cosineSimilarity returns the cosine of the angle between a and b, computed
// over their common prefix. Zero vectors score 0.
func cosineSimilarity(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func memoryScore(query string, m gage.Memory) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 1
	}
	haystack := strings.ToLower(strings.Join([]string{
		m.Text,
		m.Namespace,
		m.UserID,
		m.Provenance,
		m.Sensitivity,
		metadataString(m.Metadata),
	}, " "))
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

func memoryMatches(m gage.Memory, q gage.MemoryQuery, now time.Time) bool {
	if q.Namespace != "" && m.Namespace != q.Namespace {
		return false
	}
	if q.UserID != "" && m.UserID != q.UserID {
		return false
	}
	if !q.IncludeExpired && !m.ExpiresAt.IsZero() && !m.ExpiresAt.After(now) {
		return false
	}
	return metadataMatches(m.Metadata, q.Metadata)
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
