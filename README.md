# gage

**gage** is a provider-agnostic Go toolkit for building agentic systems. It is a
**library** — you import it into your own program; it never starts a server or
owns a `main`. Everything streams end to end.

It gives you, behind clean interfaces:

- **LLM providers**: OpenRouter, vLLM, Ollama, **Codex** (ChatGPT/Codex plan via
  OAuth) and **Claude Code** (Claude plan via OAuth).
- **Built-in tools**: `read_file`, `write_file`, `edit`, `bash`, `grep`, `glob`,
  `list_dir`, `web_fetch`, `web_search`.
- **MCP**: connect to Model Context Protocol servers (stdio or streamable HTTP,
  with header/bearer auth) and expose their tools to the agent.
- **Skills**: load Claude Code–style `SKILL.md` folders.
- **Agents**: an agentic loop with tool execution, permissions, sub-agents, and
  event streaming.
- **Production guards**: per-tool timeouts, panic recovery, structured
  observations, concurrency wrappers, and safe defaults for network/filesystem
  tools.
- **HTTP**: an SSE `http.Handler` to expose an agent (you mount it).

## Architecture

gage is built as **hexagonal ports & adapters**. The root package `gage` holds
the domain types and the ports (interfaces); everything else is an adapter that
depends on the core, never the reverse.

```
gage/                 core: Message, Event, Usage + ports (Provider, Tool, ...)
├── providers/        Provider adapters (openrouter, vllm, ollama, codex, claudecode)
│   ├── shared/       HTTP client, SSE parser, OAuth (PKCE, token source, stores)
│   └── openai/       reusable Chat Completions + Responses wire formats
├── tools/            built-in tools + registry + permission decorator
├── search/           SearchProvider impls (duckduckgo, brave, tavily)
├── mcp/              MCP client → gage.Tool bridge
├── skills/           SKILL.md loader + the "skill" tool
├── agent/            the agentic loop, sub-agents
└── httpx/            SSE handler (no server)
```

The two central contracts:

```go
// A model backend.
type Provider interface {
    Stream(ctx context.Context, req Request) (<-chan Event, error)
    Name() string
}

// An executable capability the model can call.
type Tool interface {
    Name() string
    Description() string
    Schema() JSONSchema
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}
```

A `Provider` streams a channel of unified `Event`s (`text_delta`,
`reasoning_delta`, `tool_call_*`, `usage`, `message_done`, ...). The agent relays
those events, runs the requested tools, feeds results back, and repeats until a
final answer — emitting a terminal `done` event.

## Install

```sh
go get github.com/deepteams/gage
```

Requires Go 1.26+.

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/deepteams/gage"
    "github.com/deepteams/gage/agent"
    "github.com/deepteams/gage/tools"
    "github.com/deepteams/gage/providers/openrouter"
)

func main() {
    // 1. A provider.
    provider := openrouter.New("sk-or-...", openrouter.WithDefaultModel("anthropic/claude-sonnet-4.5"))

    // 2. A tool registry with built-in tools confined to a working dir.
    reg := tools.NewRegistry()
    reg.MustRegister(tools.NewFSTools(tools.FSConfig{Root: "."})...)
    reg.MustRegister(tools.NewSearchTools(tools.FSConfig{Root: "."})...)
    reg.MustRegister(tools.NewBashTool(tools.BashConfig{Dir: "."}))

    // 3. An agent.
    ag, _ := agent.New(agent.Config{
        Provider: provider,
        Registry: reg,
        System:   "You are a coding assistant. Use tools to inspect the repo.",
    })

    // 4. Stream the run.
    stream, _ := ag.Run(context.Background(), []gage.Message{
        gage.UserText("List the Go files and summarize the project."),
    })
    for ev := range stream {
        switch ev.Type {
        case gage.EventTextDelta:
            fmt.Print(ev.Text)
        case gage.EventToolResult:
            fmt.Printf("\n[tool %s]\n", ev.ToolResult.Text())
        }
    }
}
```

## Providers

| Provider | Constructor | Auth |
|---|---|---|
| OpenRouter | `openrouter.New(apiKey, ...)` | API key |
| vLLM | `vllm.New(baseURL, ...)` | optional key |
| Ollama | `ollama.New(baseURL, ...)` | none (local) |
| Codex | `codex.New(store, ...)` | OAuth (ChatGPT/Codex plan) |
| Claude Code | `claudecode.New(store, console, ...)` | OAuth (Claude plan) |

All implement `gage.Provider`, so they are interchangeable in `agent.Config`.

### OAuth providers (Codex, Claude Code)

> ⚠️ **Heads-up.** Codex and Claude Code here authenticate against undocumented
> backend endpoints using the OAuth "plan" flow, presenting themselves as the
> official CLIs. These endpoints are not a public API, can change without
> notice, and their use is subject to the respective providers' terms. Use at
> your own risk.

You supply a `gage.TokenStore` — **you** own how tokens are persisted. gage
provides an optional file/memory store in `providers/shared/oauth`, but any
implementation works (database, keychain, secret manager):

```go
type TokenStore interface {
    Load(ctx context.Context) (Credentials, error)
    Save(ctx context.Context, c Credentials) error
}
```

Log in once to populate the store, then construct the provider:

```go
store := oauth.NewFileStore("/secure/path/codex.json") // or your own TokenStore

// One-time login (opens a browser to the auth URL).
_, err := codex.Login(ctx, store, func(url string) { browser.Open(url) })

// Use it — tokens refresh transparently through the store.
provider := codex.New(store)
```

Claude Code uses a copy-paste redirect flow:

```go
authURL, complete, _ := claudecode.Login(false)
fmt.Println("Visit:", authURL)
// user pastes the returned "code#state"
creds, _ := complete(ctx, store, pasted)
provider := claudecode.New(store, false)
```

## Tools & permissions

Register the built-ins you want, or add your own with `tools.Func`. Gate every
execution behind an approver:

```go
approver := gage.ApproverFunc(func(ctx context.Context, r gage.PermissionRequest) (gage.Decision, error) {
    if r.Metadata.ReadOnly {
        return gage.Allow, nil
    }
    // Your app decides how to ask the user or apply policy. r includes:
    // Tool, Input, Agent, RunID, Turn, Metadata, and Summary.
    return askUserForApproval(ctx, r.Summary)
})
ag, _ := agent.New(agent.Config{Provider: p, Registry: reg, Approver: approver})
```

Built-in tools expose advisory `ToolMetadata` (`ReadOnly`, `Filesystem`,
`Network`, `Shell`, `Destructive`, `RequiresApproval`, `Tags`) and concise call
summaries. Custom tools can implement `gage.ToolMetadataProvider` /
`gage.ToolCallDescriber`, or use `tools.FuncWithMetadata`.

For production use, add a per-tool timeout and an observer for audit logs,
metrics, or traces:

```go
observer := agent.ObserverFunc(func(ctx context.Context, o agent.Observation) {
    log.Printf("run=%s type=%s tool=%s error=%v duration=%s",
        o.RunID, o.Type, o.Tool, o.IsError, o.Duration)
})

ag, _ := agent.New(agent.Config{
    Provider:    p,
    Registry:    reg,
    Approver:    approver,
    ToolTimeout: 30 * time.Second,
    Observer:    observer,
})
```

Tool panics are recovered and returned to the model as failed tool results.
`tools.LimitConcurrency` / `tools.LimitConcurrencyAll` can cap concurrent
executions of expensive tools:

```go
reg.MustRegister(tools.LimitConcurrency(tools.NewBashTool(tools.BashConfig{Dir: "."}), 2))
```

`web_search` needs a `SearchProvider`. DuckDuckGo needs no key:

```go
reg.MustRegister(tools.NewWebTools(tools.WebConfig{Search: duckduckgo.New()})...)
```

`brave.New(key)` and `tavily.New(key)` are drop-in alternatives.

`web_fetch` blocks localhost, private, link-local, multicast and unspecified
addresses by default, including redirects. For trusted local/internal use only,
set `tools.WebConfig{AllowPrivateHosts: true}`.

## MCP

```go
client, _ := mcp.ConnectStdio(ctx, mcp.StdioConfig{
    Name: "fs", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
})
defer client.Close()
client.Register(ctx, reg) // tools appear as "fs__<tool>"
```

HTTP servers with auth:

```go
client, _ := mcp.ConnectHTTP(ctx, mcp.HTTPConfig{
    Name: "api", Endpoint: "https://mcp.example.com",
    Headers: mcp.BearerHeaders(token),
})
```

## Skills

```go
set, _ := skills.LoadDir("./skills")   // folders each holding a SKILL.md
reg.MustRegister(skills.NewTool(set))  // the model can load a skill on demand
ag, _ := agent.New(agent.Config{Provider: p, Registry: reg, Skills: set})
```

## Sub-agents

Any agent can be exposed as a tool for another agent:

```go
researcher, _ := agent.New(agent.Config{Provider: p, Registry: researchReg, Name: "researcher"})
reg.MustRegister(researcher.AsTool("researcher", "Delegate research tasks."))
```

## Serving an agent over HTTP (SSE)

gage gives you the handler; you mount and serve it:

```go
h := httpx.StreamHandler(ag, func(r *http.Request) ([]gage.Message, error) {
    var body struct{ Prompt string `json:"prompt"` }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        return nil, err
    }
    return []gage.Message{gage.UserText(body.Prompt)}, nil
})
http.Handle("/agent", h)
http.ListenAndServe(":8080", nil) // your app owns this
```

## Testing

Everything is tested against `httptest`/in-memory transports — no network access:

```sh
go test ./... -race
```

## License

See repository.
