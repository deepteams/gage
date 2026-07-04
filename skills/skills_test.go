package skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadDir(t *testing.T) {
	set, err := LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 2 {
		t.Fatalf("expected 2 skills, got %d: %v", set.Len(), names(set))
	}
	sk, ok := set.Get("pdf-fill")
	if !ok {
		t.Fatal("pdf-fill not loaded")
	}
	if sk.Description != "Fill PDF forms from structured data." {
		t.Fatalf("description = %q", sk.Description)
	}
	body, err := sk.Body()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "pdftk") || strings.Contains(body, "name: pdf-fill") {
		t.Fatalf("body = %q", body)
	}
}

func TestLoadDirFallbackName(t *testing.T) {
	set, _ := LoadDir("testdata")
	// Folder without frontmatter falls back to the directory name.
	if _, ok := set.Get("no-frontmatter"); !ok {
		t.Fatalf("fallback name missing: %v", names(set))
	}
}

func TestSystemPrompt(t *testing.T) {
	set, _ := LoadDir("testdata")
	sp := set.SystemPrompt()
	if !strings.Contains(sp, "pdf-fill: Fill PDF forms") {
		t.Fatalf("system prompt = %q", sp)
	}
}

func TestSkillTool(t *testing.T) {
	set, _ := LoadDir("testdata")
	tool := NewTool(set)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"pdf-fill"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text(), "# Skill: pdf-fill") || !strings.Contains(res.Text(), "pdftk") {
		t.Fatalf("skill tool output = %q", res.Text())
	}

	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if !res.IsError {
		t.Fatal("expected error for unknown skill")
	}

	// Schema enumerates skill names.
	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	json.Unmarshal(tool.Schema(), &schema)
	if len(schema.Properties.Name.Enum) != 2 {
		t.Fatalf("enum = %v", schema.Properties.Name.Enum)
	}
}

func names(s *Set) []string {
	var out []string
	for _, sk := range s.List() {
		out = append(out, sk.Name)
	}
	return out
}
