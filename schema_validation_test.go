package gage

import (
	"strings"
	"testing"

	"github.com/deepteams/gage/jsonschema"
)

func TestValidateToolInputStrictObject(t *testing.T) {
	schema := jsonschema.StrictObject(map[string]jsonschema.Property{
		"name": jsonschema.Str("name"),
		"age":  jsonschema.Int("age"),
	}, "name")
	if err := ValidateToolInput(schema, []byte(`{"name":"Ada","age":37}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolInput(schema, []byte(`{"age":37}`)); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("missing required err = %v", err)
	}
	if err := ValidateToolInput(schema, []byte(`{"name":"Ada","extra":true}`)); err == nil || !strings.Contains(err.Error(), "extra is not allowed") {
		t.Fatalf("additional property err = %v", err)
	}
	if err := ValidateToolInput(schema, []byte(`{"name":"Ada","age":"old"}`)); err == nil || !strings.Contains(err.Error(), "age must be integer") {
		t.Fatalf("type err = %v", err)
	}
}

func TestValidateToolInputEnumAndPattern(t *testing.T) {
	min := 2
	max := 4
	schema := jsonschema.StrictObject(map[string]jsonschema.Property{
		"mode": jsonschema.Enum("mode", "read", "write"),
		"id": {
			Type:      "string",
			MinLength: &min,
			MaxLength: &max,
			Pattern:   "^[a-z]+$",
		},
	}, "mode", "id")
	if err := ValidateToolInput(schema, []byte(`{"mode":"read","id":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolInput(schema, []byte(`{"mode":"exec","id":"abc"}`)); err == nil || !strings.Contains(err.Error(), "allowed values") {
		t.Fatalf("enum err = %v", err)
	}
	if err := ValidateToolInput(schema, []byte(`{"mode":"read","id":"A"}`)); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("pattern/length err = %v", err)
	}
}
