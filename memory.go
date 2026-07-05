package gage

import (
	"context"
	"time"
)

// Memory is one durable fact, preference, decision, or note an agent may use
// across runs. Stores may attach their own IDs when the caller leaves ID empty.
type Memory struct {
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	UserID      string            `json:"user_id,omitempty"`
	Provenance  string            `json:"provenance,omitempty"`
	Sensitivity string            `json:"sensitivity,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
	ExpiresAt   time.Time         `json:"expires_at,omitempty"`
}

// MemoryQuery describes a recall request. Query is interpreted by the concrete
// store (keyword search, vector search, SQL full text, ...). Metadata entries,
// when set, are exact-match filters. Limit <= 0 lets the store choose a default.
type MemoryQuery struct {
	Query          string            `json:"query,omitempty"`
	Limit          int               `json:"limit,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	UserID         string            `json:"user_id,omitempty"`
	IncludeExpired bool              `json:"include_expired,omitempty"`
}

// MemoryStore persists and retrieves long-lived agent memories. The memory
// package provides a small in-memory implementation and tools; production apps
// can back this port with a database, vector index, or user-profile service.
type MemoryStore interface {
	// Remember saves m and returns the stored record, including generated ID and
	// CreatedAt values when the store owns them.
	Remember(ctx context.Context, m Memory) (Memory, error)
	// Recall returns memories relevant to q, newest/relevant first.
	Recall(ctx context.Context, q MemoryQuery) ([]Memory, error)
	// Forget removes one memory. Deleting a missing memory is a no-op.
	Forget(ctx context.Context, id string) error
}
