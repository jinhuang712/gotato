package session

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDefinitionKeyBytes is the longest kind or name, in bytes, that
// CheckDefinitionKey accepts.
const MaxDefinitionKeyBytes = 128

// Definitions is a store of documents keyed by (kind, name). It knows nothing
// of what a document holds: it keeps the bytes as they were written, and an
// implementation copies them in and out, so that it and its caller never
// share bytes. It is not part of Store; a caller passes it on its own.
type Definitions interface {
	// SaveDefinition stores doc under (kind, name), replacing what was there.
	// A kind or name that CheckDefinitionKey refuses is ErrInvalidID.
	SaveDefinition(ctx context.Context, kind, name string, doc []byte) error
	// LoadDefinition returns the document stored under (kind, name), or an
	// error that errors.Is ErrNotFound when there is none.
	LoadDefinition(ctx context.Context, kind, name string) ([]byte, error)
}

// CheckDefinitionKey returns ErrInvalidID, with the reason, for a kind or name
// no Definitions store should keep: an empty one, one longer than
// MaxDefinitionKeyBytes, one that is not valid UTF-8 or has a control
// character, one with a path separator or "..", and one that starts with ".".
// A store calls it first, so all of them refuse the same keys the same way.
func CheckDefinitionKey(kind, name string) error {
	for _, part := range []struct{ what, s string }{{"kind", kind}, {"name", name}} {
		reason := ""
		switch s := part.s; {
		case s == "":
			reason = "it is empty"
		case len(s) > MaxDefinitionKeyBytes:
			reason = fmt.Sprintf("it is longer than %d bytes", MaxDefinitionKeyBytes)
		case !utf8.ValidString(s):
			reason = "it is not valid UTF-8"
		case strings.IndexFunc(s, unicode.IsControl) >= 0:
			reason = "it has a control character"
		case strings.ContainsAny(s, `/\`):
			reason = "it has a path separator"
		case strings.Contains(s, ".."):
			reason = `it has ".."`
		case strings.HasPrefix(s, "."):
			reason = `it starts with "."`
		}
		if reason != "" {
			return fmt.Errorf("%w: definition %s %q: %s", ErrInvalidID, part.what, part.s, reason)
		}
	}
	return nil
}
