// Package structured turns model output into typed Go values.
//
// Generate streams one completion constrained to the strict JSON Schema
// reflected from T (via tools.SchemaOf), collects the final text, and decodes
// it into T — repairing invalid JSON with follow-up messages when needed.
// Decode and FromResult apply the same tolerant decoding (whitespace,
// markdown code fences) to text you already have, e.g. after an agent run.
//
//	type Sentiment struct {
//		Label string  `json:"label" desc:"Overall sentiment." enum:"positive,negative,neutral"`
//		Score float64 `json:"score" desc:"Confidence in [0,1]."`
//	}
//
//	s, usage, err := structured.Generate[Sentiment](ctx, provider, gage.Request{
//		Messages: []gage.Message{gage.UserText("I love this library!")},
//	})
package structured

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/tools"
)

// maxRepairs bounds the decode-repair round-trips of Generate, on top of the
// initial attempt.
const maxRepairs = 2

// Generate runs one structured generation against p and decodes the model's
// answer into T (a struct type; like tools.Typed, non-struct types panic).
//
// It forces req.Options.ResponseFormat to the strict JSON Schema reflected
// from T via tools.SchemaOf — named after T's type name, lowercased — streams
// the request, and decodes the collected text with Decode. When decoding
// fails it repairs: the assistant reply and a user message quoting the decode
// error (and demanding only valid JSON) are appended to the conversation and
// the request is retried, up to 2 repair attempts total. The returned Usage
// accumulates across every attempt.
//
// Fallback: if the provider rejects ResponseFormat with gage.ErrUnsupported,
// Generate retries once without ResponseFormat, instead appending an
// instruction carrying the JSON Schema to the system prompt. The fallback is
// best-effort — without provider-side enforcement the repair loop is the only
// guard against malformed output.
func Generate[T any](ctx context.Context, p gage.Provider, req gage.Request, opts ...gage.Option) (T, gage.Usage, error) {
	var zero T
	var total gage.Usage

	schema := tools.SchemaOf[T]()
	req.Options = gage.ApplyOptions(req.Options, opts...)
	msgs := append([]gage.Message(nil), req.Messages...)

	useFormat := true
	repairs := 0
	for {
		r := req
		r.Messages = msgs
		if useFormat {
			r.Options.ResponseFormat = &gage.ResponseFormat{
				Type:   gage.ResponseJSONSchema,
				Name:   schemaName[T](),
				Schema: schema,
				Strict: true,
			}
		} else {
			r.Options.ResponseFormat = nil
			r.System = appendSchemaInstruction(req.System, schema)
		}

		text, usage, err := collect(ctx, p, r)
		total = total.Add(usage)
		if err != nil {
			if useFormat && errors.Is(err, gage.ErrUnsupported) {
				// The provider cannot honor ResponseFormat: retry once with
				// the schema spelled out in the system prompt instead.
				useFormat = false
				continue
			}
			return zero, total, err
		}

		v, derr := Decode[T](text)
		if derr == nil {
			return v, total, nil
		}
		if repairs >= maxRepairs {
			return zero, total, fmt.Errorf("structured: output still invalid after %d repair attempts: %w", maxRepairs, derr)
		}
		repairs++
		msgs = append(msgs,
			gage.AssistantText(text),
			gage.UserText(fmt.Sprintf(
				"Your previous reply could not be decoded: %v. Respond again with only valid JSON matching the required schema, with no prose and no code fences.",
				derr)),
		)
	}
}

// Decode decodes model output text into T. It trims surrounding whitespace
// and strips a markdown code fence (```json ... ```), then json.Unmarshals.
func Decode[T any](text string) (T, error) {
	var v T
	s := stripFences(strings.TrimSpace(text))
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return v, fmt.Errorf("structured: decode into %T: %w", v, err)
	}
	return v, nil
}

// FromResult decodes the final text of a completed agent run into T. It is a
// convenience for callers using agent.RunSync followed by Decode.
func FromResult[T any](res *gage.Result) (T, error) {
	if res == nil {
		var zero T
		return zero, errors.New("structured: nil result")
	}
	return Decode[T](res.Text)
}

// schemaName derives the ResponseFormat schema name from T's type name,
// lowercased (pointers dereferenced). Anonymous types fall back to "output".
func schemaName[T any]() string {
	t := reflect.TypeOf((*T)(nil)).Elem()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if name := strings.ToLower(t.Name()); name != "" {
		return name
	}
	return "output"
}

// appendSchemaInstruction appends the ErrUnsupported-fallback instruction to
// the system prompt: the schema itself plus a demand for bare JSON output.
func appendSchemaInstruction(system string, schema gage.JSONSchema) string {
	instr := "You must answer with a single JSON value that validates against this JSON Schema. " +
		"Output only the JSON, with no prose and no code fences.\nJSON Schema:\n" + string(schema)
	if system == "" {
		return instr
	}
	return system + "\n\n" + instr
}

// collect streams one request and gathers the assistant text and usage. A
// terminal error event becomes the returned error; usage seen before the
// failure is still reported so callers can accumulate it.
func collect(ctx context.Context, p gage.Provider, req gage.Request) (string, gage.Usage, error) {
	var usage gage.Usage
	stream, err := p.Stream(ctx, req)
	if err != nil {
		return "", usage, err
	}
	var text strings.Builder
	for ev := range stream {
		switch ev.Type {
		case gage.EventTextDelta:
			text.WriteString(ev.Text)
		case gage.EventUsage:
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case gage.EventError:
			if ev.Err != nil {
				return "", usage, ev.Err
			}
			return "", usage, errors.New(ev.ErrorString)
		}
	}
	if ctx.Err() != nil {
		return "", usage, ctx.Err()
	}
	return text.String(), usage, nil
}

// stripFences removes a surrounding markdown code fence, including an
// optional language tag on the opening fence. Input must already be
// whitespace-trimmed.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:] // drop the opening fence line (and any language tag)
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
