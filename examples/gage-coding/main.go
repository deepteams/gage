// Command gage-coding is an example interactive coding agent built on gage —
// the skeleton of an "opencode-like" CLI. It wires together:
//
//   - provider selection (codex via OAuth login, anthropic/openrouter via API
//     keys, ollama as local fallback); the rest of the program only ever sees
//     gage.Provider
//   - the built-in coding tools (read_file/write_file/edit/list_dir, grep/glob,
//     bash, web_fetch/web_search) confined to a workspace root
//   - an interactive terminal Approver with per-input remembered decisions
//   - SKILL.md skills advertised in the system prompt and loaded on demand
//   - a read-only "explore" sub-agent the model can delegate questions to
//   - the agent loop with guardrails (turn cap, loop detection, stream
//     retries, per-tool timeouts) and context compaction
//   - streaming rendering of gage.Events
//   - session persistence with sessions.NewFileStore
//
// Usage:
//
//	go run . -login codex          # once: OAuth login with a ChatGPT plan
//	export ANTHROPIC_API_KEY=...   # or OPENROUTER_API_KEY, or a local ollama
//	go run . [-root DIR] [-model ID] [-session NAME] [-yolo]
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/pricing"
	"github.com/deepteams/gage/providers/anthropic"
	"github.com/deepteams/gage/providers/ollama"
	"github.com/deepteams/gage/providers/openrouter"
	"github.com/deepteams/gage/search/duckduckgo"
	"github.com/deepteams/gage/sessions"
	"github.com/deepteams/gage/skills"
	"github.com/deepteams/gage/tools"
)

func main() {
	root := flag.String("root", ".", "workspace root the agent may read and modify")
	model := flag.String("model", "", "model id (default depends on the provider)")
	sessionID := flag.String("session", "", "session name to persist and resume the conversation")
	yolo := flag.Bool("yolo", false, "skip tool approval prompts (only for trusted tasks)")
	login := flag.String("login", "", `run an OAuth login flow ("codex") and exit`)
	skillsDir := flag.String("skills", ".agents/skills", "directory of SKILL.md skill folders, resolved against -root (missing dir: no skills)")
	flag.Parse()

	var err error
	if *login != "" {
		err = runLogin(*login)
	} else {
		err = run(*root, *model, *sessionID, *skillsDir, *yolo)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gage-coding:", err)
		os.Exit(1)
	}
}

// runLogin connects a subscription provider once; later runs pick up the
// stored tokens (see pickProvider).
func runLogin(provider string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch provider {
	case "codex":
		return codexLogin(ctx)
	default:
		return fmt.Errorf("unknown login provider %q (supported: codex)", provider)
	}
}

func run(root, model, sessionID, skillsDir string, yolo bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}

	provider, modelID, err := pickProvider(ctx, model)
	if err != nil {
		return err
	}

	// One reader shared between the REPL and the approver: the approver only
	// runs while the REPL is blocked on the event stream, so they never race.
	stdin := bufio.NewReader(os.Stdin)

	var approver gage.Approver
	if yolo {
		fmt.Println("⚠ approval prompts disabled (-yolo): every tool call runs unattended")
	} else {
		// RememberingPerInput caches "always" answers by tool name + exact
		// input, so approving one bash command never green-lights another.
		approver = gage.RememberingPerInput(&terminalApprover{in: stdin, out: os.Stdout})
	}

	// Skills: SKILL.md folders advertised in the system prompt (Config.Skills)
	// and loaded on demand by the model through the "skill" tool. The default
	// .agents/skills follows the cross-tool convention and is a per-project
	// location, so a relative -skills path resolves against the workspace root.
	if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(absRoot, skillsDir)
	}
	skillSet, err := loadSkills(skillsDir)
	if err != nil {
		return err
	}
	var extra []gage.Tool
	if skillSet != nil {
		extra = append(extra, skills.NewTool(skillSet))
	}

	// Sub-agent: a read-only explorer the model can delegate codebase
	// questions to (see subagent.go).
	explore, err := newExplorerTool(provider, absRoot)
	if err != nil {
		return err
	}
	extra = append(extra, explore)

	ag, err := agent.New(agent.Config{
		Name:     "gage-coding",
		Provider: provider,
		System:   systemPrompt(absRoot),
		Registry: newRegistry(absRoot, extra...),
		Skills:   skillSet,
		Approver: approver,

		// Guardrails: bound the loop, catch identical-tool-call loops, retry
		// transient stream failures, and time-box each tool execution.
		MaxTurns:         50,
		MaxToolRepeats:   3,
		MaxStreamRetries: 2,
		MaxParallelTools: 4,
		ToolTimeout:      2 * time.Minute,

		// Compaction: summarize old turns once the context reaches the
		// threshold, keeping the last 8 messages verbatim. CountTokens
		// upgrades the proactive check to an exact provider-side count when
		// the provider supports it (anthropic, gemini); otherwise the agent
		// silently falls back to the gage.EstimateTokens heuristic.
		Compactor:    agent.Summarize(provider, "", 8),
		CompactAfter: 100_000,
		CountTokens:  true,
	})
	if err != nil {
		return err
	}

	history, store, err := loadSession(ctx, absRoot, sessionID)
	if err != nil {
		return err
	}

	fmt.Printf("gage-coding · %s · root %s (/quit to exit)\n", modelID, absRoot)
	if skillSet != nil {
		names := make([]string, 0, skillSet.Len())
		for _, sk := range skillSet.List() {
			names = append(names, sk.Name)
		}
		fmt.Printf("skills: %s\n", strings.Join(names, ", "))
	}

	r := &renderer{out: os.Stdout}
	for {
		fmt.Print("\n> ")
		line, err := stdin.ReadString('\n')
		if err != nil {
			return nil // EOF (ctrl-D)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			return nil
		}

		history = append(history, gage.UserText(line))
		stream, err := ag.Run(ctx, history)
		if err != nil {
			return err
		}

		var res *gage.Result
		var runErr error
		for ev := range stream {
			r.handle(ev)
			switch ev.Type {
			case gage.EventDone:
				res = ev.Result
			case gage.EventPaused:
				// Unreachable with the synchronous terminal approver, but a
				// UI whose Approver returns gage.ErrApprovalPending would
				// persist the checkpoint here and continue with agent.Resume
				// once the user has decided.
				if store != nil {
					runErr = store.SaveSession(ctx, sessionID, gage.Session{
						Messages:   ev.Checkpoint.Messages,
						Checkpoint: ev.Checkpoint,
					})
				}
				fmt.Println("\nrun paused awaiting approval; checkpoint saved")
			case gage.EventError:
				runErr = ev.Err
			}
		}

		if runErr != nil {
			if ctx.Err() != nil {
				return nil // interrupted with ctrl-C
			}
			fmt.Fprintf(os.Stderr, "\nrun failed: %v\n", runErr)
			history = history[:len(history)-1] // drop the unanswered user message
			continue
		}
		if res == nil {
			continue
		}

		// Result.Messages is the full conversation (compacted if compaction
		// ran), so it replaces — not extends — the local history.
		history = res.Messages
		printRunSummary(res, modelID)
		if store != nil {
			if err := store.SaveSession(ctx, sessionID, gage.Session{Messages: history}); err != nil {
				fmt.Fprintf(os.Stderr, "save session: %v\n", err)
			}
		}
	}
}

// pickProvider selects a backend. Everything downstream only sees
// gage.Provider — swapping providers never touches the agent code.
func pickProvider(ctx context.Context, model string) (gage.Provider, string, error) {
	// A ChatGPT subscription connected once via `-login codex` wins: logging
	// in is the most explicit choice a user can make (see codex.go for the
	// OAuth alternative to API keys).
	if p, id, ok := codexProvider(ctx, model); ok {
		return p, id, nil
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		return anthropic.New(anthropic.Config{APIKey: key, Model: model}), model, nil
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		if model == "" {
			model = "anthropic/claude-sonnet-4.5"
		}
		return openrouter.New(key, openrouter.WithDefaultModel(model)), model, nil
	}
	// No API key: fall back to a local ollama daemon.
	if model == "" {
		model = "qwen3.6:35b-a3b-coding-mxfp8"
	}
	base := os.Getenv("OLLAMA_HOST")
	if base == "" {
		base = "http://localhost:11434"
	}
	return ollama.New(base, ollama.WithDefaultModel(model)), model, nil
}

// newRegistry assembles the built-in coding tool set, confined to root, plus
// any extra tools (the skill tool, the explore sub-agent).
func newRegistry(root string, extra ...gage.Tool) gage.ToolRegistry {
	fsCfg := tools.FSConfig{Root: root} // symlink-safe: paths cannot escape root

	all := tools.NewFSTools(fsCfg)                    // read_file, write_file, edit, list_dir
	all = append(all, tools.NewSearchTools(fsCfg)...) // grep, glob
	// BashConfig.Env is left nil on purpose: commands then run with a minimal
	// sanitized environment, so secrets in this process never leak to
	// model-driven shell commands. This is NOT an OS sandbox — for untrusted
	// input, set RequireSandbox with a real BashSandbox implementation.
	all = append(all, tools.NewBashTool(tools.BashConfig{Dir: root}))
	// Private-host blocking stays enabled on web_fetch: the model cannot
	// reach localhost or link-local addresses.
	all = append(all, tools.NewWebTools(tools.WebConfig{Search: duckduckgo.New()})...)
	all = append(all, extra...)

	// Cap every tool result so a huge file or command output cannot blow up
	// the context window.
	all = tools.LimitResultSizeAll(all, 48<<10)

	reg := tools.NewRegistry()
	reg.MustRegister(all...)
	return reg
}

func systemPrompt(root string) string {
	return fmt.Sprintf(`You are gage-coding, a careful coding agent working inside %s.

All filesystem tools are confined to that workspace root: pass paths relative
to it (e.g. "cmd/serve/main.go", or "." for the root itself).

Use the tools to inspect the project before changing it: glob/grep to locate
code, read_file before write_file or edit. Prefer minimal targeted edits over
rewriting whole files. After a change, verify it with bash (build, vet, tests)
when the project has them. Keep answers short and concrete.`, root)
}

// loadSession opens the file-backed SessionStore under root and returns the
// stored conversation, when -session is set.
func loadSession(ctx context.Context, root, id string) ([]gage.Message, gage.SessionStore, error) {
	if id == "" {
		return nil, nil, nil
	}
	store, err := sessions.NewFileStore(filepath.Join(root, ".gage-coding"))
	if err != nil {
		return nil, nil, err
	}
	s, err := store.LoadSession(ctx, id)
	switch {
	case errors.Is(err, gage.ErrSessionNotFound):
		return nil, store, nil
	case err != nil:
		return nil, nil, err
	}
	if s.Checkpoint != nil {
		// A full UI would prompt for the checkpoint's Pending() calls and call
		// agent.Resume with the decisions; this example drops the paused turn.
		fmt.Println("note: dropping a paused checkpoint from a previous run")
	}
	fmt.Printf("resumed session %q (%d messages)\n", id, len(s.Messages))
	return s.Messages, store, nil
}

func printRunSummary(res *gage.Result, model string) {
	u := res.Usage
	line := fmt.Sprintf("%d turn(s) · %d in / %d out tokens", res.Turns, u.InputTokens, u.OutputTokens)
	if cost, ok := pricing.Cost(model, u); ok {
		line += fmt.Sprintf(" · ~$%.4f", cost)
	}
	fmt.Printf("\x1b[2m%s\x1b[0m\n", line)
}
