// Package jsonschema provides small helpers to build JSON Schema documents for
// tool parameters without pulling in a full schema library.
package jsonschema

import "encoding/json"

// Property describes a single object property.
type Property struct {
	Type        string              `json:"type,omitempty"`
	Description string              `json:"description,omitempty"`
	Enum        []string            `json:"enum,omitempty"`
	Items       *Property           `json:"items,omitempty"`      // for type "array"
	Properties  map[string]Property `json:"properties,omitempty"` // for type "object"
	Default     any                 `json:"default,omitempty"`
}

// Object builds an object schema with the given properties and required keys.
// The returned value is a marshaled JSON Schema suitable for gage.ToolSchema.
func Object(props map[string]Property, required ...string) json.RawMessage {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	b, err := json.Marshal(schema)
	if err != nil {
		// The inputs are always marshalable structs/maps; a failure here is a
		// programming error, so fall back to an empty object schema.
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return b
}

// Str builds a string property with a description.
func Str(desc string) Property { return Property{Type: "string", Description: desc} }

// Int builds an integer property with a description.
func Int(desc string) Property { return Property{Type: "integer", Description: desc} }

// Bool builds a boolean property with a description.
func Bool(desc string) Property { return Property{Type: "boolean", Description: desc} }

// Num builds a number property with a description.
func Num(desc string) Property { return Property{Type: "number", Description: desc} }

// Enum builds a string property constrained to a set of values.
func Enum(desc string, values ...string) Property {
	return Property{Type: "string", Description: desc, Enum: values}
}

// Array builds an array property whose items match the given element schema.
func Array(desc string, items Property) Property {
	return Property{Type: "array", Description: desc, Items: &items}
}
