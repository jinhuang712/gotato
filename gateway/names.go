package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	gotato "github.com/jinhuang712/gotato"
)

// maxFunctionName is the provider limit on function names
// (^[a-zA-Z0-9_-]{1,64}$ for OpenAI-compatible APIs).
const maxFunctionName = 64

// gatewayFunctionName returns the provider-facing name for a Tool ID. It is
// the ID itself when the ID is already provider-safe, and otherwise the ID
// with each unsafe character replaced by '_' (demo.echo becomes demo_echo).
//
// Names stay readable on purpose: a Model copies the name it was shown into
// its tool call, and models reproduce readable names far more reliably than
// opaque encodings. A name longer than the limit is truncated and suffixed
// with a short hash of the full ID.
func gatewayFunctionName(id string) string {
	name := sanitizeFunctionName(id)
	if name == "" {
		return hashedFunctionName(id)
	}
	if len(name) > maxFunctionName {
		return name[:maxFunctionName-9] + "_" + shortHash(id, 8)
	}
	return name
}

// hashedFunctionName is the collision fallback: a readable prefix plus a hash
// of the full ID, unique per ID.
func hashedFunctionName(id string) string {
	prefix := sanitizeFunctionName(id)
	if len(prefix) > maxFunctionName-17 {
		prefix = prefix[:maxFunctionName-17]
	}
	if prefix == "" {
		prefix = "tool"
	}
	return prefix + "_" + shortHash(id, 16)
}

// plainFunctionName reports whether s matches ^[a-zA-Z0-9_-]{1,64}$, the
// function names that chat completions, responses, and messages APIs accept.
func plainFunctionName(s string) bool {
	return s != "" && len(s) <= maxFunctionName && sanitizeFunctionName(s) == s
}

func sanitizeFunctionName(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func shortHash(id string, n int) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:n]
}

// functionNames names every Tool ID a request uses, its Tools and the Tool
// Calls in its history, and returns the map from provider name to Tool ID.
// IDs that providers accept claim their own names first, so when two IDs
// sanitize to the same name (a.b and a_b), the rewritten one takes the hash
// and a_b goes out unchanged, whatever order the IDs come in.
func functionNames(request gotato.ModelRequest) (map[string]string, error) {
	ids := make([]string, 0, len(request.Tools))
	for _, spec := range request.Tools {
		ids = append(ids, spec.ID)
	}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			ids = append(ids, call.ToolID)
		}
	}
	names := make(map[string]string, len(ids))
	for _, id := range ids {
		if plainFunctionName(id) {
			names[id] = id
		}
	}
	for _, id := range ids {
		if _, err := assignFunctionName(names, id); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// assignFunctionName records the provider name for id in names (provider
// name -> Tool ID) and returns it. When the name id sanitizes to belongs to
// another ID, id falls back to a hashed name, so every Tool in a request
// keeps a distinct name. functionNames runs it over a whole request first,
// so later calls for the same request only look names up.
func assignFunctionName(names map[string]string, id string) (string, error) {
	name := gatewayFunctionName(id)
	if previous, exists := names[name]; exists && previous != id {
		name = hashedFunctionName(id)
		if previous, exists := names[name]; exists && previous != id {
			return "", fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, id)
		}
	}
	names[name] = id
	return name, nil
}
