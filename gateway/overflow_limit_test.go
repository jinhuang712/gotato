package gateway

import "testing"

// A refusal as too long that names the request's limit in tokens says it by
// ContextLimit, in each provider's wording; one that does not says 0.
func TestARefusalNamesItsLimitWhenItsWordingDoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    int
	}{
		{"chat", "This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.", 8192},
		{"chat with separators", "This model's maximum context length is 128,000 tokens.", 128000},
		{"anthropic", "prompt is too long: 210000 tokens > 200000 maximum", 200000},
		{"anthropic with max_tokens", "input length and max_tokens exceed context limit: 190000 + 21333 > 200000", 200000},
		{"responses", "Your input exceeds the context window of 400000 tokens.", 400000},
		{"no number", "Prompt is too long", 0},
		{"a number that is not a limit", "prompt is too long for 3 items", 0},
	} {
		err := &Error{Message: tc.message, Overflow: true}
		if got := err.ContextLimit(); got != tc.want {
			t.Errorf("%s: ContextLimit = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := (&Error{Message: "maximum context length is 8192 tokens"}).ContextLimit(); got != 0 {
		t.Errorf("an error that is no overflow names the limit %d", got)
	}
	var none *Error
	if none.ContextLimit() != 0 {
		t.Error("a nil error names a limit")
	}
}
