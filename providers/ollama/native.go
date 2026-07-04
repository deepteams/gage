package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/providers/shared"
)

var defaultNativeHTTP = shared.NewClient("gage/ollama")

// nativeProvider streams Ollama's /api/chat NDJSON protocol.
type nativeProvider struct {
	cfg config
}

func (p *nativeProvider) Name() string { return "ollama" }

func (p *nativeProvider) client() *shared.Client {
	if p.cfg.http != nil {
		return p.cfg.http
	}
	return defaultNativeHTTP
}

func (p *nativeProvider) Stream(ctx context.Context, req gage.Request) (<-chan gage.Event, error) {
	body, err := buildNativeBody(req, p.cfg.defaultModel)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: "ollama", Status: resp.StatusCode, Body: string(b)}
	}

	out := make(chan gage.Event)
	go p.pump(ctx, resp, out)
	return out, nil
}

func (p *nativeProvider) pump(ctx context.Context, resp *http.Response, out chan<- gage.Event) {
	defer close(out)
	defer resp.Body.Close()

	send := func(e gage.Event) bool { return shared.Send(ctx, out, e) }
	if !send(gage.MessageStart()) {
		return
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	stopReason := gage.StopEndTurn
	toolIdx := 0
	var usage *gage.Usage

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk nativeChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Message.Content != "" {
			if !send(gage.TextDelta(chunk.Message.Content)) {
				return
			}
		}
		if chunk.Message.Thinking != "" {
			if !send(gage.ReasoningDelta(chunk.Message.Thinking)) {
				return
			}
		}
		// Ollama emits complete tool calls (not streamed fragments).
		for _, tc := range chunk.Message.ToolCalls {
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			call := gage.ToolCall{
				ID:    fmt.Sprintf("call_%d", toolIdx),
				Name:  tc.Function.Name,
				Input: args,
			}
			toolIdx++
			if !send(gage.ToolCallStart(gage.ToolCall{ID: call.ID, Name: call.Name})) {
				return
			}
			if !send(gage.ToolCallDone(call)) {
				return
			}
			stopReason = gage.StopToolUse
		}
		if chunk.Done {
			usage = &gage.Usage{InputTokens: chunk.PromptEvalCount, OutputTokens: chunk.EvalCount}
			if chunk.DoneReason == "length" {
				stopReason = gage.StopMaxTokens
			}
		}
	}
	if err := sc.Err(); err != nil {
		send(gage.ErrorEvent(fmt.Errorf("ollama: stream: %w", err)))
		return
	}
	if usage != nil {
		if !send(gage.UsageEvent(*usage)) {
			return
		}
	}
	send(gage.MessageDone(stopReason))
}

// ---- wire types ----

type nativeChunk struct {
	Message struct {
		Content   string `json:"content"`
		Thinking  string `json:"thinking"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}
