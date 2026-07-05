package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/agent"
	"github.com/deepteams/gage/mcp"
	"github.com/deepteams/gage/pricing"
	"github.com/deepteams/gage/search/duckduckgo"
	"github.com/deepteams/gage/sessions"
	"github.com/deepteams/gage/skills"
	"github.com/deepteams/gage/tools"
)

type appRuntime struct {
	root         string
	modelID      string
	provider     gage.Provider
	cfg          appConfig
	policy       permissionPolicy
	skillsDir    string
	skillSet     *skills.Set
	instructions instructionBundle
	commands     *commandSet
	sessionID    string
	store        gage.SessionStore
	history      []gage.Message
	snapshots    *snapshotManager
	formatters   *formatterManager
	todos        *todoStore
	approver     gage.Approver
	approval     approvalAsker
	questioner   questionAsker
	auto         bool
	mcpClients   []*mcp.Client
	mcpTools     []gage.Tool
}

func newRuntime(ctx context.Context, root, model, sessionID, skillsDir, configPath string) (*appRuntime, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(absRoot, configPath)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = cfg.Model
	}
	provider, modelID, err := pickProvider(ctx, model)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(absRoot, skillsDir)
	}
	skillSet, err := loadSkills(skillsDir)
	if err != nil {
		return nil, err
	}
	instructions, err := loadInstructions(absRoot, cfg.Instructions)
	if err != nil {
		return nil, err
	}
	commands, err := loadCommands(absRoot, cfg.Command)
	if err != nil {
		return nil, err
	}
	policy, err := parsePermissionPolicy(cfg.Permission)
	if err != nil {
		return nil, err
	}
	history, store, err := loadSession(ctx, absRoot, sessionID)
	if err != nil {
		return nil, err
	}

	rt := &appRuntime{
		root:         absRoot,
		modelID:      modelID,
		provider:     provider,
		cfg:          cfg,
		policy:       policy,
		skillsDir:    skillsDir,
		skillSet:     skillSet,
		instructions: instructions,
		commands:     commands,
		sessionID:    sessionID,
		store:        store,
		history:      history,
		snapshots:    newSnapshotManager(absRoot),
		formatters:   newFormatterManager(absRoot, cfg.Formatters),
		todos:        newTodoStore(),
	}
	if err := rt.connectMCP(ctx); err != nil {
		return nil, err
	}
	return rt, nil
}

func (a *appRuntime) Close() {
	for _, c := range a.mcpClients {
		_ = c.Close()
	}
}

func (a *appRuntime) SetInteractors(approval approvalAsker, question questionAsker, auto bool) {
	a.approval = approval
	a.questioner = question
	a.auto = auto
	a.approver = gage.RememberingPerInput(&configuredApprover{
		policy: a.policy,
		auto:   auto,
		asker:  approval,
	})
}

func (a *appRuntime) RunPrompt(ctx context.Context, prompt string, mode agentMode, emit func(gage.Event)) (*gage.Result, string, error) {
	ag, err := a.newAgent(mode)
	if err != nil {
		return nil, "", err
	}
	a.snapshots.Begin(prompt)
	a.history = append(a.history, gage.UserText(prompt))
	stream, err := ag.Run(ctx, a.history)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		a.snapshots.Discard()
		return nil, "", err
	}

	var res *gage.Result
	var paused *gage.Checkpoint
	var runErr error
	for ev := range stream {
		if emit != nil {
			emit(ev)
		}
		switch ev.Type {
		case gage.EventDone:
			res = ev.Result
		case gage.EventPaused:
			paused = ev.Checkpoint
		case gage.EventError:
			runErr = ev.Err
		}
	}

	if runErr != nil {
		// The turn failed mid-flight. Commit (not discard) any file changes the
		// tools already applied so /undo can still revert them, then drop the
		// dangling user message so the conversation stays consistent.
		a.snapshots.Commit()
		a.history = a.history[:len(a.history)-1]
		return nil, "", runErr
	}

	if paused != nil {
		// The approver deferred the decision (ErrApprovalPending). Keep the
		// applied changes undoable, align in-memory history with the checkpoint
		// we persist, and tell the user instead of silently swallowing the run.
		a.snapshots.Commit()
		a.history = paused.Messages
		if a.store != nil {
			if err := a.store.SaveSession(ctx, a.sessionID, gage.Session{
				Messages:   paused.Messages,
				Checkpoint: paused,
			}); err != nil {
				return nil, "", fmt.Errorf("save paused checkpoint: %w", err)
			}
		}
		return nil, "run paused awaiting approval; checkpoint saved", nil
	}

	if res == nil {
		a.history = a.history[:len(a.history)-1]
		a.snapshots.Discard()
		return nil, "", nil
	}

	a.history = res.Messages
	changed := a.snapshots.Commit()
	summary := runSummary(res, a.modelID)
	if changed > 0 {
		summary += fmt.Sprintf(" · %d tracked file(s)", changed)
	}
	if a.store != nil {
		if err := a.store.SaveSession(ctx, a.sessionID, gage.Session{Messages: a.history}); err != nil {
			// A successful turn must not be reported as failed just because the
			// session could not be persisted; surface it as a warning instead.
			summary += fmt.Sprintf(" · warning: session not saved: %v", err)
		}
	}
	return res, summary, nil
}

func (a *appRuntime) newAgent(mode agentMode) (*agent.Agent, error) {
	reg := tools.NewRegistry()
	all := a.toolsForMode(mode)
	all = tools.LimitResultSizeAll(all, 48<<10)
	reg.MustRegister(all...)

	maxTurns := a.cfg.MaxTurns
	if maxTurns == 0 {
		maxTurns = 50
	}
	compactAfter := a.cfg.CompactAfter
	if compactAfter == 0 {
		compactAfter = 100_000
	}
	toolTimeout := a.cfg.ToolTimeout.Duration()
	if toolTimeout == 0 {
		toolTimeout = 2 * time.Minute
	}
	return agent.New(agent.Config{
		Name:             "gage-coding/" + string(mode),
		Provider:         a.provider,
		System:           a.systemPrompt(mode),
		Registry:         reg,
		Skills:           a.skillSet,
		Approver:         a.approver,
		MaxTurns:         maxTurns,
		MaxToolRepeats:   3,
		MaxStreamRetries: 2,
		MaxParallelTools: 4,
		ToolTimeout:      toolTimeout,
		Compactor:        agent.Summarize(a.provider, "", 8),
		CompactAfter:     compactAfter,
		CountTokens:      true,
	})
}

func (a *appRuntime) toolsForMode(mode agentMode) []gage.Tool {
	fsCfg := tools.FSConfig{Root: a.root}
	var all []gage.Tool
	all = append(all, tools.NewFSTools(fsCfg)...)
	all = wrapMutations(all, a.snapshots, a.formatters)
	all = append(all, tools.NewSearchTools(fsCfg)...)
	if mode != modePlan {
		all = append(all, tools.NewBashTool(tools.BashConfig{Dir: a.root}))
	}
	all = append(all, tools.NewWebTools(tools.WebConfig{Search: duckduckgo.New()})...)
	if a.skillSet != nil {
		all = append(all, skills.NewTool(a.skillSet))
	}
	all = append(all, a.todos.tools()...)
	all = append(all, newQuestionTool(a.questioner))
	if explore, err := newExplorerTool(a.provider, a.root); err == nil {
		all = append(all, explore)
	}
	if mode != modePlan {
		all = append(all, a.mcpTools...)
	}

	filtered := all[:0]
	for _, t := range all {
		if toolAllowedInMode(mode, t) && a.toolEnabled(t) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func toolAllowedInMode(mode agentMode, t gage.Tool) bool {
	name := t.Name()
	meta := gage.MetadataOf(t)
	switch mode {
	case modePlan:
		switch name {
		case "skill", "explore", "question", "todowrite", "todoread":
			return true
		}
		return meta.ReadOnly && !meta.Shell && !meta.Destructive && !meta.RequiresApproval
	case modeReview:
		// Review inspects and may run shell verification, but must not mutate
		// the workspace. Filter on metadata so mutating MCP servers and future
		// built-ins are blocked too — not just write_file/edit by name. Bash
		// stays for verification; the permission layer still gates each command.
		if name == "bash" {
			return true
		}
		if meta.Destructive || (meta.Filesystem && !meta.ReadOnly) || meta.RequiresApproval {
			return false
		}
		return true
	default:
		return true
	}
}

func (a *appRuntime) toolEnabled(t gage.Tool) bool {
	if len(a.cfg.Tools) == 0 {
		return true
	}
	// Consult the tool name, its permission category, and the "*" catch-all so
	// {"tools": {"*": false}} disables everything (registry filtering is the
	// single enforcement point; the approver no longer re-checks this).
	keys := []string{t.Name(), toolPermissionKey(t), "*"}
	for _, key := range keys {
		if enabled, ok := a.cfg.Tools[key]; ok && !enabled {
			return false
		}
	}
	return true
}

func toolPermissionKey(t gage.Tool) string {
	return permissionKey(gage.PermissionRequest{Tool: t.Name(), Metadata: gage.MetadataOf(t)})
}

func (a *appRuntime) systemPrompt(mode agentMode) string {
	spec := specForMode(mode)
	var b strings.Builder
	fmt.Fprintf(&b, `You are gage-coding, an opencode-like coding agent demo working inside %s.

All filesystem tools are confined to that workspace root: pass paths relative
to it. Use glob/grep to locate code, read_file before write_file or edit, and
prefer minimal targeted edits.

%s

Use todowrite/todoread for multi-step work. Use question when the task is
underspecified enough that guessing would risk the wrong target or scope.
`, a.root, spec.System)
	if a.instructions.Text != "" {
		b.WriteString("\nProject instructions loaded from AGENTS-style files:\n\n")
		b.WriteString(a.instructions.Text)
	}
	return b.String()
}

func (a *appRuntime) connectMCP(ctx context.Context) error {
	// The servers are independent, so dial them concurrently: startup latency
	// becomes the slowest single connect rather than the sum of all of them.
	names := sortedKeys(a.cfg.MCP)
	type result struct {
		client *mcp.Client
		tools  []gage.Tool
		err    error
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		cfg := a.cfg.MCP[name]
		if cfg.Enabled != nil && !*cfg.Enabled {
			continue
		}
		wg.Add(1)
		go func(i int, name string, cfg mcpServerConfig) {
			defer wg.Done()
			timeout := 5 * time.Second
			if cfg.TimeoutMS > 0 {
				timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			client, err := a.connectOneMCP(cctx, name, cfg)
			cancel()
			if err != nil {
				results[i] = result{err: fmt.Errorf("mcp %s: %w", name, err)}
				return
			}
			ts, err := client.Tools(ctx)
			if err != nil {
				_ = client.Close()
				results[i] = result{err: fmt.Errorf("mcp %s: %w", name, err)}
				return
			}
			results[i] = result{client: client, tools: ts}
		}(i, name, cfg)
	}
	wg.Wait()

	for _, r := range results {
		if r.err == nil {
			continue
		}
		for _, other := range results {
			if other.client != nil {
				_ = other.client.Close()
			}
		}
		return r.err
	}
	for _, r := range results {
		if r.client != nil {
			a.mcpClients = append(a.mcpClients, r.client)
			a.mcpTools = append(a.mcpTools, r.tools...)
		}
	}
	return nil
}

func (a *appRuntime) connectOneMCP(ctx context.Context, name string, cfg mcpServerConfig) (*mcp.Client, error) {
	switch strings.ToLower(cfg.Type) {
	case "local", "stdio":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("mcp %s: command is required", name)
		}
		dir := cfg.CWD
		if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(a.root, dir)
		}
		var env []string
		for _, key := range sortedKeys(cfg.Environment) {
			env = append(env, key+"="+cfg.Environment[key])
		}
		return mcp.ConnectStdio(ctx, mcp.StdioConfig{
			Name:    name,
			Command: cfg.Command[0],
			Args:    cfg.Command[1:],
			Env:     env,
			Dir:     dir,
		}, mcp.WithSamplingProvider(a.provider))
	case "remote", "http":
		return mcp.ConnectHTTP(ctx, mcp.HTTPConfig{
			Name:     name,
			Endpoint: cfg.URL,
			Headers:  cfg.Headers,
		}, mcp.WithSamplingProvider(a.provider))
	default:
		return nil, fmt.Errorf("mcp %s: unknown type %q", name, cfg.Type)
	}
}

type slashResult struct {
	Output   string
	Prompt   string
	Mode     agentMode
	SetMode  *agentMode
	Quit     bool
	ClearLog bool
}

func (a *appRuntime) HandleSlash(ctx context.Context, line string, current agentMode) (slashResult, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return slashResult{}, nil
	}
	name := strings.TrimPrefix(fields[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	switch name {
	case "q", "quit", "exit":
		return slashResult{Quit: true}, nil
	case "help", "?":
		return slashResult{Output: a.helpText()}, nil
	case "mode":
		if args == "" {
			return slashResult{Output: fmt.Sprintf("current mode: %s\navailable: %s", current, strings.Join(modeNames(), ", "))}, nil
		}
		mode, err := parseMode(args)
		if err != nil {
			return slashResult{}, err
		}
		return slashResult{Output: "mode set to " + string(mode), SetMode: &mode}, nil
	case "init":
		out, err := initAgentsFile(a.root)
		if err == nil {
			_ = a.Reload(ctx)
		}
		return slashResult{Output: out}, err
	case "reload":
		return slashResult{Output: "reloaded config, skills, commands, and instructions"}, a.Reload(ctx)
	case "clear":
		a.history = nil
		if a.store != nil {
			_ = a.store.SaveSession(ctx, a.sessionID, gage.Session{})
		}
		return slashResult{Output: "conversation cleared", ClearLog: true}, nil
	case "undo":
		out, err := a.snapshots.Undo()
		return slashResult{Output: out}, err
	case "redo":
		out, err := a.snapshots.Redo()
		return slashResult{Output: out}, err
	case "tools":
		mode := current
		if args != "" {
			var err error
			mode, err = parseMode(args)
			if err != nil {
				return slashResult{}, err
			}
		}
		return slashResult{Output: a.listTools(mode)}, nil
	case "skills":
		return slashResult{Output: a.listSkills()}, nil
	case "commands":
		return slashResult{Output: a.listCommands()}, nil
	case "sessions":
		out, err := a.listSessions(ctx)
		return slashResult{Output: out}, err
	default:
		if cmd, ok := a.commands.Get(name); ok {
			mode := cmd.Mode
			if mode == "" {
				mode = current
			}
			return slashResult{Prompt: expandCommand(cmd, args), Mode: mode}, nil
		}
		return slashResult{Output: "unknown command: /" + name + " (try /help)"}, nil
	}
}

func (a *appRuntime) Reload(ctx context.Context) error {
	cfg, err := loadConfig(a.root, a.cfg.Path)
	if err != nil {
		return err
	}
	if cfg.Path == "" {
		cfg.Path = a.cfg.Path
	}
	a.cfg = cfg
	policy, err := parsePermissionPolicy(cfg.Permission)
	if err != nil {
		return err
	}
	a.policy = policy
	skillSet, err := loadSkills(a.skillsDir)
	if err != nil {
		return err
	}
	a.skillSet = skillSet
	instructions, err := loadInstructions(a.root, cfg.Instructions)
	if err != nil {
		return err
	}
	a.instructions = instructions
	commands, err := loadCommands(a.root, cfg.Command)
	if err != nil {
		return err
	}
	a.commands = commands
	a.formatters = newFormatterManager(a.root, cfg.Formatters)
	a.SetInteractors(a.approval, a.questioner, a.auto)
	return nil
}

func (a *appRuntime) helpText() string {
	return strings.TrimSpace(`Slash commands:
  /mode [build|plan|review]  switch agent mode
  /init                      create AGENTS.md starter instructions
  /tools [mode]              list tools visible in a mode
  /skills                    list loaded SKILL.md skills
  /commands                  list custom .agents/commands
  /undo /redo                revert or reapply tracked write_file/edit changes
  /clear                     clear conversation history
  /reload                    reload config, instructions, skills, commands
  /quit                      exit

Custom commands: add .agents/commands/name.md and run /name arguments.`)
}

func (a *appRuntime) listTools(mode agentMode) string {
	ts := a.toolsForMode(mode)
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, fmt.Sprintf("%s [%s]", t.Name(), toolPermissionKey(t)))
	}
	return strings.Join(names, "\n")
}

func (a *appRuntime) listSkills() string {
	if a.skillSet == nil || a.skillSet.Len() == 0 {
		return "no skills loaded"
	}
	var names []string
	for _, sk := range a.skillSet.List() {
		names = append(names, sk.Name)
	}
	return strings.Join(names, "\n")
}

func (a *appRuntime) listCommands() string {
	cmds := a.commands.List()
	if len(cmds) == 0 {
		return "no custom commands loaded"
	}
	var b strings.Builder
	for _, cmd := range cmds {
		desc := cmd.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "/%s [%s] %s\n", cmd.Name, cmd.Mode, desc)
	}
	return strings.TrimSpace(b.String())
}

func (a *appRuntime) listSessions(ctx context.Context) (string, error) {
	if a.store == nil {
		return "sessions disabled; pass -session NAME to persist one", nil
	}
	ids, err := a.store.ListSessions(ctx)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "no sessions", nil
	}
	return strings.Join(ids, "\n"), nil
}

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
		fmt.Println("note: dropping a paused checkpoint from a previous run")
	}
	return s.Messages, store, nil
}

func runSummary(res *gage.Result, model string) string {
	u := res.Usage
	line := fmt.Sprintf("%d turn(s) · %d in / %d out tokens", res.Turns, u.InputTokens, u.OutputTokens)
	if cost, ok := pricing.Cost(model, u); ok {
		line += fmt.Sprintf(" · ~$%.4f", cost)
	}
	return line
}

func toolInputString(raw json.RawMessage, max int) string {
	s := compactJSON(raw, max)
	if len(s) == 0 {
		return "{}"
	}
	return s
}
