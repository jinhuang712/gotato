package gotato

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// NewFuncTool exposes an ordinary Go function as a Tool. The input type must be
// a struct; its exported fields become the JSON Schema properties Core
// validates before the executor runs.
//
// Field rules follow encoding/json: the `json` tag renames a field, `-` skips
// it, and `omitempty` marks it optional. A pointer field is optional too. The
// `description` tag documents a field and the `enum` tag lists its allowed
// string values, comma separated.
//
// The returned Tool never sees provider objects: Core validates the arguments,
// unmarshals them into In, and turns a returned error into a failed Tool
// Result.
func NewFuncTool[In any, Out any](name, description string, fn func(context.Context, In) (Out, error)) (Tool, error) {
	if fn == nil {
		return nil, runtimeError(ErrInvalidArgument, "NewFuncTool", "function is nil", nil)
	}
	return NewFuncToolWithProgress(name, description, func(ctx context.Context, in In, _ ToolProgress) (Out, error) {
		return fn(ctx, in)
	})
}

// NewFuncToolWithProgress is NewFuncTool for a function that reports bounded
// progress while it runs.
func NewFuncToolWithProgress[In any, Out any](name, description string, fn func(context.Context, In, ToolProgress) (Out, error)) (Tool, error) {
	if strings.TrimSpace(name) == "" {
		return nil, runtimeError(ErrInvalidArgument, "NewFuncTool", "Tool name is empty", nil)
	}
	if fn == nil {
		return nil, runtimeError(ErrInvalidArgument, "NewFuncTool", "function is nil", nil)
	}
	schema, err := inputSchemaFor(reflect.TypeFor[In]())
	if err != nil {
		return nil, runtimeError(ErrInvalidArgument, "NewFuncTool", "cannot derive InputSchema for "+name+": "+err.Error(), err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, runtimeError(ErrInvalidArgument, "NewFuncTool", "cannot encode InputSchema for "+name, err)
	}
	return &funcTool[In, Out]{
		spec: ToolSpec{ID: name, Name: name, Description: description, InputSchema: encoded},
		fn:   fn,
	}, nil
}

// WithFunc registers an ordinary Go function as a Tool during construction. A
// type the schema generator cannot describe fails NewAgent instead of failing
// at the first Model request.
func WithFunc[In any, Out any](name, description string, fn func(context.Context, In) (Out, error)) Option {
	return func(c *agentConfig) error {
		tool, err := NewFuncTool(name, description, fn)
		if err != nil {
			return err
		}
		c.tools = append(c.tools, tool)
		return nil
	}
}

type funcTool[In any, Out any] struct {
	spec ToolSpec
	fn   func(context.Context, In, ToolProgress) (Out, error)
}

func (t *funcTool[In, Out]) Spec() ToolSpec { return cloneToolSpec(t.spec) }

func (t *funcTool[In, Out]) Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error) {
	var input In
	if len(use.ArgumentsJSON) > 0 {
		if err := json.Unmarshal(use.ArgumentsJSON, &input); err != nil {
			return ToolResult{}, runtimeError(ErrToolArgumentFailure, "Tool", "cannot decode arguments for "+t.spec.ID, err)
		}
	}
	output, err := t.fn(ctx, input, progress)
	if err != nil {
		return ToolResult{}, err
	}
	return toolResultFor(output)
}

func toolResultFor(output any) (ToolResult, error) {
	switch typed := output.(type) {
	case ToolResult:
		return typed, nil
	case *ToolResult:
		if typed == nil {
			return ToolResult{Status: ToolResultOK}, nil
		}
		return *typed, nil
	case string:
		return ToolResult{Status: ToolResultOK, Content: []ContentPart{{Kind: ContentText, Text: typed}}}, nil
	case []ContentPart:
		return ToolResult{Status: ToolResultOK, Content: cloneContent(typed)}, nil
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ToolResult{}, runtimeError(ErrToolExecutionFailure, "Tool", "cannot encode Tool output", err)
	}
	return ToolResult{Status: ToolResultOK, Content: []ContentPart{{Kind: ContentJSON, Text: string(encoded)}}}, nil
}

// inputSchemaFor derives the object Schema Core validates arguments against.
// The supported subset matches validateSchemaValue: type, properties,
// required, items, additionalProperties, enum, and description.
func inputSchemaFor(typ reflect.Type) (map[string]any, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input type must be a struct, found %s", typ.Kind())
	}
	return structSchema(typ, map[reflect.Type]bool{})
}

func structSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	if visiting[typ] {
		return nil, fmt.Errorf("recursive struct type %s", typ.String())
	}
	visiting[typ] = true
	defer delete(visiting, typ)

	properties := map[string]any{}
	required := []any{}
	if err := collectFields(typ, visiting, properties, &required); err != nil {
		return nil, err
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, nil
}

func collectFields(typ reflect.Type, visiting map[reflect.Type]bool, properties map[string]any, required *[]any) error {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				if err := collectFields(embedded, visiting, properties, required); err != nil {
					return err
				}
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		if _, duplicate := properties[name]; duplicate {
			return fmt.Errorf("duplicate property name %q", name)
		}
		child, err := valueSchema(field.Type, visiting)
		if err != nil {
			return fmt.Errorf("field %s: %w", field.Name, err)
		}
		if description := field.Tag.Get("description"); description != "" {
			child["description"] = description
		}
		if enum := field.Tag.Get("enum"); enum != "" {
			values := make([]any, 0)
			for _, value := range strings.Split(enum, ",") {
				value = strings.TrimSpace(value)
				if value != "" {
					values = append(values, value)
				}
			}
			if len(values) > 0 {
				child["enum"] = values
			}
		}
		properties[name] = child
		optional := field.Type.Kind() == reflect.Pointer || strings.Contains(options, "omitempty")
		if !optional {
			*required = append(*required, name)
		}
	}
	return nil
}

func valueSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}, nil
	}
	// A type that encodes itself as a JSON string has no useful field
	// structure. Describing it as an object would reject every valid value.
	if typ == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	if typ.Kind() != reflect.String && reflect.PointerTo(typ).Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		return map[string]any{"type": "string"}, nil
	}
	switch typ.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Interface:
		if typ.NumMethod() == 0 {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("unsupported interface type %s", typ.String())
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}, nil
		}
		items, err := valueSchema(typ.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key must be a string, found %s", typ.Key().Kind())
		}
		values, err := valueSchema(typ.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		return structSchema(typ, visiting)
	default:
		return nil, fmt.Errorf("unsupported type %s", typ.String())
	}
}
