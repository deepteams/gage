package tools

import (
	"context"
	"encoding/json"

	"github.com/deepteams/gage"
	"github.com/deepteams/gage/internal/jsonschema"
)

// Func builds a gage.Tool from a function and an explicit parameter schema.
func Func(name, description string, schema gage.JSONSchema, fn func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error)) gage.Tool {
	if len(schema) == 0 {
		schema = jsonschema.Object(nil)
	}
	return gage.ToolFunc{ToolName: name, Desc: description, Params: schema, Fn: fn}
}

// FuncWithMetadata builds a gage.Tool from a function and advisory metadata.
func FuncWithMetadata(name, description string, schema gage.JSONSchema, meta gage.ToolMetadata, fn func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error)) gage.Tool {
	if len(schema) == 0 {
		schema = jsonschema.Object(nil)
	}
	return gage.ToolFunc{ToolName: name, Desc: description, Params: schema, Meta: meta, Fn: fn}
}

// ToolFuncMust builds a gage.Tool from a function with a permissive empty-object
// schema. Use Func when you need to describe parameters.
func ToolFuncMust(name, description string, fn func(ctx context.Context, input json.RawMessage) (gage.ToolResult, error)) gage.Tool {
	return Func(name, description, nil, fn)
}
