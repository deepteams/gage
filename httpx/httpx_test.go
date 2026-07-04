package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

// stubRunner emits a fixed set of events.
type stubRunner struct{ events []gage.Event }

func (s stubRunner) Run(ctx context.Context, input []gage.Message) (<-chan gage.Event, error) {
	ch := make(chan gage.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestStreamHandlerFraming(t *testing.T) {
	runner := stubRunner{events: []gage.Event{
		gage.MessageStart(),
		gage.TextDelta("hi"),
		gage.MessageDone("end_turn"),
		gage.DoneEvent(),
	}}
	h := StreamHandler(runner, func(r *http.Request) ([]gage.Message, error) {
		return []gage.Message{gage.UserText("x")}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/agent", nil)
	h.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: text_delta") || !strings.Contains(body, `data: {`) {
		t.Fatalf("body missing frames:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("body missing done frame:\n%s", body)
	}
	// Each frame ends with a blank line.
	if !strings.Contains(body, "\n\n") {
		t.Fatal("frames not blank-line separated")
	}
}

func TestStreamHandlerBadInput(t *testing.T) {
	h := StreamHandler(stubRunner{}, func(r *http.Request) ([]gage.Message, error) {
		return nil, http.ErrBodyNotAllowed
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}
