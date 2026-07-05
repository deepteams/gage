# gage-coding

A minimal interactive coding agent — the skeleton of an "opencode-like" CLI —
built entirely on gage. It is an example, not a product: ~400 lines showing how
the library's pieces are meant to fit together.

This directory is its own Go module (with a `replace` on the parent), like
`otel/`, so the gage library itself keeps zero `main` packages.

## Run

```sh
cd examples/gage-coding

# Pick a backend — either an OAuth subscription login (once):
go run . -login codex                  # ChatGPT/Codex plan, no API key

# ...or an API key:
export ANTHROPIC_API_KEY=sk-ant-...    # Anthropic Messages API
export OPENROUTER_API_KEY=sk-or-...    # any model on OpenRouter
#   ...or neither: falls back to a local ollama daemon (OLLAMA_HOST to override)

go run . [-root DIR] [-model ID] [-session NAME] [-yolo]
```

Provider precedence: stored codex tokens → `ANTHROPIC_API_KEY` →
`OPENROUTER_API_KEY` → local ollama.

- `-root` — workspace the agent may read and modify (default `.`). All
  filesystem tools are confined to it, symlink-safe.
- `-model` — model id; defaults per provider (`claude-sonnet-4-5`,
  `anthropic/claude-sonnet-4.5`, `qwen3:8b`).
- `-session` — persist the conversation under `<root>/.gage-coding/` and
  resume it on the next run.
- `-yolo` — skip approval prompts. Only for tasks you fully trust.
- `-login codex` — run the OAuth (PKCE) login flow for a ChatGPT plan and
  exit. Tokens are stored under your user config dir
  (`~/Library/Application Support/gage-coding/codex.json` on macOS,
  `~/.config/gage-coding/codex.json` on Linux) and refreshed transparently.

Then just talk to it:

```
> add a --verbose flag to cmd/serve and thread it into the logger
```

Tool calls stream as they happen; writes, shell commands and network access
prompt for approval (`y` once, `a` always for that exact call, `n` deny).

## What each file demonstrates

| File | gage concepts |
|---|---|
| `main.go` | `agent.New` + `agent.Config` guardrails (`MaxTurns`, `MaxToolRepeats`, `MaxStreamRetries`, `ToolTimeout`, `MaxParallelTools`), compaction (`agent.Summarize` + `CompactAfter` + `CountTokens`), provider selection behind the `gage.Provider` port, tool registry assembly (`tools.NewFSTools`/`NewSearchTools`/`NewBashTool`/`NewWebTools`, `LimitResultSizeAll`), session persistence (`sessions.NewFileStore`, `gage.Session`), cost estimation (`pricing.Cost`) |
| `approver.go` | a custom `gage.Approver`: auto-allow read-only local tools via `ToolMetadata`, prompt for the rest, remembered decisions with `gage.RememberingPerInput` |
| `codex.go` | the OAuth way of connecting: `codex.Login` (PKCE, localhost callback), a file-backed `gage.TokenStore` (`oauth.NewFileStore`), and `codex.New` with transparent token refresh |
| `render.go` | consuming the `gage.Event` stream: text/reasoning deltas, tool calls and results, resetting partial output on `message_start` after a mid-stream retry |

## Security posture (deliberate defaults)

- Filesystem tools are confined to `-root`; paths cannot escape it, even via
  symlinks.
- `bash` runs with a minimal sanitized environment (`BashConfig.Env` nil), so
  API keys in the CLI's environment never leak into model-driven commands.
  This is **not** an OS sandbox — for untrusted input, set
  `BashConfig.RequireSandbox` with a real `BashSandbox`.
- `web_fetch` keeps private-host blocking on: the model cannot reach
  localhost or link-local addresses.
- Every tool result is capped (`LimitResultSizeAll`) so one huge output cannot
  blow up the context window.
- "Always" approvals are cached per tool **and exact input**
  (`RememberingPerInput`), never per tool name alone.

## Going further

Things a real opencode-style CLI would add, and where gage already helps:

- **More OAuth subscription providers** — `codex.go` shows the pattern for a
  ChatGPT plan; `providers/claudecode` works the same way for Claude
  subscriptions (`claudecode.Login` + a `gage.TokenStore`). Note these
  providers call undocumented backend endpoints that may change; see the
  package docs. For headless environments, `oauth.ManualLogin` replaces the
  localhost callback with a paste-the-code flow.
- **Out-of-band approvals** — return `gage.ErrApprovalPending` from the
  Approver, persist the `gage.Checkpoint` from the `paused` event, resume with
  `agent.Resume` (see the `workflow` package for a durable wrapper).
- **MCP servers** — `mcp.New` adapts any MCP server's tools into the registry.
- **Skills** — load `SKILL.md` folders with `skills.Load` and register
  `skills.NewTool`.
- **Sub-agents** — see `agent/subagent.go` for delegating scoped tasks.
- **Observability** — plug `otelgage` (nested module `otel/`) into
  `agent.Config.Observer` for OpenTelemetry spans.
- **Testing your agent** — script a fake provider with `gagetest` and assert
  on the event stream; no network needed.
