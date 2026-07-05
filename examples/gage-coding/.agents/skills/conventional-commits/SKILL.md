---
name: conventional-commits
description: How to stage and write a git commit for this repository (conventional commit format). Load before committing work.
---

# Conventional commits

When the user asks to commit work:

1. Inspect what changed: `git status --short` then `git diff` (and
   `git diff --staged` if something is already staged).
2. Stage only the files related to the request with `git add <paths>`.
   Never use `git add -A` or `git add .`.
3. Write the message as `type(scope): summary`:
   - `type` is one of `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.
   - `scope` is the package or area touched (e.g. `agent`, `tools`, `provider`).
   - `summary` is imperative, lowercase, no trailing period, ≤ 72 chars.
4. If the change needs explanation, add a body separated by a blank line:
   what changed and why, wrapped at 72 columns.
5. Show the exact `git commit -m ...` command and run it after approval.

Never commit if the build or tests fail: run the project's build/test command
first and report failures instead.
