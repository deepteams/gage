// Package gage is a provider-agnostic toolkit for building agentic systems in Go.
//
// gage follows a hexagonal (ports & adapters) architecture: the root package
// defines the domain types (Message, ToolCall, Event, Usage, Result...) and
// the ports (interfaces) that describe the capabilities the library needs —
// Provider, Tool, ToolRegistry, SearchProvider, Approver, Compactor,
// MemoryStore, TokenStore and SessionStore. Concrete implementations (adapters) live in
// sub-packages and depend on the core, never the other way around:
//
//   - providers/anthropic, providers/openrouter, providers/vllm,
//     providers/ollama, providers/codex, providers/claudecode implement
//     Provider; providers/fallback chains several Providers for failover.
//   - tools implements the built-in Tool set, Typed tools, and a ToolRegistry.
//   - search implements SearchProvider (duckduckgo, brave, tavily).
//   - mcp bridges Model Context Protocol servers into Tools (plus resources,
//     prompts, and sampling).
//   - skills loads SKILL.md skill folders.
//   - memory implements an in-memory MemoryStore and memory tools.
//   - jsonschema builds JSON Schema documents for tool parameters.
//   - sessions implements SessionStore (in-memory and JSON files).
//   - agent runs the agentic loop and streams Events.
//   - httpx exposes an agent over Server-Sent Events.
//
// Everything streams end to end: a Provider returns a channel of Event values,
// and the agent relays those events (plus its own tool-result events) to the
// caller. gage is a library — it never starts a server or owns a main function.
package gage
