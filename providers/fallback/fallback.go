// Package fallback provides a gage.Provider that tries a sequence of
// providers in order, failing over to the next one when a provider errors
// before producing any content. Combined with the agent's per-turn stream
// retry, it gives multi-provider availability without touching the loop.
package fallback

import (
	"context"
	"errors"
	"fmt"

	"github.com/deepteams/gage"
)

// New returns a Provider that streams from the first provider in providers
// that produces content. A provider is skipped when its Stream call fails or
// its stream ends with a terminal error before any content event (text,
// reasoning, or tool-call activity). Once content has flowed, the stream is
// committed: later errors are forwarded, not failed over, because the
// consumer has already seen partial output.
func New(providers ...gage.Provider) gage.Provider {
	if len(providers) == 0 {
		panic("fallback.New: at least one provider required")
	}
	return &provider{providers: providers}
}

type provider struct {
	providers []gage.Provider
}

func (p *provider) Name() string { return "fallback" }

func (p *provider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	out := make(chan gage.Event)
	go func() {
		defer close(out)
		var lastErr gage.Event
		haveErr := false
		for _, inner := range p.providers {
			if ctx.Err() != nil {
				return
			}
			committed, errEv, ok := p.tryOne(ctx, inner, req, out)
			if committed || !ok {
				// Content flowed (stream fully relayed) or ctx died mid-relay.
				return
			}
			lastErr, haveErr = errEv, true
		}
		// Every provider failed before content: forward the last error.
		if !haveErr {
			lastErr = gage.ErrorEvent(errors.New("fallback: all providers failed"))
		}
		select {
		case out <- lastErr:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// tryOne streams from inner, buffering events until the first content event.
// Before content: a terminal error means "try the next provider" (committed
// false, the error event returned). After content: everything, including
// errors, is relayed verbatim (committed true). ok is false when ctx was
// cancelled while relaying.
func (p *provider) tryOne(ctx context.Context, inner gage.Provider, req gage.Request, out chan<- gage.Event) (committed bool, errEv gage.Event, ok bool) {
	stream, err := inner.Stream(ctx, req)
	if err != nil {
		return false, gage.ErrorEvent(fmt.Errorf("%s: %w", inner.Name(), err)), true
	}
	send := func(e gage.Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var buffered []gage.Event
	for ev := range stream {
		if !committed {
			if ev.Type == gage.EventError {
				// Failed before content: drain and report for failover.
				for range stream {
				}
				return false, ev, true
			}
			if !isContent(ev.Type) {
				buffered = append(buffered, ev)
				continue
			}
			committed = true
			for _, b := range buffered {
				if !send(b) {
					return true, gage.Event{}, false
				}
			}
		}
		if !send(ev) {
			return true, gage.Event{}, false
		}
	}
	if !committed {
		// Stream closed without content or error (e.g. cancelled upstream):
		// treat as a failure of this provider.
		return false, gage.ErrorEvent(fmt.Errorf("%s: stream ended without content", inner.Name())), ctx.Err() == nil
	}
	return true, gage.Event{}, true
}

// isContent reports whether the event carries model output the consumer has
// observed, after which failing over would duplicate output.
func isContent(t gage.EventType) bool {
	switch t {
	case gage.EventTextDelta, gage.EventReasoningDelta, gage.EventReasoningDone,
		gage.EventToolCallStart, gage.EventToolCallDelta, gage.EventToolCallDone,
		gage.EventMessageDone:
		return true
	}
	return false
}
