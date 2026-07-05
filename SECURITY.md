# Security

gage exposes model-driven tools. Treat every model tool call as untrusted input.

## Reporting

Please report security issues privately to the repository owner or maintainer. Do not open a public issue with exploit details before a fix is available.

## Operational Notes

- `bash` is not an operating-system sandbox. Use an external sandbox such as a container, VM, restricted user, seccomp profile, or platform sandbox when executing untrusted commands.
- Configure an `Approver` for tools that write files, run commands, delete memory, call private services, or mutate external state.
- Prefer `gage.RememberingPerInput` or `gage.RememberingBy` for remembered approvals of argument-sensitive tools.
- Keep `tools.WebConfig.AllowPrivateHosts` false unless the caller intentionally trusts local/internal network access.
- Scope filesystem tools with `tools.FSConfig{Root: ...}` and avoid exposing process-wide credentials to model-controlled commands.
