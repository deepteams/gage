// Package policy provides conservative Approver implementations for agents
// that expose model-driven tools.
package policy

import (
	"context"
	"fmt"

	"github.com/deepteams/gage"
)

// Action is the policy outcome for a category of tool call.
type Action string

const (
	// Allow permits the call.
	Allow Action = "allow"
	// Deny blocks the call with a model-visible reason.
	Deny Action = "deny"
	// Pause returns gage.ErrApprovalPending so the agent checkpoints and lets
	// the application collect an out-of-band decision.
	Pause Action = "pause"
)

// Config maps tool metadata to approval outcomes. The zero value is
// conservative: read-only calls are allowed, everything else pauses.
type Config struct {
	// ReadOnly covers read-only calls that are neither network nor filesystem.
	ReadOnly Action
	// FilesystemRead covers local read-only filesystem calls.
	FilesystemRead Action
	// NetworkRead covers read-only network calls such as web_fetch/search.
	NetworkRead Action
	// Sensitive covers destructive, write, memory-mutating, or
	// RequiresApproval calls.
	Sensitive Action
	// Shell covers shell/subprocess calls. It wins over Sensitive.
	Shell Action
	// Unknown covers tools without metadata.
	Unknown Action
	// DenyTags always deny calls whose metadata contains one of these tags.
	DenyTags []string
}

// SecureConfig returns the recommended default policy for interactive agents:
// local read-only tools are allowed; network, shell, writes, MCP, memory
// mutations and unknown tools pause for explicit approval.
func SecureConfig() Config {
	return Config{
		ReadOnly:       Allow,
		FilesystemRead: Allow,
		NetworkRead:    Pause,
		Sensitive:      Pause,
		Shell:          Pause,
		Unknown:        Pause,
	}
}

// StrictConfig pauses every call except tools explicitly denied by tag.
func StrictConfig() Config {
	return Config{
		ReadOnly:       Pause,
		FilesystemRead: Pause,
		NetworkRead:    Pause,
		Sensitive:      Pause,
		Shell:          Pause,
		Unknown:        Pause,
	}
}

// Secure returns an Approver using SecureConfig.
func Secure() gage.Approver { return New(SecureConfig()) }

// New builds an Approver from cfg. Unset actions inherit the zero-value
// conservative defaults described on Config.
func New(cfg Config) gage.Approver {
	cfg = withDefaults(cfg)
	return approver{cfg: cfg}
}

type approver struct {
	cfg Config
}

func (a approver) Approve(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	if err := ctx.Err(); err != nil {
		return gage.Approval{}, err
	}
	meta := req.Metadata
	for _, deny := range a.cfg.DenyTags {
		for _, tag := range meta.Tags {
			if tag == deny {
				return gage.Denied(fmt.Sprintf("policy denied tag %q", tag)), nil
			}
		}
	}

	action, reason := a.action(meta)
	switch action {
	case Allow:
		return gage.Allowed(), nil
	case Deny:
		return gage.Denied(reason), nil
	default:
		return gage.Approval{}, fmt.Errorf("%w: %s", gage.ErrApprovalPending, reason)
	}
}

func (a approver) action(meta gage.ToolMetadata) (Action, string) {
	switch {
	case meta.Shell:
		return a.cfg.Shell, "shell tool requires approval"
	case meta.Destructive || meta.RequiresApproval:
		return a.cfg.Sensitive, "sensitive tool requires approval"
	case meta.Network:
		return a.cfg.NetworkRead, "network tool requires approval"
	case meta.Filesystem && meta.ReadOnly:
		return a.cfg.FilesystemRead, "filesystem read approved by policy"
	case meta.ReadOnly:
		return a.cfg.ReadOnly, "read-only tool approved by policy"
	default:
		return a.cfg.Unknown, "unknown tool risk requires approval"
	}
}

func withDefaults(cfg Config) Config {
	def := SecureConfig()
	if cfg.ReadOnly == "" {
		cfg.ReadOnly = def.ReadOnly
	}
	if cfg.FilesystemRead == "" {
		cfg.FilesystemRead = def.FilesystemRead
	}
	if cfg.NetworkRead == "" {
		cfg.NetworkRead = def.NetworkRead
	}
	if cfg.Sensitive == "" {
		cfg.Sensitive = def.Sensitive
	}
	if cfg.Shell == "" {
		cfg.Shell = def.Shell
	}
	if cfg.Unknown == "" {
		cfg.Unknown = def.Unknown
	}
	return cfg
}
