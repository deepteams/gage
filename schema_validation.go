package gage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
)

// ValidateToolInput validates raw tool input against the JSON Schema subset
// gage emits for tools. Unsupported schema keywords are ignored deliberately:
// the goal is a portable safety net before Execute, not a full JSON Schema
// implementation.
func ValidateToolInput(schema JSONSchema, input json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var root schemaNode
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return root.validate("$", value)
}

type schemaNode struct {
	Type                 any                   `json:"type,omitempty"`
	Properties           map[string]schemaNode `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	AdditionalProperties any                   `json:"additionalProperties,omitempty"`
	Enum                 []json.RawMessage     `json:"enum,omitempty"`
	Items                *schemaNode           `json:"items,omitempty"`
	AnyOf                []schemaNode          `json:"anyOf,omitempty"`
	Minimum              *float64              `json:"minimum,omitempty"`
	Maximum              *float64              `json:"maximum,omitempty"`
	MinLength            *int                  `json:"minLength,omitempty"`
	MaxLength            *int                  `json:"maxLength,omitempty"`
	Pattern              string                `json:"pattern,omitempty"`
}

func (s schemaNode) validate(path string, v any) error {
	if len(s.AnyOf) > 0 {
		var last error
		for _, branch := range s.AnyOf {
			if err := branch.validate(path, v); err == nil {
				return nil
			} else {
				last = err
			}
		}
		if last != nil {
			return fmt.Errorf("%s does not match any allowed shape: %w", path, last)
		}
		return fmt.Errorf("%s does not match any allowed shape", path)
	}
	if len(s.Enum) > 0 && !enumContains(s.Enum, v) {
		return fmt.Errorf("%s is not one of the allowed values", path)
	}
	if typ := s.typeName(); typ != "" {
		if err := validateType(path, typ, v); err != nil {
			return err
		}
	}
	switch x := v.(type) {
	case map[string]any:
		if err := s.validateObject(path, x); err != nil {
			return err
		}
	case []any:
		if s.Items != nil {
			for i, item := range x {
				if err := s.Items.validate(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
					return err
				}
			}
		}
	case string:
		if s.MinLength != nil && len(x) < *s.MinLength {
			return fmt.Errorf("%s must be at least %d bytes", path, *s.MinLength)
		}
		if s.MaxLength != nil && len(x) > *s.MaxLength {
			return fmt.Errorf("%s must be at most %d bytes", path, *s.MaxLength)
		}
		if s.Pattern != "" {
			re, err := regexp.Compile(s.Pattern)
			if err != nil {
				return fmt.Errorf("invalid pattern at %s: %w", path, err)
			}
			if !re.MatchString(x) {
				return fmt.Errorf("%s does not match pattern %q", path, s.Pattern)
			}
		}
	case json.Number:
		if err := s.validateNumber(path, x); err != nil {
			return err
		}
	}
	return nil
}

func (s schemaNode) validateObject(path string, obj map[string]any) error {
	for _, name := range s.Required {
		if _, ok := obj[name]; !ok {
			return fmt.Errorf("%s.%s is required", path, name)
		}
	}
	for name, value := range obj {
		prop, ok := s.Properties[name]
		if !ok {
			if b, ok := s.AdditionalProperties.(bool); ok && !b {
				return fmt.Errorf("%s.%s is not allowed", path, name)
			}
			continue
		}
		if err := prop.validate(path+"."+name, value); err != nil {
			return err
		}
	}
	return nil
}

func (s schemaNode) validateNumber(path string, n json.Number) error {
	f, err := n.Float64()
	if err != nil {
		return fmt.Errorf("%s is not a valid number", path)
	}
	if s.Minimum != nil && f < *s.Minimum {
		return fmt.Errorf("%s must be >= %v", path, *s.Minimum)
	}
	if s.Maximum != nil && f > *s.Maximum {
		return fmt.Errorf("%s must be <= %v", path, *s.Maximum)
	}
	return nil
}

func (s schemaNode) typeName() string {
	switch t := s.Type.(type) {
	case string:
		return t
	case []any:
		if len(t) == 1 {
			if name, ok := t[0].(string); ok {
				return name
			}
		}
	}
	return ""
}

func validateType(path, typ string, v any) error {
	ok := false
	switch typ {
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "number":
		_, ok = v.(json.Number)
	case "integer":
		if n, isNum := v.(json.Number); isNum {
			f, err := n.Float64()
			ok = err == nil && math.Trunc(f) == f
		}
	default:
		ok = true
	}
	if !ok {
		return fmt.Errorf("%s must be %s", path, typ)
	}
	return nil
}

func enumContains(values []json.RawMessage, v any) bool {
	canonical, err := canonicalJSON(v)
	if err != nil {
		return false
	}
	for _, raw := range values {
		var ev any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&ev); err != nil {
			continue
		}
		want, err := canonicalJSON(ev)
		if err == nil && bytes.Equal(canonical, want) {
			return true
		}
	}
	return false
}

func canonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
