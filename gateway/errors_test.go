package gateway

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/gotato"
)

// The cut of a plain-text error body must not split a rune.
func TestReadErrorBodyCutsOnARuneBoundary(t *testing.T) {
	body := readErrorBody(strings.NewReader("a" + strings.Repeat("é", 400)))
	if !utf8.ValidString(body.Message) || len(body.Message) > 512 {
		t.Fatalf("Message has %d bytes, valid = %v", len(body.Message), utf8.ValidString(body.Message))
	}
}

func TestErrorIsPublicAndKeepsTheTransportCauseHidden(t *testing.T) {
	cause := errors.New(`Post "https://gateway.internal/v1/messages": dial tcp 10.0.0.5: refused`)
	sent := &Error{Message: "the request could not be sent", cause: cause}
	var public gotato.PublicError
	if !errors.As(error(sent), &public) {
		t.Fatal("gateway.Error is not a gotato.PublicError")
	}
	if strings.Contains(public.Public(), "gateway.internal") || !errors.Is(sent, cause) {
		t.Fatalf("public text %q, cause kept = %v", public.Public(), errors.Is(sent, cause))
	}
	refused := &Error{StatusCode: 400, Message: "model not found"}
	if got := refused.Public(); got != "gateway: HTTP 400: model not found" {
		t.Fatalf("public text = %q", got)
	}
}
