package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepteams/gage"
)

type deployArgs struct {
	Service  string            `json:"service" desc:"Service to deploy."`
	Env      string            `json:"env" desc:"Target environment." enum:"dev,staging,prod"`
	Replicas int               `json:"replicas,omitempty" desc:"Replica count."`
	Ratio    float64           `json:"ratio"`
	DryRun   *bool             `json:"dry_run" desc:"Plan only."`
	Tags     []string          `json:"tags,omitempty"`
	Labels   map[string]string `json:"labels,omitempty" desc:"Free-form labels."`
	Target   struct {
		Region string `json:"region" desc:"Region name."`
		Zone   string `json:"zone,omitempty"`
	} `json:"target" desc:"Placement target."`
	Payload json.RawMessage `json:"payload,omitempty"`
	Blob    []byte          `json:"blob,omitempty"`
	skipped string          //nolint:unused // exercises unexported-field skipping
	Ignored string          `json:"-"`
}

const deploySchemaGolden = `{"additionalProperties":false,"properties":` +
	`{"blob":{"type":"string"},` +
	`"dry_run":{"type":"boolean","description":"Plan only."},` +
	`"env":{"type":"string","description":"Target environment.","enum":["dev","staging","prod"]},` +
	`"labels":{"type":"object","description":"Free-form labels."},` +
	`"payload":{},` +
	`"ratio":{"type":"number"},` +
	`"replicas":{"type":"integer","description":"Replica count."},` +
	`"service":{"type":"string","description":"Service to deploy."},` +
	`"tags":{"type":"array","items":{"type":"string"}},` +
	`"target":{"type":"object","description":"Placement target.","properties":` +
	`{"region":{"type":"string","description":"Region name."},"zone":{"type":"string"}},"required":["region"]}},` +
	`"required":["service","env","ratio","target"],"type":"object"}`

func TestTypedSchemaGolden(t *testing.T) {
	tool := Typed("deploy", "Deploy a service.", func(ctx context.Context, args deployArgs) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	})
	// Normalize key order by decoding and re-encoding both sides.
	if got, want := canonicalJSON(t, string(tool.Schema())), canonicalJSON(t, deploySchemaGolden); got != want {
		t.Fatalf("schema mismatch:\n got: %s\nwant: %s", got, want)
	}
	if tool.Name() != "deploy" || tool.Description() != "Deploy a service." {
		t.Fatalf("name/description = %q %q", tool.Name(), tool.Description())
	}
}

func canonicalJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", s, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTypedSchemaCached(t *testing.T) {
	tool := Typed("t", "d", func(ctx context.Context, args deployArgs) (gage.ToolResult, error) {
		return gage.TextResult("", "ok"), nil
	})
	a := tool.Schema()
	b := tool.Schema()
	if &a[0] != &b[0] {
		t.Fatal("schema should be computed once and reused")
	}
}

func TestTypedExecute(t *testing.T) {
	type sumArgs struct {
		A int  `json:"a"`
		B int  `json:"b"`
		C *int `json:"c"`
	}
	tool := Typed("sum", "Add numbers.", func(ctx context.Context, args sumArgs) (gage.ToolResult, error) {
		total := args.A + args.B
		if args.C != nil {
			total += *args.C
		}
		return gage.TextResult("", strings.Repeat("x", total)), nil
	})
	res := run(t, tool, `{"a":1,"b":2,"c":3}`)
	if res.Text() != "xxxxxx" {
		t.Fatalf("execute = %q", res.Text())
	}
	// Empty input decodes into the zero value.
	res = run(t, tool, ``)
	if res.IsError || res.Text() != "" {
		t.Fatalf("empty input = %+v", res)
	}
}

func TestTypedUnmarshalErrorNamesField(t *testing.T) {
	type args struct {
		Count int `json:"count"`
	}
	tool := Typed("counter", "Count.", func(ctx context.Context, a args) (gage.ToolResult, error) {
		t.Fatal("handler must not run on bad input")
		return gage.ToolResult{}, nil
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"count":"three"}`))
	if err != nil {
		t.Fatalf("bad input must be a model-visible result, not a Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text(), `"count"`) {
		t.Fatalf("expected error naming field count, got %q", res.Text())
	}

	res, err = tool.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "invalid arguments") {
		t.Fatalf("expected invalid-arguments error, got %q", res.Text())
	}
}

func TestTypedWithMetadata(t *testing.T) {
	type args struct{}
	tool := TypedWithMetadata("m", "meta tool", gage.ToolMetadata{ReadOnly: true, Network: true},
		func(ctx context.Context, a args) (gage.ToolResult, error) {
			return gage.TextResult("", "ok"), nil
		})
	meta := gage.MetadataOf(tool)
	if !meta.ReadOnly || !meta.Network {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestTypedPanicsOnNonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-struct type parameter")
		}
	}()
	Typed("bad", "bad", func(ctx context.Context, args string) (gage.ToolResult, error) {
		return gage.ToolResult{}, nil
	})
}

func TestTypedEmbeddedStructFlattens(t *testing.T) {
	type Common struct {
		ID string `json:"id"`
	}
	type args struct {
		Common
		Name string `json:"name"`
	}
	tool := Typed("emb", "embedded", func(ctx context.Context, a args) (gage.ToolResult, error) {
		return gage.TextResult("", a.ID+"/"+a.Name), nil
	})
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if _, ok := props["id"]; !ok {
		t.Fatalf("embedded field not flattened: %v", props)
	}
	res := run(t, tool, `{"id":"a","name":"b"}`)
	if res.Text() != "a/b" {
		t.Fatalf("execute = %q", res.Text())
	}
}
