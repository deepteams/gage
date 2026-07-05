package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/deepteams/gage"
)

func TestSecurePolicyAllowsFilesystemReads(t *testing.T) {
	approval, err := Secure().Approve(context.Background(), gage.PermissionRequest{
		Tool: "read_file",
		Metadata: gage.ToolMetadata{
			ReadOnly:   true,
			Filesystem: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !approval.Allow {
		t.Fatalf("approval = %+v", approval)
	}
}

func TestSecurePolicyPausesSensitiveAndNetwork(t *testing.T) {
	for _, req := range []gage.PermissionRequest{
		{Tool: "bash", Metadata: gage.ToolMetadata{Shell: true, Destructive: true}},
		{Tool: "write_file", Metadata: gage.ToolMetadata{Filesystem: true, Destructive: true, RequiresApproval: true}},
		{Tool: "web_fetch", Metadata: gage.ToolMetadata{ReadOnly: true, Network: true}},
		{Tool: "unknown"},
	} {
		if _, err := Secure().Approve(context.Background(), req); !errors.Is(err, gage.ErrApprovalPending) {
			t.Fatalf("%s err = %v, want ErrApprovalPending", req.Tool, err)
		}
	}
}

func TestPolicyDenyTags(t *testing.T) {
	approval, err := New(Config{DenyTags: []string{"prod"}}).Approve(context.Background(), gage.PermissionRequest{
		Tool:     "deploy",
		Metadata: gage.ToolMetadata{ReadOnly: true, Tags: []string{"prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if approval.Allow || approval.Reason == "" {
		t.Fatalf("approval = %+v", approval)
	}
}
