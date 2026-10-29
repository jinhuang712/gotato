package gotato

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func validateToolSpec(spec ToolSpec) error {
	if strings.TrimSpace(spec.ID) == "" {
		return runtimeError(CodeInvalidArgument, "ToolSpec", "Tool ID is empty", nil)
	}
	if !spec.Effect.Valid() {
		return runtimeError(CodeInvalidArgument, "ToolSpec", "unknown Effect "+string(spec.Effect), nil)
	}
	if spec.InputSchema != nil {
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			return runtimeError(CodeInvalidArgument, "ToolSpec", "invalid InputSchema", err)
		}
		if schema == nil {
			return runtimeError(CodeInvalidArgument, "ToolSpec", "InputSchema must be an object", nil)
		}
	}
	return nil
}

// toolEntry is a Tool as the registry holds it: with the Spec it read once,
// when the Tool entered (see intake).
type toolEntry struct {
	tool Tool
	spec ToolSpec
}

// intake is the one way a Tool enters the registry, whether it comes from
// WithTools, a ToolSource or a ToolSet. It reads each Tool's Spec once, checks
// it and keeps it, so nothing after intake calls Spec again or fails on a Tool.
// what names the source in an error. held are the Tools the source's IDs may
// not repeat, and check, when it is not nil, refuses the names a source may not
// take. A Tool that is nil, whose Spec panics or is invalid, or whose ID is
// taken fails the whole source: the caller never keeps part of it.
func intake(what string, tools []Tool, held []toolEntry, check func(ToolSpec) error) ([]toolEntry, error) {
	taken := make(map[string]bool, len(held)+len(tools))
	for _, entry := range held {
		taken[entry.spec.ID] = true
	}
	entries := make([]toolEntry, 0, len(tools))
	err := callHost("Tool.Spec", func() error {
		for _, tool := range tools {
			spec := cloneToolSpec(tool.Spec())
			if err := validateToolSpec(spec); err != nil {
				return err
			}
			if check != nil {
				if err := check(spec); err != nil {
					return err
				}
			}
			// [CONTRACT] a Tool ID names one Tool of the root namespace, or of its ToolSet.
			if taken[spec.ID] {
				return runtimeError(CodeInvalidArgument, what, what+" has a duplicate Tool ID: "+spec.ID, nil)
			}
			taken[spec.ID] = true
			entries = append(entries, toolEntry{tool: tool, spec: spec})
		}
		return nil
	})
	// [LIVENESS] a nil Tool, or a Spec that panics, fails its source instead of the process.
	if panicked, ok := err.(*panicError); ok {
		return nil, runtimeError(CodeInvalidArgument, what, panicked.Public(), err)
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// validateToolSchema checks Tool arguments against the supported JSON Schema
// subset. Core validates a JSON value rather than a Go struct, so `omitempty`
// on a Go field never changes what `required` means here.
//
// Enforced:
//
//	type                  object, array, string, boolean, number, integer, null
//	properties            recursive validation of declared members
//	required              declared member must be present
//	additionalProperties  false rejects undeclared members
//
// Carried to the Model but not enforced by Core: description, enum, format,
// items, and every other keyword. An unknown keyword is ignored rather than
// rejected, so a richer provider Schema still passes through unchanged.
func validateToolSchema(schema, arguments []byte) error {
	if len(schema) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("invalid InputSchema: %w", err)
	}
	var value any
	if err := json.Unmarshal(arguments, &value); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return validateSchemaValue(root, value, "arguments")
}

func validateSchemaValue(schema map[string]any, value any, path string) error {
	if typ, ok := schema["type"].(string); ok && !schemaTypeMatches(typ, value) {
		return fmt.Errorf("%s must be %s", path, typ)
	}
	if allowed, ok := schema["enum"].([]any); ok && len(allowed) > 0 {
		if !enumAllows(allowed, value) {
			// The allowed values come from the declared Schema, so naming
			// them is safe. The received value is provider payload and stays
			// out of the message.
			return fmt.Errorf("%s must be one of: %s", path, describeEnum(allowed))
		}
	}
	if required, ok := schema["required"].([]any); ok {
		object, isObject := value.(map[string]any)
		if !isObject {
			return fmt.Errorf("%s must be object", path)
		}
		for _, raw := range required {
			name, isString := raw.(string)
			if isString {
				if _, exists := object[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	if object, ok := value.(map[string]any); ok {
		for name, rawSchema := range properties {
			if raw, exists := object[name]; exists {
				if child, ok := rawSchema.(map[string]any); ok {
					if err := validateSchemaValue(child, raw, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, declared := properties[name]; !declared {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
			}
		}
	}
	return nil
}

// enumAllows compares a decoded JSON value against the declared enum. Both
// sides come from encoding/json, so the dynamic types are limited to string,
// float64, bool, and nil.
func enumAllows(allowed []any, value any) bool {
	for _, candidate := range allowed {
		switch expected := candidate.(type) {
		case string:
			if actual, ok := value.(string); ok && actual == expected {
				return true
			}
		case float64:
			if actual, ok := value.(float64); ok && actual == expected {
				return true
			}
		case bool:
			if actual, ok := value.(bool); ok && actual == expected {
				return true
			}
		case nil:
			if value == nil {
				return true
			}
		}
	}
	return false
}

func describeEnum(allowed []any) string {
	names := make([]string, 0, len(allowed))
	for _, candidate := range allowed {
		names = append(names, fmt.Sprintf("%v", candidate))
	}
	return strings.Join(names, ", ")
}

func schemaTypeMatches(typ string, value any) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == math.Trunc(number)
	case "null":
		return value == nil
	default:
		return true
	}
}
