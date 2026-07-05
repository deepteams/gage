package fallback

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

// scripted is a provider that either fails Stream, or plays a fixed event
// sequence.
type scripted struct {
	name      string
	streamErr error
	events    []gage.Event
	calls     int
}

func (s *scripted) Name() string { return s.name }

func (s *scripted) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	s.calls++
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	ch := make(chan gage.Event)
	go func() {
		defer close(ch)
		for _, e := range s.events {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func good(name, text string) *scripted {
	return &scripted{name: name, events: []gage.Event{
		gage.MessageStart(), gage.TextDelta(text), gage.MessageDone(gage.StopEndTurn),
	}}
}

func collect(t *testing.T, p gage.Provider) []gage.Event {
	t.Helper()
	ch, err := p.Stream(context.Background(), gage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var out []gage.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func textOf(events []gage.Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Type == gage.EventTextDelta {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func hasError(events []gage.Event) bool {
	for _, e := range events {
		if e.Type == gage.EventError {
			return true
		}
	}
	return false
}

func TestFailoverOnStreamError(t *testing.T) {
	p1 := &scripted{name: "a", streamErr: errors.New("dial failed")}
	p2 := good("b", "ok")
	events := collect(t, New(p1, p2))
	if hasError(events) || textOf(events) != "ok" {
		t.Fatalf("events = %+v", events)
	}
	if p1.calls != 1 || p2.calls != 1 {
		t.Fatalf("calls = %d/%d", p1.calls, p2.calls)
	}
}

func TestFailoverOnPreContentErrorEvent(t *testing.T) {
	p1 := &scripted{name: "a", events: []gage.Event{
		gage.MessageStart(), gage.ErrorEvent(&gage.APIError{Provider: "a", Status: 529, Body: "overloaded"}),
	}}
	p2 := good("b", "ok")
	events := collect(t, New(p1, p2))
	if hasError(events) || textOf(events) != "ok" {
		t.Fatalf("events = %+v", events)
	}
	// The failed provider's message_start must not leak through.
	starts := 0
	for _, e := range events {
		if e.Type == gage.EventMessageStart {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("message_start count = %d", starts)
	}
}

func TestNoFailoverAfterContent(t *testing.T) {
	p1 := &scripted{name: "a", events: []gage.Event{
		gage.MessageStart(), gage.TextDelta("partial"), gage.ErrorEvent(errors.New("mid-stream")),
	}}
	p2 := good("b", "ok")
	events := collect(t, New(p1, p2))
	if !hasError(events) {
		t.Fatal("mid-stream error not forwarded")
	}
	if p2.calls != 0 {
		t.Fatal("failed over after content had streamed")
	}
}

func TestAllProvidersFailForwardsLastError(t *testing.T) {
	p1 := &scripted{name: "a", streamErr: errors.New("a down")}
	p2 := &scripted{name: "b", events: []gage.Event{
		gage.ErrorEvent(&gage.APIError{Provider: "b", Status: 500, Body: "b down"}),
	}}
	events := collect(t, New(p1, p2))
	if len(events) != 1 || events[0].Type != gage.EventError {
		t.Fatalf("events = %+v", events)
	}
	// The last provider's error is the one surfaced.
	var apiErr *gage.APIError
	if !errors.As(events[0].Err, &apiErr) || apiErr.Provider != "b" {
		t.Fatalf("terminal error = %v", events[0].Err)
	}
}
