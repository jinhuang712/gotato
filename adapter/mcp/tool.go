package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool is one MCP tool as a gotato.Tool.
type tool struct {
	set  *ToolSet
	name string
	spec gotato.ToolSpec
}

func newTool(set *ToolSet, t *sdk.Tool) *tool {
	effect, ok := set.cfg.effects[t.Name]
	if !ok {
		effect = effectOf(t.Annotations)
	}
	return &tool{set: set, name: t.Name, spec: gotato.ToolSpec{
		ID:           t.Name,
		Name:         t.Name,
		Description:  t.Description,
		InputSchema:  slimSchema(t.Name, t.InputSchema),
		OutputSchema: slimSchema(t.Name, t.OutputSchema),
		Effect:       effect,
	}}
}

func (t *tool) Spec() gotato.ToolSpec { return t.spec }

// Execute calls the tool on the server. The Run's ctx cancels the call. A
// protocol or transport failure is an error; a result the server marks
// isError is a failed Tool Result the model reads.
func (t *tool) Execute(ctx context.Context, use gotato.ToolUse, progress gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	params := &sdk.CallToolParams{Name: t.name}
	if len(bytes.TrimSpace(use.Arguments)) > 0 {
		params.Arguments = json.RawMessage(use.Arguments)
	}
	if progress != nil {
		// A server sends progress only to a call that carries a token.
		token := fmt.Sprintf("gotato-%d", t.set.tokens.Add(1))
		params.SetProgressToken(token)
		t.set.progress.Store(token, progress)
		defer t.set.progress.Delete(token)
	}
	// A server that forgot the session (it restarted) answers 404 before it
	// runs anything, so the call is sent once more on a new session, even for
	// a tool that writes. A connection that closed during a call may have run
	// it, and that error is the caller's.
	for attempt := 0; ; attempt++ {
		session, err := t.set.connect(ctx)
		if err != nil {
			return gotato.ToolResult{}, err
		}
		result, err := session.CallTool(ctx, params)
		if err == nil {
			if result.NeedsInput() {
				return gotato.ToolResult{Status: gotato.ToolResultStatusFailed, SafeError: needsInput,
					Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: needsInput}}}, nil
			}
			return convertResult(result), nil
		}
		t.set.drop(session, err)
		if attempt == 0 && errors.Is(err, sdk.ErrSessionMissing) && ctx.Err() == nil {
			continue
		}
		return gotato.ToolResult{}, fmt.Errorf("mcp %s.%s: %w", t.set.name, t.name, err)
	}
}

// needsInput is what the model reads when a tool asks, in the middle of its
// call, for input that Gotato cannot give (elicitation, sampling, roots).
const needsInput = "the MCP tool asked for input in the middle of the call (elicitation or sampling), which this client cannot give, so the call ended; call it again with everything it needs in the arguments"

// effectOf maps a tool's annotations to its Effect, with the defaults of the
// MCP specification for a hint the server left out: not read-only, destructive
// and open world. A tool with no annotations is destructive.
//
//	readOnlyHint                       read
//	not destructive, closed world      write_local
//	not destructive, open world        write_external
//	anything else                      destructive
func effectOf(a *sdk.ToolAnnotations) gotato.ToolEffect {
	if a == nil {
		return gotato.ToolEffectDestructive
	}
	if a.ReadOnlyHint {
		return gotato.ToolEffectRead
	}
	if a.DestructiveHint == nil || *a.DestructiveHint {
		return gotato.ToolEffectDestructive
	}
	if a.OpenWorldHint == nil || *a.OpenWorldHint {
		return gotato.ToolEffectWriteExternal
	}
	return gotato.ToolEffectWriteLocal
}

// slimSchema drops what costs tokens in every request and tells the model
// nothing: $schema, $id and examples, and a title equal to the tool's name
// (or, on a property, to the property's own name). It returns the schema as
// JSON with sorted keys, so the bytes are the same on every Instance. A
// schema that is absent or not an object is returned as the empty value.
func slimSchema(toolName string, schema any) []byte {
	if schema == nil {
		return nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil || root == nil {
		return nil
	}
	slimNode(root, toolName)
	out, err := json.Marshal(root)
	if err != nil {
		return nil
	}
	return out
}

// slimNode walks the keywords that hold schemas. It never treats the name of
// a property as a keyword, so a property called "title" or "examples" stays.
func slimNode(node map[string]any, redundantTitle string) {
	delete(node, "$schema")
	delete(node, "$id")
	delete(node, "examples")
	if title, ok := node["title"].(string); ok && title == redundantTitle {
		delete(node, "title")
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if members, ok := node[key].(map[string]any); ok {
			for name, member := range members {
				if child, ok := member.(map[string]any); ok {
					title := ""
					if key == "properties" {
						title = name
					}
					slimNode(child, title)
				}
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "if", "then", "else", "contains", "propertyNames", "unevaluatedItems", "unevaluatedProperties"} {
		if child, ok := node[key].(map[string]any); ok {
			slimNode(child, "")
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := node[key].([]any); ok {
			for _, member := range list {
				if child, ok := member.(map[string]any); ok {
					slimNode(child, "")
				}
			}
		}
	}
	if list, ok := node["items"].([]any); ok {
		for _, member := range list {
			if child, ok := member.(map[string]any); ok {
				slimNode(child, "")
			}
		}
	}
}

// convertResult maps an MCP tool result to a Gotato Tool Result.
//
//   - text becomes a text Part; an image becomes an image Part.
//   - structuredContent becomes a JSON Part, and a text block that only repeats
//     it (what the specification asks servers to send for older clients) is
//     dropped.
//   - audio, a resource link and an embedded resource have no Part kind of
//     their own: an embedded text resource is its text under a header with the
//     URI, an embedded image blob is an image Part, and the rest become one
//     line of text that names the content and its size.
//   - isError makes the Tool Result failed; its content is what the model reads.
//   - _meta, on the result and on a content block, is copied as strings, without
//     the gotato.MetaTag key, so a server cannot write tagged text as the system.
func convertResult(result *sdk.CallToolResult) gotato.ToolResult {
	out := gotato.ToolResult{Status: gotato.ToolResultStatusOK, Metadata: metaStrings(result.Meta)}
	if result.IsError {
		out.Status = gotato.ToolResultStatusFailed
	}
	var structured *gotato.ContentPart
	if result.StructuredContent != nil {
		if encoded, err := json.Marshal(result.StructuredContent); err == nil {
			structured = &gotato.ContentPart{Kind: gotato.ContentKindJSON, Text: string(encoded)}
		}
	}
	for _, content := range result.Content {
		part, ok := convertContent(content)
		if !ok {
			continue
		}
		if structured != nil && part.Kind == gotato.ContentKindText && sameJSON(part.Text, structured.Text) {
			continue
		}
		out.Content = append(out.Content, part)
	}
	if structured != nil {
		out.Content = append(out.Content, *structured)
	}
	if out.Status == gotato.ToolResultStatusFailed {
		out.SafeError = firstText(out.Content, 200)
		if out.SafeError == "" {
			out.SafeError = "the MCP tool reported an error"
		}
	}
	return out
}

func convertContent(content sdk.Content) (gotato.ContentPart, bool) {
	switch c := content.(type) {
	case *sdk.TextContent:
		return gotato.ContentPart{Kind: gotato.ContentKindText, Text: c.Text, Metadata: metaStrings(c.Meta)}, true
	case *sdk.ImageContent:
		return gotato.ContentPart{Kind: gotato.ContentKindImage, Data: c.Data, MIMEType: c.MIMEType, Metadata: metaStrings(c.Meta)}, true
	case *sdk.AudioContent:
		return placeholder(fmt.Sprintf("[audio content, %s, %d bytes, not shown]", orUnknown(c.MIMEType), len(c.Data)), c.Meta), true
	case *sdk.ResourceLink:
		line := "[resource link: " + c.URI
		if c.Name != "" {
			line += " (" + c.Name + ")"
		}
		if c.MIMEType != "" {
			line += ", " + c.MIMEType
		}
		if c.Description != "" {
			line += ", " + c.Description
		}
		return placeholder(line+"]", c.Meta), true
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return gotato.ContentPart{}, false
		}
		r := c.Resource
		switch {
		case r.Text != "":
			return placeholder("[resource "+r.URI+"]\n"+r.Text, c.Meta), true
		case strings.HasPrefix(r.MIMEType, "image/") && len(r.Blob) > 0:
			return gotato.ContentPart{Kind: gotato.ContentKindImage, Data: r.Blob, MIMEType: r.MIMEType, Metadata: metaStrings(c.Meta)}, true
		default:
			return placeholder(fmt.Sprintf("[embedded resource: %s, %s, %d bytes, not shown]", r.URI, orUnknown(r.MIMEType), len(r.Blob)), c.Meta), true
		}
	case nil:
		return gotato.ContentPart{}, false
	default:
		return placeholder(fmt.Sprintf("[%T content, not shown]", content), nil), true
	}
}

func placeholder(text string, meta sdk.Meta) gotato.ContentPart {
	return gotato.ContentPart{Kind: gotato.ContentKindText, Text: text, Metadata: metaStrings(meta)}
}

func orUnknown(mime string) string {
	if mime == "" {
		return "unknown type"
	}
	return mime
}

// metaStrings copies _meta as strings (a value that is not a string is its
// JSON), and leaves out the gotato.MetaTag key.
func metaStrings(meta sdk.Meta) map[string]string {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for key := range meta {
		if key != gotato.MetaTag {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if text, ok := meta[key].(string); ok {
			out[key] = text
			continue
		}
		encoded, err := json.Marshal(meta[key])
		if err != nil {
			continue
		}
		out[key] = string(encoded)
	}
	return out
}

func sameJSON(a, b string) bool {
	var left, right any
	if json.Unmarshal([]byte(a), &left) != nil || json.Unmarshal([]byte(b), &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func firstText(parts []gotato.ContentPart, limit int) string {
	for _, part := range parts {
		if part.Kind == gotato.ContentKindText && part.Text != "" {
			text := strings.TrimSpace(part.Text)
			if len(text) > limit {
				text = text[:limit]
			}
			return text
		}
	}
	return ""
}
