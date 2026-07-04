package jsonschema

import (
	"encoding/json"
	"testing"
)

func TestObject(t *testing.T) {
	raw := Object(map[string]Property{
		"path":  Str("file path"),
		"limit": Int("max lines"),
		"mode":  Enum("mode", "r", "w"),
		"tags":  Array("tags", Str("a tag")),
	}, "path")

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" {
		t.Fatalf("type = %v", m["type"])
	}
	req, ok := m["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "path" {
		t.Fatalf("required = %v", m["required"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || len(props) != 4 {
		t.Fatalf("properties = %v", m["properties"])
	}
}

func TestObjectNoRequired(t *testing.T) {
	raw := Object(nil)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if req, ok := m["required"].([]any); !ok || len(req) != 0 {
		t.Fatalf("required = %v", m["required"])
	}
}
