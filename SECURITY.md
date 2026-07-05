# Security

gage exposes model-driven tools. Treat every model tool call as untrusted input.

## Reporting

Please report security issues privately to the repository owner or maintainer. Do not open a public issue with exploit details before a fix is available.

## Operational Notes

- `bash` is not an operating-system sandbox. Use an external sandbox such as a container, VM, restricted user, seccomp profile, or platform sandbox when executing untrusted commands.
- Prefer `tools.BashConfig{RequireSandbox: true, Sandbox: ...}` for agents that may execute untrusted shell instructions. `tools.ExternalSandbox` can wrap firejail, bubblewrap, containers, VMs, or a platform sandbox.
- Configure an `Approver` for tools that write files, run commands, delete memory, call private services, or mutate external state.
- Prefer `policy.Secure()` as a conservative starting point: it allows local read-only filesystem calls and pauses network, shell, writes, MCP, memory mutations, and unknown tools for out-of-band approval.
- Prefer `gage.RememberingPerInput` or `gage.RememberingBy` for remembered approvals of argument-sensitive tools.
- Keep `tools.WebConfig.AllowPrivateHosts` false unless the caller intentionally trusts local/internal network access.
- Scope filesystem tools with `tools.FSConfig{Root: ...}` and avoid exposing process-wide credentials to model-controlled commands.
- Use encrypted stores (`sessions.NewEncryptedFileStore`, `oauth.NewEncryptedFileStore`) for local persisted sessions, checkpoints, and OAuth credentials when disk exposure matters.
- Scope long-lived memories by namespace/user and avoid storing sensitive or regulated data unless your application has consent, retention, audit, and deletion policies.
