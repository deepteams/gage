// Package jsonschema provides small helpers to build JSON Schema documents for
// tool parameters without pulling in a full schema library.
package jsonschema

import "encoding/json"

// Property describes a single schema node (an object property, array item,
// anyOf branch, ...).
type Property struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`

	// Enum constrains a string property to a fixed set of values. For enums of
	// other types use EnumValues; when both are set, EnumValues wins.
	Enum []string `json:"-"`
	// EnumValues constrains the property to a fixed set of values of any type.
	EnumValues []any `json:"enum,omitempty"`

	Items      *Property           `json:"items,omitempty"`      // for type "array"
	Properties map[string]Property `json:"properties,omitempty"` // for type "object"
	// Required lists the required property names of a nested "object" property.
	Required []string `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`

	// Numeric constraints (types "integer" and "number").
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`

	// String constraints.
	MinLength *int   `json:"minLength,omitempty"`
	MaxLength *int   `json:"maxLength,omitempty"`
	Pattern   string `json:"pattern,omitempty"`

	// AdditionalProperties, when non-nil, is emitted for "object" properties.
	// Set it to false (see Ptr) to reject unknown keys.
	AdditionalProperties *bool `json:"additionalProperties,omitempty"`

	// AnyOf declares alternative schemas; usually used without Type.
	AnyOf []Property `json:"anyOf,omitempty"`
}

// MarshalJSON emits the string Enum through the "enum" key unless EnumValues
// is set, which takes precedence.
func (p Property) MarshalJSON() ([]byte, error) {
	type alias Property // drop methods to avoid recursion
	a := alias(p)
	if len(a.EnumValues) == 0 && len(p.Enum) > 0 {
		a.EnumValues = make([]any, len(p.Enum))
		for i, v := range p.Enum {
			a.EnumValues[i] = v
		}
	}
	return json.Marshal(a)
}

// Ptr returns a pointer to v; a convenience for the pointer-typed constraint
// fields (Minimum, MaxLength, AdditionalProperties, ...).
func Ptr[T any](v T) *T { return &v }

// Object builds an object schema with the given properties and required keys.
// The returned value is a marshaled JSON Schema suitable for gage.ToolSchema.
func Object(props map[string]Property, required ...string) json.RawMessage {
	return marshalObject(props, required, nil)
}

// StrictObject is Object with "additionalProperties": false, so inputs with
// unknown keys are rejected by schema-validating providers.
func StrictObject(props map[string]Property, required ...string) json.RawMessage {
	f := false
	return marshalObject(props, required, &f)
}

func marshalObject(props map[string]Property, required []string, additional *bool) json.RawMessage {
	if props == nil {
		props = map[string]Property{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	if additional != nil {
		schema["additionalProperties"] = *additional
	}
	b, err := json.Marshal(schema)
	if err != nil {
		// The inputs are always marshalable structs/maps; a failure here is a
		// programming error, so fall back to an empty object schema.
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return b
}

// ObjectProp builds a nested object property with the given properties and
// required keys, for use inside another schema.
func ObjectProp(props map[string]Property, required ...string) Property {
	return Property{Type: "object", Properties: props, Required: required}
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

// EnumOf builds a property of the given type constrained to a set of values of
// any type (e.g. EnumOf("priority", "integer", 1, 2, 3)).
func EnumOf(desc, typ string, values ...any) Property {
	return Property{Type: typ, Description: desc, EnumValues: values}
}

// Array builds an array property whose items match the given element schema.
func Array(desc string, items Property) Property {
	return Property{Type: "array", Description: desc, Items: &items}
}
