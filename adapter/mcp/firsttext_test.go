package mcp_test

import (
	"testing"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
)

// A cut at the byte limit must not split a rune: the text reaches the model.
func TestFirstTextCutsOnARuneBoundary(t *testing.T) {
	parts := []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "日本語"}}
	got := mcp.FirstText(parts, 4)
	if !utf8.ValidString(got) || got != "日" {
		t.Fatalf("FirstText = %q, want %q", got, "日")
	}
}
