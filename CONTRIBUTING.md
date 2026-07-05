# Contributing

gage is a Go library for agentic systems. Keep changes scoped, streamed, and provider-agnostic unless a package is explicitly provider-specific.

## Development

Run these before opening a PR:

```sh
go vet ./...
go test ./...
go test -race ./...
```

Tests should not call real external services. Use `httptest`, in-memory stores, or fake providers.

## Design Rules

- Keep the root `gage` package limited to domain types and ports.
- Put concrete integrations in adapter packages.
- Do not silently ignore explicit generation options. Return `gage.ErrUnsupported` when a provider cannot honor one.
- Tool-level failures should be `ToolResult{IsError: true}` so the model can react; reserve Go errors for infrastructure failures.
- Security-sensitive tools need tests for confinement, cancellation, output limits, and approval metadata.
