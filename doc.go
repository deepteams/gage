// Package gage is a provider-agnostic toolkit for building agentic systems in Go.
//
// gage follows a hexagonal (ports & adapters) architecture: the root package
// defines the domain types (Message, ToolCall, Event, Usage, Pricing, Result...)
// and the ports (interfaces) that describe the capabilities the library needs —
// Provider (plus the optional ModelLister and TokenCounter capabilities), Tool,
// ToolRegistry, SearchProvider, Approver, Compactor, MemoryStore, Embedder,
// TokenStore and SessionStore. Concrete implementations (adapters) live in
// sub-packages and depend on the core, never the other way around:
//
//   - providers/anthropic, providers/gemini, providers/openrouter,
//     providers/vllm, providers/ollama, providers/codex, providers/claudecode
//     implement Provider; providers/fallback chains several Providers for
//     failover. providers/openai.Embeddings and providers/ollama.Embedder
//     implement Embedder.
//   - tools implements the built-in Tool set, Typed tools, and a ToolRegistry.
//   - search implements SearchProvider (duckduckgo, brave, tavily).
//   - mcp bridges Model Context Protocol servers into Tools (plus resources,
//     prompts, and sampling).
//   - skills loads SKILL.md skill folders.
//   - memory implements an in-memory MemoryStore and memory tools, with
//     optional embedding-based recall.
//   - jsonschema builds JSON Schema documents for tool parameters.
//   - sessions implements SessionStore (in-memory and JSON files).
//   - agent runs the agentic loop and streams Events.
//   - structured decodes model output into typed Go values (Generate[T]).
//   - pricing ships a dated per-model rate table for Pricing.Cost.
//   - gagetest provides a scripted Provider for testing agents offline.
//   - httpx exposes an agent over Server-Sent Events.
//   - otel (nested module github.com/deepteams/gage/otel) maps agent
//     observations onto OpenTelemetry spans.
//
// Everything streams end to end: a Provider returns a channel of Event values,
// and the agent relays those events (plus its own tool-result events) to the
// caller. gage is a library — it never starts a server or owns a main function.
package gage
