package httpx

import (
	"context"
	"net/http"

	"github.com/deepteams/gage"
)

// Runner is the subset of *agent.Agent needed to stream a run. *agent.Agent
// satisfies it, and it keeps httpx free of an import cycle / hard dependency on
// the agent package.
type Runner interface {
	Run(ctx context.Context, input []gage.Message) (<-chan gage.Event, error)
}

// InputFunc extracts the initial messages for a run from the request. A typical
// implementation decodes a JSON body. If it returns an error, the handler
// responds 400.
type InputFunc func(*http.Request) ([]gage.Message, error)

// StreamHandler returns an http.Handler that runs the agent for each request
// and streams its events as SSE. The response is flushed after every event so
// clients receive tokens as they are produced. The run is bound to the
// request context, so a disconnecting client cancels the agent.
//
// This is a handler only: the caller mounts it on a mux and runs the server.
func StreamHandler(agent Runner, input InputFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		msgs, err := input(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		stream, err := agent.Run(r.Context(), msgs)
		if err != nil {
			// Report the failure as an SSE error frame, then stop.
			_ = WriteSSE(w, gage.ErrorEvent(err))
			flusher.Flush()
			return
		}

		for ev := range stream {
			if err := WriteSSE(w, ev); err != nil {
				return // client went away
			}
			flusher.Flush()
		}
	})
}
