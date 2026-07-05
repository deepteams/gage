package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

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

type approvalAsker interface {
	AskApproval(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error)
}

type configuredApprover struct {
	policy permissionPolicy
	tools  map[string]bool
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
	if a.disabled(req) {
		return gage.Denied("tool disabled by config"), nil
	}
	action := a.resolve(req)
	switch action {
	case permissionAllow:
		return gage.Allowed(), nil
	case permissionDeny:
		return gage.Denied("denied by permission config"), nil
	default:
		if a.auto || a.asker == nil {
			return gage.Allowed(), nil
		}
		return a.asker.AskApproval(ctx, req)
	}
}

func (a *configuredApprover) disabled(req gage.PermissionRequest) bool {
	if len(a.tools) == 0 {
		return false
	}
	for _, key := range permissionKeys(req) {
		if enabled, ok := a.tools[key]; ok && !enabled {
			return true
		}
	}
	if enabled, ok := a.tools[req.Tool]; ok && !enabled {
		return true
	}
	return false
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

func (r permissionRule) Match(target string) (permissionAction, bool) {
	var matched *permissionAction
	for _, pr := range r.Patterns {
		if wildcardMatch(pr.Pattern, target) {
			a := pr.Action
			matched = &a
		}
	}
	if matched != nil {
		return *matched, true
	}
	if r.Action != nil {
		return *r.Action, true
	}
	return "", false
}

func defaultPermission(req gage.PermissionRequest) permissionAction {
	target := permissionTarget(req)
	if req.Tool == "read_file" && isSensitiveReadPath(target) {
		return permissionDeny
	}
	m := req.Metadata
	if m.ReadOnly && m.Filesystem && !m.Network && !m.Destructive {
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
