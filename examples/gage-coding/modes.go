package main

import (
	"fmt"
	"strings"
)

type agentMode string

const (
	modeBuild  agentMode = "build"
	modePlan   agentMode = "plan"
	modeReview agentMode = "review"
)

type modeSpec struct {
	Name        agentMode
	Label       string
	Description string
	System      string
}

func parseMode(raw string) (agentMode, error) {
	switch agentMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", modeBuild:
		return modeBuild, nil
	case modePlan:
		return modePlan, nil
	case modeReview:
		return modeReview, nil
	default:
		return "", fmt.Errorf("unknown mode %q (use build, plan, or review)", raw)
	}
}

func mustMode(raw string) agentMode {
	m, err := parseMode(raw)
	if err != nil {
		return modeBuild
	}
	return m
}

func specForMode(mode agentMode) modeSpec {
	switch mode {
	case modePlan:
		return modeSpec{
			Name:        modePlan,
			Label:       "Plan",
			Description: "read-only planning; no file writes or shell commands",
			System: `Mode: Plan.
You may inspect the workspace and ask clarifying questions, but you must not
modify files or run shell commands. Produce a concise implementation plan with
risks, validation steps, and files likely to change.`,
		}
	case modeReview:
		return modeSpec{
			Name:        modeReview,
			Label:       "Review",
			Description: "code-review posture; no edits, shell verification can be requested",
			System: `Mode: Review.
Take a code-review stance. Prioritize bugs, behavioral regressions, security
risks, and missing tests. Do not modify files. Findings should be concrete,
ordered by severity, and reference files/symbols when possible.`,
		}
	default:
		return modeSpec{
			Name:        modeBuild,
			Label:       "Build",
			Description: "full coding mode with guarded edits and commands",
			System: `Mode: Build.
Inspect before changing code, make targeted edits, maintain the repository's
style, and verify with the smallest meaningful build, lint, or test command.
Use the todo tools for multi-step work and keep the user updated.`,
		}
	}
}

func modeNames() []string {
	return []string{string(modeBuild), string(modePlan), string(modeReview)}
}
