// Package gage is a provider-agnostic toolkit for building agentic systems in Go.
//
// gage follows a hexagonal (ports & adapters) architecture: the root package
// defines the domain types (Message, ToolCall, Event, Usage...) and the ports
// (interfaces) that describe the capabilities the library needs — Provider,
// Tool, ToolRegistry, SearchProvider, Approver and TokenStore. Concrete
// implementations (adapters) live in sub-packages and depend on the core,
// never the other way around:
//
//   - providers/openrouter, providers/vllm, providers/ollama,
//     providers/codex, providers/claudecode implement Provider.
//   - tools implements the built-in Tool set and a ToolRegistry.
//   - search implements SearchProvider (duckduckgo, brave, tavily).
//   - mcp bridges Model Context Protocol servers into Tools.
//   - skills loads SKILL.md skill folders.
//   - agent runs the agentic loop and streams Events.
//   - httpx exposes an agent over Server-Sent Events.
//
// Everything streams end to end: a Provider returns a channel of Event values,
// and the agent relays those events (plus its own tool-result events) to the
// caller. gage is a library — it never starts a server or owns a main function.
package gage
