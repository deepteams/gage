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
    `model.go`, `errors.go`.
  - Ports (interfaces): `provider.go` (`Provider`), `tool.go` (`Tool`,
    `ToolRegistry`), `search.go` (`SearchProvider`), `permission.go`
    (`Approver`), `auth.go` (`TokenStore`).
- **Adapters** (sub-packages): depend on the core, never the reverse.
  - `providers/` — `Provider` implementations. `providers/shared` has the HTTP
    client (retry), SSE parser, and OAuth (PKCE, `TokenSource`, stores).
    `providers/openai` holds the reusable Chat Completions (`chat.go`) and
    Responses (`responses.go`) wire formats; openrouter/vllm/ollama and codex
    build on them.
  - `tools/` — built-in tools, `MapRegistry`, and the permission `Guard`.
  - `search/` — `SearchProvider` impls (duckduckgo has no key).
  - `mcp/` — wraps `github.com/modelcontextprotocol/go-sdk`; adapts MCP tools to
    `gage.Tool`.
  - `skills/` — `SKILL.md` loader + the `skill` tool.
  - `agent/` — the loop (`loop.go`), config, sub-agents.
  - `httpx/` — SSE handler.
  - `internal/jsonschema` — tiny JSON Schema builder for tool params.

**Rule:** never import an adapter package from the core. If the core needs a
capability, express it as a port (interface) and let an adapter implement it.

## The event model

`gage.Event` is a single tagged struct (not an interface) so it serializes to
SSE/JSON and routes in a `select`. A provider emits:

```
message_start → (text_delta | reasoning_delta | tool_call_start/delta/end)* → usage → message_done
```

and closes its channel (or emits `error` then closes). The agent relays these
(tagging `Turn`), inserts `tool_result` after executing tools, and ends with
`done`.

## Conventions

- `context.Context` is the first parameter of anything that does I/O; cancelling
  it must stop streams and close channels.
- Wrap errors with `%w`; use the sentinels in `errors.go` (`ErrAuth`,
  `ErrRateLimited`, ...). Provider HTTP failures become `*gage.APIError`, which
  `errors.Is`-matches `ErrAuth`/`ErrRateLimited` by status.
- Tool-level failures are returned as a `ToolResult` with `IsError: true` (so
  the model sees them), **not** as a Go `error`. Reserve the `error` return for
  infrastructure failures.
- Tool metadata is advisory and optional. Use `gage.ToolMetadataProvider` and
  `gage.ToolCallDescriber` to enrich client approval/audit UX, but keep policy
  decisions in caller-provided `Approver`s.
- Agent tool execution is hardened: per-tool timeouts are configured via
  `agent.Config.ToolTimeout`, tool panics are recovered into error results, and
  `agent.Observer` emits structured lifecycle observations for audit/metrics.
- Built-in filesystem and web tools are security-sensitive. Keep root
  confinement symlink-safe, and keep `web_fetch` private-host blocking enabled
  by default unless the caller explicitly opts into trusted local/internal use.
- Streaming goroutines own closing their output channel and always `select` on
  `ctx.Done()` when sending.
- Keep dependencies minimal: stdlib + `modelcontextprotocol/go-sdk` (MCP) +
  `gopkg.in/yaml.v3` (skill frontmatter) and their transitive deps.

## Adding things

- **A new provider:** implement `gage.Provider` in `providers/<name>`. If it
  speaks OpenAI Chat Completions, embed `openai.ChatClient`; if Responses, embed
  `openai.ResponsesClient`; otherwise write a `pump` goroutine that parses the
  stream into `gage.Event`s (see `providers/ollama/native.go` for a non-SSE
  example). Test with an `httptest` server returning a recorded stream and
  assert both the request body mapping and the event sequence.
- **A new tool:** implement `gage.Tool` (or use `tools.Func`). Give it a JSON
  Schema via `internal/jsonschema`. Test on `t.TempDir()`.
- **A new search backend:** implement `gage.SearchProvider` in `search/<name>`.

## Commands

```sh
go build ./...
go vet ./...
go test ./... -race     # all tests; no real network (httptest / in-memory)
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
