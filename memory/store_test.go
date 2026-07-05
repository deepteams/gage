package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deepteams/gage"
)

// fakeEmbedder returns fixed vectors per exact text. Unknown texts error.
type fakeEmbedder struct {
	vecs map[string][]float32
	err  error
}

func (f *fakeEmbedder) Name() string { return "fake" }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.vecs[t]
		if !ok {
			return nil, fmt.Errorf("fake: no vector for %q", t)
		}
		out[i] = v
	}
	return out, nil
}

const (
	teaText      = "User's beverage of choice is oolong tea."
	playlistText = "The favorite drink playlist is on Spotify."
	queryText    = "favorite drink"
)

func semanticStore(t *testing.T) (*Store, *fakeEmbedder) {
	t.Helper()
	emb := &fakeEmbedder{vecs: map[string][]float32{
		// The tea memory shares no keywords with the query but is semantically
		// close; the playlist memory contains the query verbatim but is far.
		teaText:      {0.95, 0.05},
		playlistText: {0.0, 1.0},
		queryText:    {1.0, 0.0},
	}}
	store := NewWithEmbedder(emb)
	ctx := context.Background()
	if _, err := store.Remember(ctx, gage.Memory{Text: teaText, CreatedAt: time.Unix(100, 0)}); err != nil {
		t.Fatal(err)
	}
	// Newer, so keyword/recency ordering would put it first.
	if _, err := store.Remember(ctx, gage.Memory{Text: playlistText, CreatedAt: time.Unix(200, 0)}); err != nil {
		t.Fatal(err)
	}
	return store, emb
}

func TestSemanticRecallBeatsKeywordOrder(t *testing.T) {
	store, _ := semanticStore(t)
	ctx := context.Background()

	got, err := store.Recall(ctx, gage.MemoryQuery{Query: queryText})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("recall = %d memories, want 2", len(got))
	}
	if got[0].Text != teaText || got[1].Text != playlistText {
		t.Fatalf("semantic order = [%q, %q], want tea first", got[0].Text, got[1].Text)
	}

	// The keyword-only store cannot surface the tea memory for this query at
	// all: it shares no terms.
	kw := New()
	if _, err := kw.Remember(ctx, gage.Memory{Text: teaText, CreatedAt: time.Unix(100, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := kw.Remember(ctx, gage.Memory{Text: playlistText, CreatedAt: time.Unix(200, 0)}); err != nil {
		t.Fatal(err)
	}
	got, err = kw.Recall(ctx, gage.MemoryQuery{Query: queryText})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != playlistText {
		t.Fatalf("keyword recall = %+v, want only playlist", got)
	}
}

func TestSemanticRecallLimit(t *testing.T) {
	store, _ := semanticStore(t)

	got, err := store.Recall(context.Background(), gage.MemoryQuery{Query: queryText, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != teaText {
		t.Fatalf("recall limit 1 = %+v, want tea only", got)
	}
}

func TestRecallFallsBackToKeywordOnEmbedError(t *testing.T) {
	store, emb := semanticStore(t)
	emb.err = errors.New("embedder down")

	got, err := store.Recall(context.Background(), gage.MemoryQuery{Query: "oolong tea"})
	if err != nil {
		t.Fatalf("recall should degrade to keyword scoring, got error: %v", err)
	}
	if len(got) != 1 || got[0].Text != teaText {
		t.Fatalf("fallback recall = %+v, want tea via keywords", got)
	}
}

func TestEmptyQueryUsesRecencyWithEmbedder(t *testing.T) {
	store, emb := semanticStore(t)
	// The embedder must not be consulted for an empty query.
	emb.err = errors.New("embedder must not be called")

	got, err := store.Recall(context.Background(), gage.MemoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != playlistText || got[1].Text != teaText {
		t.Fatalf("empty-query recall = %+v, want newest first", got)
	}
}

func TestRememberPropagatesEmbedError(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("boom")}
	store := NewWithEmbedder(emb)

	_, err := store.Remember(context.Background(), gage.Memory{Text: "anything"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("remember err = %v, want embed error", err)
	}
}

func TestSemanticRecallMetadataFilter(t *testing.T) {
	emb := &fakeEmbedder{vecs: map[string][]float32{
		"a": {1, 0},
		"b": {0.9, 0.1},
		"q": {1, 0},
	}}
	store := NewWithEmbedder(emb)
	ctx := context.Background()
	if _, err := store.Remember(ctx, gage.Memory{Text: "a", Metadata: map[string]string{"user": "u1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, gage.Memory{Text: "b", Metadata: map[string]string{"user": "u2"}}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Recall(ctx, gage.MemoryQuery{Query: "q", Metadata: map[string]string{"user": "u2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "b" {
		t.Fatalf("filtered recall = %+v, want only b", got)
	}
}

func TestForgetRemovesVector(t *testing.T) {
	store, _ := semanticStore(t)
	ctx := context.Background()

	all, err := store.Recall(ctx, gage.MemoryQuery{Query: queryText})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Forget(ctx, all[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Recall(ctx, gage.MemoryQuery{Query: queryText})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID == all[0].ID {
		t.Fatalf("recall after forget = %+v", got)
	}
	store.mu.RLock()
	_, vectorLeft := store.vectors[all[0].ID]
	store.mu.RUnlock()
	if vectorLeft {
		t.Fatal("vector not removed on Forget")
	}
}
