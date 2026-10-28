package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The cut of a plain-text error body must not split a rune.
func TestReadErrorBodyCutsOnARuneBoundary(t *testing.T) {
	body := readErrorBody(strings.NewReader("a" + strings.Repeat("é", 400)))
	if !utf8.ValidString(body.Message) || len(body.Message) > 512 {
		t.Fatalf("Message has %d bytes, valid = %v", len(body.Message), utf8.ValidString(body.Message))
	}
}
