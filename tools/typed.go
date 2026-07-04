package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/jsonschema"
)

// Typed builds a gage.Tool whose parameter schema is derived from the struct
// type T by reflection, and whose inputs are unmarshaled into T before the
// handler runs.
//
// Field mapping:
//   - `json` tags name the parameters; fields tagged `json:"-"` are skipped.
//   - `desc:"..."` tags become property descriptions.
//   - `enum:"a,b,c"` tags constrain string fields to the listed values.
//   - A field is required unless it is a pointer or its json tag carries
//     ",omitempty".
//   - Supported field types: string, bool, integers, floats, slices, arrays,
//     nested structs, map[string]X, pointers to any of these, json.RawMessage
//     and any/interface{} (both unconstrained). Anything else panics at
//     construction time.
//
// The top-level schema sets "additionalProperties": false. The schema is
// computed once at construction. Malformed input produces a model-visible
// error result naming the offending field, not a Go error.
func Typed[T any](name, description string, fn func(ctx context.Context, args T) (gage.ToolResult, error)) gage.Tool {
	return TypedWithMetadata(name, description, gage.ToolMetadata{}, fn)
}

// TypedWithMetadata is Typed with advisory tool metadata attached.
func TypedWithMetadata[T any](name, description string, meta gage.ToolMetadata, fn func(ctx context.Context, args T) (gage.ToolResult, error)) gage.Tool {
	schema := schemaOfStruct(reflect.TypeOf((*T)(nil)).Elem())
	return gage.ToolFunc{
		ToolName: name,
		Desc:     description,
		Params:   schema,
		Meta:     meta,
		Fn: func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error) {
			var args T
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			dec := json.NewDecoder(bytes.NewReader(input))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&args); err != nil {
				return gage.ErrorResult("", unmarshalErrorMessage(name, err)), nil
			}
			var extra any
			if err := dec.Decode(&extra); err != io.EOF {
				if err == nil {
					err = fmt.Errorf("multiple JSON values")
				}
				return gage.ErrorResult("", unmarshalErrorMessage(name, err)), nil
			}
			return fn(ctx, args)
		},
	}
}

func unmarshalErrorMessage(tool string, err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		field := te.Field
		if field == "" {
			field = "(arguments)"
		}
		return fmt.Sprintf("invalid arguments for tool %s: field %q: cannot decode JSON %s into %s", tool, field, te.Value, te.Type)
	}
	return fmt.Sprintf("invalid arguments for tool %s: %v", tool, err)
}

// schemaOfStruct builds the top-level object schema for a struct type. It
// panics on non-struct types and unsupported field types: Typed is called at
// program construction time, so these are programming errors.
func schemaOfStruct(t reflect.Type) gage.JSONSchema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("tools.Typed: type %s is not a struct", t))
	}
	props, required := structProps(t)
	return jsonschema.StrictObject(props, required...)
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

func structProps(t reflect.Type) (map[string]jsonschema.Property, []string) {
	props := map[string]jsonschema.Property{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, tagged := f.Tag.Lookup("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && tagged {
			continue
		}
		if f.Anonymous && !tagged && f.Type.Kind() == reflect.Struct {
			// Embedded struct without a json tag: flatten, matching encoding/json.
			embProps, embReq := structProps(f.Type)
			for k, v := range embProps {
				props[k] = v
			}
			required = append(required, embReq...)
			continue
		}
		if f.PkgPath != "" {
			continue // unexported
		}
		if name == "" {
			name = f.Name
		}
		prop := propFor(f.Type, f.Name)
		if desc := f.Tag.Get("desc"); desc != "" {
			prop.Description = desc
		}
		if enum := f.Tag.Get("enum"); enum != "" {
			if prop.Type != "string" {
				panic(fmt.Sprintf("tools.Typed: enum tag on non-string field %s.%s", t, f.Name))
			}
			prop.Enum = strings.Split(enum, ",")
		}
		optional := f.Type.Kind() == reflect.Pointer || hasOption(opts, "omitempty")
		if !optional {
			required = append(required, name)
		}
		props[name] = prop
	}
	return props, required
}

func hasOption(opts, want string) bool {
	for opts != "" {
		var o string
		o, opts, _ = strings.Cut(opts, ",")
		if o == want {
			return true
		}
	}
	return false
}

func propFor(t reflect.Type, fieldName string) jsonschema.Property {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		return jsonschema.Property{} // any JSON value
	}
	switch t.Kind() {
	case reflect.String:
		return jsonschema.Property{Type: "string"}
	case reflect.Bool:
		return jsonschema.Property{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return jsonschema.Property{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return jsonschema.Property{Type: "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// []byte marshals as a base64 string.
			return jsonschema.Property{Type: "string"}
		}
		item := propFor(t.Elem(), fieldName)
		return jsonschema.Property{Type: "array", Items: &item}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("tools.Typed: unsupported map key type %s in field %s", t.Key(), fieldName))
		}
		return jsonschema.Property{Type: "object"} // free-form values
	case reflect.Struct:
		props, required := structProps(t)
		return jsonschema.ObjectProp(props, required...)
	case reflect.Interface:
		return jsonschema.Property{} // any JSON value
	default:
		panic(fmt.Sprintf("tools.Typed: unsupported field type %s in field %s", t, fieldName))
	}
}
