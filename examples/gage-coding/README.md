# gage-coding

An opencode-like coding agent demo built on `gage`.

It is still an example, not a product, but it now demonstrates most of the
moving pieces you expect from a modern terminal coding agent:

- Bubble Tea TUI with streaming tool calls, approvals, modes, and slash commands
- Build / Plan / Review agent modes
- guarded coding tools: read/write/edit/list, grep/glob, bash, web fetch/search
- `AGENTS.md` / `CLAUDE.md` style project instructions
- Claude Code-style `SKILL.md` skills
- `.agents/commands/*.md` custom commands with `$ARGUMENTS`
- `todowrite`, `todoread`, and `question` tools
- permission config with `allow`, `ask`, `deny`, wildcards, and auto mode
- format-on-edit, currently defaulting Go files through `gofmt`
- undo/redo snapshots for changes made through `write_file` and `edit`
- optional local/remote MCP tools from config
- session persistence, context compaction, retries, tool timeouts, and cost summary
- Codex and Claude Code OAuth login flows, plus API-key and Ollama providers

## Run

```sh
cd examples/gage-coding

# OAuth subscription providers:
go run . -login codex
go run . -login claude

# Or API keys:
export ANTHROPIC_API_KEY=sk-ant-...
export OPENROUTER_API_KEY=sk-or-...

# Or neither: falls back to local Ollama.
go run . -session demo
```

By default the Bubble Tea TUI starts. Use `-tui=false` for the plain REPL.

Provider precedence is: stored Codex tokens, stored Claude Code tokens,
`ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, local Ollama.

Useful flags:

- `-root DIR` confines filesystem tools to a workspace root.
- `-model ID` overrides the provider default or config model.
- `-session NAME` persists conversation state under `<root>/.gage-coding/`.
- `-auto` approves permission requests unless config denies them.
- `-config PATH` uses a specific `.gage-coding.json/.jsonc`.
- `-skills DIR` points at `SKILL.md` skill folders.

## TUI Commands

Inside the TUI:

```text
/mode plan      switch to read-only planning
/mode build     switch back to coding
/mode review    code-review posture, no edits
/init           create AGENTS.md starter rules
/tools          list tools visible in the current mode
/skills         list loaded skills
/commands       list custom commands
/undo           revert tracked write_file/edit changes
/redo           reapply the last undo
/reload         reload config, rules, skills, and commands
/clear          clear conversation history
/quit           exit
```

Custom commands live in `.agents/commands/*.md`. This example ships:

- `/plan some feature`
- `/review some path or behavior`
- `/test some scope`
- `/commit`

## Config

The included `.gage-coding.jsonc` demonstrates the opencode-style surface:

- `permission` controls tool categories with `allow`, `ask`, `deny`.
- `tools` can hide tools entirely.
- `formatters` maps file extensions to commands, using `$FILE`.
- `mcp` can define local or remote MCP servers. The sample MCP is disabled.
- `instructions` can add extra local instruction files or globs.

Example permission snippet:

```jsonc
{
  "permission": {
    "read": {
      "*": "allow",
      "*.env": "deny",
      "*.env.*": "deny",
      "*.env.example": "allow"
    },
    "bash": {
      "*": "ask",
      "git status*": "allow",
      "rm *": "deny"
    },
    "edit": "ask"
  }
}
```

## File Map

| File | Demonstrates |
|---|---|
| `main.go` | flags, TUI/REPL entrypoints, provider selection |
| `tui.go` | Bubble Tea UI, streaming events, approvals, question prompts |
| `runtime.go` | mode-specific agent construction, sessions, MCP, slash commands |
| `permissions.go` | opencode-like permission rules and wildcards |
| `modes.go` | Build / Plan / Review system behavior |
| `commands.go` + `.agents/commands/` | markdown custom commands |
| `instructions.go` | `AGENTS.md`, `CLAUDE.md`, and rules loading |
| `mutations.go` | format-on-edit and undo/redo snapshots |
| `extra_tools.go` | `todowrite`, `todoread`, `question` tools |
| `skills.go` + `.agents/skills/` | `SKILL.md` loading and `skill` tool |
| `subagent.go` | read-only `explore` sub-agent exposed as a tool |
| `codex.go`, `claude.go` | OAuth subscription providers |
| `render.go` | plain REPL event renderer |

## Deliberate Limits

- The Bubble Tea UI is a demo TUI, not a full opencode clone.
- Undo/redo tracks `write_file` and `edit`; shell commands may change files
  outside that snapshot mechanism.
- `bash` is guarded and uses a sanitized environment, but it is not an OS
  sandbox. For untrusted work, configure `BashConfig.RequireSandbox` with a
  real sandbox implementation.
- MCP OAuth management, LSP tools, share links, and server/web clients are left
  as follow-up examples.
