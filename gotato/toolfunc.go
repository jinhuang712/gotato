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
// it, and `omitempty` or `omitzero` marks it optional. A pointer field is
// optional too. The
// `description` tag documents a field and the `enum` tag lists its allowed
// string values, comma separated.
//
// The returned Tool never sees provider objects: Core validates the arguments,
// unmarshals them into In, and turns a returned error into a failed Tool
// Result.
func NewFuncTool[In any, Out any](name, description string, fn func(context.Context, In) (Out, error)) (Tool, error) {
	if fn == nil {
		return nil, runtimeError(CodeInvalidArgument, "NewFuncTool", "function is nil", nil)
	}
	return NewFuncToolWithProgress(name, description, func(ctx context.Context, in In, _ ToolProgressFunc) (Out, error) {
		return fn(ctx, in)
	})
}

// NewFuncToolWithProgress is NewFuncTool for a function that reports bounded
// progress while it runs.
func NewFuncToolWithProgress[In any, Out any](name, description string, fn func(context.Context, In, ToolProgressFunc) (Out, error)) (Tool, error) {
	if strings.TrimSpace(name) == "" {
		return nil, runtimeError(CodeInvalidArgument, "NewFuncTool", "Tool name is empty", nil)
	}
	if fn == nil {
		return nil, runtimeError(CodeInvalidArgument, "NewFuncTool", "function is nil", nil)
	}
	schema, err := inputSchemaFor(reflect.TypeFor[In]())
	if err != nil {
		return nil, runtimeError(CodeInvalidArgument, "NewFuncTool", "cannot derive InputSchema for "+name+": "+err.Error(), err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, runtimeError(CodeInvalidArgument, "NewFuncTool", "cannot encode InputSchema for "+name, err)
	}
	return &funcTool[In, Out]{
		spec: ToolSpec{ID: name, Name: name, Description: description, InputSchema: encoded},
		fn:   fn,
	}, nil
}

type funcTool[In any, Out any] struct {
	spec ToolSpec
	fn   func(context.Context, In, ToolProgressFunc) (Out, error)
}

func (t *funcTool[In, Out]) Spec() ToolSpec { return cloneToolSpec(t.spec) }

func (t *funcTool[In, Out]) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	var input In
	if len(use.Arguments) > 0 {
		if err := json.Unmarshal(use.Arguments, &input); err != nil {
			return ToolResult{}, runtimeError(CodeToolArgumentFailure, "Tool", "cannot decode arguments for "+t.spec.ID, err)
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
			return ToolResult{Status: ToolResultStatusOK}, nil
		}
		return *typed, nil
	case string:
		return ToolResult{Status: ToolResultStatusOK, Content: []ContentPart{{Kind: ContentKindText, Text: typed}}}, nil
	case []ContentPart:
		return ToolResult{Status: ToolResultStatusOK, Content: cloneParts(typed)}, nil
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ToolResult{}, runtimeError(CodeToolExecutionFailure, "Tool", "cannot encode Tool output", err)
	}
	return ToolResult{Status: ToolResultStatusOK, Content: []ContentPart{{Kind: ContentKindJSON, Text: string(encoded)}}}, nil
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
		if err := collectField(typ.Field(i), visiting, properties, required); err != nil {
			return err
		}
	}
	return nil
}

// collectField adds one struct field to properties and required, following encoding/json.
func collectField(field reflect.StructField, visiting map[reflect.Type]bool, properties map[string]any, required *[]any) error {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return nil
	}
	name, options, _ := strings.Cut(tag, ",")
	if err := checkJSONOptions(options, field.Name); err != nil {
		return err
	}
	if embedded, ok := embeddedStruct(field, name); ok {
		// encoding/json promotes the exported fields of an embedded
		// struct even when the embedded type itself is unexported, so
		// traverse it before the exported-field check.
		return collectEmbedded(embedded, visiting, properties, required)
	}
	if !field.IsExported() {
		return nil
	}
	if name == "" {
		name = field.Name
	}
	if _, duplicate := properties[name]; duplicate {
		return fmt.Errorf("duplicate property name %q", name)
	}
	child, err := fieldSchema(field, visiting)
	if err != nil {
		return err
	}
	properties[name] = child
	if !fieldOptional(field, options) {
		*required = append(*required, name)
	}
	return nil
}

// checkJSONOptions rejects a json tag option the generated Schema cannot represent.
func checkJSONOptions(options, fieldName string) error {
	for option := range strings.SplitSeq(options, ",") {
		switch option {
		case "", "omitempty", "omitzero":
		default:
			// encoding/json options other than omitempty and omitzero (notably
			// ",string") change how a field is encoded but have no
			// representation in the generated Schema. Reject them at
			// construction rather than emit a Schema the decoder rejects.
			return fmt.Errorf("unsupported json option %q on field %s", option, fieldName)
		}
	}
	return nil
}

// embeddedStruct returns the struct type of an untagged embedded field, if it is one.
func embeddedStruct(field reflect.StructField, name string) (reflect.Type, bool) {
	if !field.Anonymous || name != "" {
		return nil, false
	}
	embedded := field.Type
	for embedded.Kind() == reflect.Pointer {
		embedded = embedded.Elem()
	}
	return embedded, embedded.Kind() == reflect.Struct
}

// collectEmbedded collects the promoted fields of an embedded struct, failing on recursion.
func collectEmbedded(embedded reflect.Type, visiting map[reflect.Type]bool, properties map[string]any, required *[]any) error {
	if visiting[embedded] {
		return fmt.Errorf("recursive struct type %s", embedded.String())
	}
	visiting[embedded] = true
	err := collectFields(embedded, visiting, properties, required)
	delete(visiting, embedded)
	return err
}

// fieldSchema derives a field's Schema with its description and enum tags.
func fieldSchema(field reflect.StructField, visiting map[reflect.Type]bool) (map[string]any, error) {
	child, err := valueSchema(field.Type, visiting)
	if err != nil {
		return nil, fmt.Errorf("field %s: %w", field.Name, err)
	}
	if description := field.Tag.Get("description"); description != "" {
		child["description"] = description
	}
	if values := enumValues(field.Tag.Get("enum")); len(values) > 0 {
		child["enum"] = values
	}
	return child, nil
}

// enumValues splits an enum tag into its non-empty, trimmed values.
func enumValues(enum string) []any {
	if enum == "" {
		return nil
	}
	values := make([]any, 0)
	for value := range strings.SplitSeq(enum, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

// fieldOptional reports whether a field may be left out of the arguments.
func fieldOptional(field reflect.StructField, options string) bool {
	return field.Type.Kind() == reflect.Pointer || strings.Contains(options, "omitempty") || strings.Contains(options, "omitzero")
}

func valueSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	// A named slice or map can be recursive (`type List []List`). Track it
	// for the duration of the element walk so re-entry fails instead of
	// recursing until the stack overflows. structSchema tracks structs the
	// same way.
	switch typ.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		if visiting[typ] {
			return nil, fmt.Errorf("recursive type %s", typ.String())
		}
		visiting[typ] = true
		defer delete(visiting, typ)
	}
	if schema, ok := encodedSchema(typ); ok {
		return schema, nil
	}
	return kindSchema(typ, visiting)
}

// encodedSchema is the Schema of a type that encoding/json encodes in a form
// of its own rather than by its kind.
func encodedSchema(typ reflect.Type) (map[string]any, bool) {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}, true
	}
	// A type that encodes itself as a JSON string has no useful field
	// structure. Describing it as an object would reject every valid value.
	if typ == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}, true
	}
	if typ.Kind() != reflect.String && reflect.PointerTo(typ).Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		return map[string]any{"type": "string"}, true
	}
	return nil, false
}

// kindSchema is the Schema of a type by its kind.
func kindSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
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
		return listSchema(typ, visiting)
	case reflect.Map:
		return mapSchema(typ, visiting)
	case reflect.Struct:
		return structSchema(typ, visiting)
	default:
		return nil, fmt.Errorf("unsupported type %s", typ.String())
	}
}

// listSchema is the Schema of a slice or an array.
func listSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	if typ.Elem().Kind() == reflect.Uint8 {
		// encoding/json encodes []byte as a base64 string but [N]byte as
		// an array of numbers, so a fixed-size byte array is not a string.
		if typ.Kind() == reflect.Array {
			return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, nil
		}
		return map[string]any{"type": "string"}, nil
	}
	items, err := valueSchema(typ.Elem(), visiting)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "array", "items": items}, nil
}

// mapSchema is the Schema of a map, whose key must be a string.
func mapSchema(typ reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	if typ.Key().Kind() != reflect.String {
		return nil, fmt.Errorf("map key must be a string, found %s", typ.Key().Kind())
	}
	values, err := valueSchema(typ.Elem(), visiting)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "object", "additionalProperties": values}, nil
}
