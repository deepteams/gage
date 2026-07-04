package jsonschema

import (
	"encoding/json"
	"strings"
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
	if _, present := m["additionalProperties"]; present {
		t.Fatal("Object must not emit additionalProperties")
	}
}

func marshalProp(t *testing.T, p Property) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStringEnumMarshal(t *testing.T) {
	got := marshalProp(t, Enum("mode", "r", "w"))
	want := `{"type":"string","description":"mode","enum":["r","w"]}`
	if got != want {
		t.Fatalf("enum = %s, want %s", got, want)
	}
}

func TestEnumValuesMarshal(t *testing.T) {
	got := marshalProp(t, EnumOf("level", "integer", 1, 2, 3))
	want := `{"type":"integer","description":"level","enum":[1,2,3]}`
	if got != want {
		t.Fatalf("enum values = %s, want %s", got, want)
	}
	// EnumValues wins over Enum when both are set.
	got = marshalProp(t, Property{Type: "string", Enum: []string{"a"}, EnumValues: []any{"b"}})
	want = `{"type":"string","enum":["b"]}`
	if got != want {
		t.Fatalf("enum precedence = %s, want %s", got, want)
	}
}

func TestNumericAndStringConstraints(t *testing.T) {
	p := Num("score")
	p.Minimum = Ptr(0.0)
	p.Maximum = Ptr(1.0)
	got := marshalProp(t, p)
	want := `{"type":"number","description":"score","minimum":0,"maximum":1}`
	if got != want {
		t.Fatalf("numeric = %s, want %s", got, want)
	}

	s := Str("id")
	s.MinLength = Ptr(1)
	s.MaxLength = Ptr(8)
	s.Pattern = "^[a-z]+$"
	got = marshalProp(t, s)
	want = `{"type":"string","description":"id","minLength":1,"maxLength":8,"pattern":"^[a-z]+$"}`
	if got != want {
		t.Fatalf("string constraints = %s, want %s", got, want)
	}
}

func TestObjectPropNested(t *testing.T) {
	p := ObjectProp(map[string]Property{
		"host": Str("hostname"),
		"port": Int("port"),
	}, "host")
	got := marshalProp(t, p)
	want := `{"type":"object","properties":{"host":{"type":"string","description":"hostname"},"port":{"type":"integer","description":"port"}},"required":["host"]}`
	if got != want {
		t.Fatalf("nested object = %s, want %s", got, want)
	}
}

func TestObjectPropAdditionalProperties(t *testing.T) {
	p := ObjectProp(map[string]Property{"a": Str("a")}, "a")
	p.AdditionalProperties = Ptr(false)
	got := marshalProp(t, p)
	if !strings.Contains(got, `"additionalProperties":false`) {
		t.Fatalf("missing additionalProperties: %s", got)
	}
}

func TestAnyOfMarshal(t *testing.T) {
	p := Property{Description: "id or name", AnyOf: []Property{
		{Type: "string"},
		{Type: "integer"},
	}}
	got := marshalProp(t, p)
	want := `{"description":"id or name","anyOf":[{"type":"string"},{"type":"integer"}]}`
	if got != want {
		t.Fatalf("anyOf = %s, want %s", got, want)
	}
}

func TestStrictObject(t *testing.T) {
	raw := StrictObject(map[string]Property{"q": Str("query")}, "q")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if ap, ok := m["additionalProperties"].(bool); !ok || ap {
		t.Fatalf("additionalProperties = %v", m["additionalProperties"])
	}
	if req, ok := m["required"].([]any); !ok || len(req) != 1 || req[0] != "q" {
		t.Fatalf("required = %v", m["required"])
	}
}
