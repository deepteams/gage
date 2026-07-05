# CLAUDE.md

Guidance for working in the `gage` repository.

## What this is

`gage` (module `github.com/deepteams/gage`) is a **library** for building
agentic systems, agnostic to model/API/provider. It is imported by other Go
programs. It is **never** an application:

- no `main` package, no `func main`;
- no `http.ListenAndServe` — the `httpx` package returns `http.Handler` values
  only, and the consumer mounts/serves them;
- OAuth login helpers may briefly bind a localhost callback server, but only for
  the interactive login flow, never to serve application traffic.

Everything streams end to end via `<-chan gage.Event`.

## Architecture: hexagonal (ports & adapters)

- **Core** (root package `gage`): domain types + ports. Depends on nothing
  outside the stdlib.
  - Domain: `message.go`, `tool_call.go`, `event.go`, `usage.go`, `options.go`,
    `result.go`, `model.go`, `errors.go`, `estimate.go` (heuristic token
    estimation), `pricing.go` (`Pricing` rates + `Cost`), `session.go`
    (`Checkpoint`, `Session`).
  - Ports (interfaces): `provider.go` (`Provider`, plus the optional
    `ModelLister` and `TokenCounter` capabilities), `tool.go` (`Tool`,
    `ToolRegistry`), `search.go` (`SearchProvider`), `permission.go`
    (`Approver`), `compact.go` (`Compactor`), `memory.go` (`MemoryStore`),
    `embedding.go` (`Embedder`), `auth.go` (`TokenStore`), `session.go`
    (`SessionStore`).
- **Adapters** (sub-packages): depend on the core, never the reverse.
  - `providers/` — `Provider` implementations. `providers/shared` has the HTTP
    client (retry), SSE parser, `Send` helper, and OAuth (PKCE, `TokenSource`,
    stores). `providers/openai` holds the reusable Chat Completions (`chat.go`)
    and Responses (`responses.go`) wire formats; openrouter/vllm/ollama and
    codex build on them. `providers/anthropic` holds the reusable Messages wire
    format and the API-key provider; claudecode builds on it.
    `providers/gemini` speaks the Gemini API natively (streaming, thinking
    signatures, `countTokens`). `providers/fallback` chains several providers:
    it fails over to the next one when a provider errors before producing any
    content. Embedder adapters: `openai.Embeddings` (any OpenAI-compatible
    `/embeddings` endpoint) and `ollama.Embedder`.
  - `tools/` — built-in tools, `Typed[T]` reflected tools, `MapRegistry`, the
    permission `Guard`, and the `LimitConcurrency`/`LimitResultSize` wrappers.
  - `search/` — `SearchProvider` impls (duckduckgo has no key).
  - `mcp/` — wraps `github.com/modelcontextprotocol/go-sdk`; adapts MCP tools to
    `gage.Tool`, plus resources, prompts, `tools/list_changed` sync, and
    sampling backed by a `gage.Provider`.
  - `skills/` — `SKILL.md` loader + the `skill` tool.
  - `memory/` — in-memory `MemoryStore` + `memory_remember`,
    `memory_recall`, and `memory_forget` tools; `NewWithEmbedder` upgrades
    recall to cosine-similarity ranking (keyword fallback on embed failure).
  - `sessions/` — `SessionStore` impls (in-memory, JSON file store).
  - `agent/` — the loop (`loop.go`), config, hooks, compactors, sub-agents,
    pause/resume (`Resume`, `*Paused`).
  - `httpx/` — SSE handler.
  - `jsonschema/` — JSON Schema builder for tool params (public).
  - `structured/` — typed structured output: `Generate[T]` (reflected strict
    JSON schema, repair loop on invalid JSON, plain-prompt fallback on
    `ErrUnsupported`), `Decode[T]`, `FromResult[T]`.
  - `pricing/` — dated USD-per-MTok snapshot table (`Default`) with
    exact/prefix/provider-stripped model lookup; override before billing.
  - `gagetest/` — exported scripted `gage.Provider` so consumers test their
    agents without network (canonical event sequences, error injection,
    request capture).
  - `otel/` — **nested Go module** (`github.com/deepteams/gage/otel`, package
    `otelgage`): `agent.Observer` → OpenTelemetry spans (GenAI semconv). Kept
    out of the root module so the core stays dependency-light; it uses a
    `replace => ../` for in-repo dev and is consumable once the repo is tagged.

**Rule:** never import an adapter package from the core. If the core needs a
capability, express it as a port (interface) and let an adapter implement it.

## The event model

`gage.Event` is a single tagged struct (not an interface) so it serializes to
SSE/JSON and routes in a `select`. A provider emits:

```
message_start → (text_delta | reasoning_delta | reasoning_done | tool_call_start/delta/end)* → usage → message_done
```

and closes its channel (or emits `error` then closes). `reasoning_done` closes
a reasoning block and carries the provider's opaque replay signature; the agent
preserves signed reasoning parts in history and encoders replay them (Anthropic
extended thinking requires this). The agent relays all events (tagging `Turn`),
inserts `tool_result` after executing tools, and ends with `done` carrying a
`gage.Result` (full conversation, final text, stop reason, aggregated usage,
turn count) — or with `paused` carrying a `gage.Checkpoint` when an `Approver`
returned `ErrApprovalPending`; the caller persists the checkpoint (see
`SessionStore`) and continues with `agent.Resume` plus the recorded decisions.

## Conventions

- `context.Context` is the first parameter of anything that does I/O; cancelling
  it must stop streams and close channels.
- Wrap errors with `%w`; use the sentinels in `errors.go` (`ErrAuth`,
  `ErrRateLimited`, ...). Provider HTTP failures become `*gage.APIError`, which
  `errors.Is`-matches `ErrAuth`/`ErrRateLimited` by status.
- Providers never silently drop an explicitly requested option: if a provider
  cannot honor `ResponseFormat`, `ToolChoice`, `StopSequences`, or
  `ReasoningEffort`, `Stream` fails fast with `gage.Unsupported(provider,
  option)` (matching `ErrUnsupported`). The same applies to content the
  provider cannot express (e.g. `PartDocument` on ollama, document URLs on
  Chat Completions). `PromptCache` is the exception — it is a hint, ignored
  where the provider has no explicit cache control.
- Stop reasons are typed (`gage.StopReason`); providers normalize their wire
  values onto the `Stop*` constants and pass unknown values through verbatim.
- Tool-level failures are returned as a `ToolResult` with `IsError: true` (so
  the model sees them), **not** as a Go `error`. Reserve the `error` return for
  infrastructure failures.
- Tool metadata is advisory and optional. Use `gage.ToolMetadataProvider` and
  `gage.ToolCallDescriber` to enrich client approval/audit UX, but keep policy
  decisions in caller-provided `Approver`s.
- Use `gage.RememberingPerInput` or `gage.RememberingBy` for remembered
  approvals of argument-sensitive tools. `gage.Remembering` intentionally
  caches by tool name only.
- Agent tool execution is hardened: per-tool timeouts are configured via
  `agent.Config.ToolTimeout`, tool panics are recovered into error results, and
  `agent.Observer` emits structured lifecycle observations for audit/metrics.
- Agent runs are bounded by `MaxTurns`, `TokenBudget` (fails with
  `ErrBudgetExceeded`; compaction usage counts too), and `MaxToolRepeats`
  (identical consecutive tool calls get an error result, then the run fails
  with `ErrLoopDetected`). `MaxStreamRetries` retries a turn on retryable
  stream failures (never on `ErrAuth`/`ErrUnsupported`/cancellation).
- Compaction triggers both proactively (heuristic `gage.EstimateTokens` before
  the provider call, so an oversized first request never ships) and reactively
  (provider-reported input tokens); a `Compactor` returns the usage it spent.
  `agent.Config.CountTokens` upgrades the proactive check to an exact
  provider-side count when the provider implements `gage.TokenCounter`
  (anthropic, gemini), falling back to the heuristic on error.
- Built-in filesystem and web tools are security-sensitive. Keep root
  confinement symlink-safe, and keep `web_fetch` private-host blocking enabled
  by default unless the caller explicitly opts into trusted local/internal use.
- Streaming goroutines own closing their output channel and always `select` on
  `ctx.Done()` when sending.
- Keep dependencies minimal: stdlib + `modelcontextprotocol/go-sdk` (MCP) +
  `gopkg.in/yaml.v3` (skill frontmatter) and their transitive deps. Anything
  heavier (OpenTelemetry) lives in its own nested module (see `otel/`).

## Adding things

- **A new provider:** implement `gage.Provider` in `providers/<name>`. If it
  speaks OpenAI Chat Completions, embed `openai.ChatClient`; if Responses, embed
  `openai.ResponsesClient`; if Anthropic Messages, build on
  `providers/anthropic.Client`; otherwise write a `pump` goroutine that parses
  the stream into `gage.Event`s (see `providers/ollama/native.go` for a non-SSE
  example). Test with an `httptest` server returning a recorded stream and
  assert both the request body mapping and the event sequence.
- **A new tool:** prefer `tools.Typed[T]` (schema reflected from a struct);
  otherwise implement `gage.Tool` (or use `tools.Func`) with a schema from the
  `jsonschema` package. Test on `t.TempDir()`.
- **A new search backend:** implement `gage.SearchProvider` in `search/<name>`.

## Commands

```sh
go build ./...
go vet ./...
go test ./... -race     # all tests; no real network (httptest / in-memory)
(cd otel && go test ./... -race)   # nested module, not covered by ./...
```

Tests never hit the network. Provider tests use `net/http/httptest`; MCP tests
use the SDK's in-memory transport; OAuth tests mock the token endpoint.

## Gotchas

- Codex/Claude Code call undocumented backend endpoints via the OAuth "plan"
  flow and present themselves as the official CLIs. Endpoints, client ids and
  headers can change; keep them in the provider's `auth.go` constants.
- Claude Code requires the first `system` block to be exactly
  `You are Claude Code, Anthropic's official CLI for Claude.` — see
  `claudecode.SystemSpoof` / `systemBlocks`.
- Token storage is the consumer's responsibility via `TokenStore`; do not
  hard-code file paths in providers.
