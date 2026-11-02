package gotato

import (
	"fmt"
	"strconv"
	"strings"
)

// Window is a model's context window as a team states it: a number of tokens,
// or "unknown" written out. The zero Window states nothing, so a place that
// requires the window (model.New) can refuse a model whose team forgot it,
// and leaving it out is never an accident. A hand-written Model that states
// nothing reads as unknown to the runtime.
//
// A Window is written as text: the number, or the word unknown, and the empty
// text is the zero Window. That is how it reads from and writes to JSON and
// YAML.
type Window struct {
	tokens  int
	stated  bool
	unknown bool
}

// WindowTokens states a window of n tokens. n must be above 0; model.New
// refuses any other value when it builds a Client.
func WindowTokens(n int) Window { return Window{tokens: n, stated: true} }

// WindowUnknown states that the window is not known. Compaction then has no
// ceiling and runs only after the provider refuses a request as too long.
func WindowUnknown() Window { return Window{stated: true, unknown: true} }

// Tokens returns the window in tokens, or 0 when it is unknown or not stated.
func (w Window) Tokens() int {
	if w.unknown {
		return 0
	}
	return max(w.tokens, 0)
}

// Stated reports whether the team said anything: a number of tokens (even one
// that is not usable) or "unknown".
func (w Window) Stated() bool { return w.stated }

// Valid returns an error when the window is a number of tokens that is not
// above 0. The zero Window and an unknown one are valid; whether a window
// must be stated is the caller's to check with Stated.
func (w Window) Valid() error {
	if w.stated && !w.unknown && w.tokens <= 0 {
		return fmt.Errorf("a window of %d tokens: WindowTokens needs n > 0 (use WindowUnknown for an unknown window)", w.tokens)
	}
	return nil
}

// String is the text form: the number, "unknown", or "" for the zero Window.
func (w Window) String() string {
	switch {
	case !w.stated:
		return ""
	case w.unknown:
		return "unknown"
	}
	return strconv.Itoa(w.tokens)
}

// MarshalText implements encoding.TextMarshaler.
func (w Window) MarshalText() ([]byte, error) { return []byte(w.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It accepts a number of
// tokens above 0, the word unknown in any case, or empty text, and refuses
// anything else.
func (w *Window) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	switch {
	case value == "":
		*w = Window{}
	case strings.EqualFold(value, "unknown"):
		*w = WindowUnknown()
	default:
		tokens, err := strconv.Atoi(value)
		if err != nil || tokens <= 0 {
			return fmt.Errorf("window %q: want a number of tokens above 0, or unknown", value)
		}
		*w = WindowTokens(tokens)
	}
	return nil
}
