package modelctx

import (
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// ContextWindower is a Model that knows its context window in tokens.
// gateway.Client is one, from the model's context_window_tokens in its
// configuration. Gotato keeps no table of windows, so a Model that does not
// say has none.
type ContextWindower interface {
	ContextWindowTokens() int
}

// CacheRetainer is a Model that knows how long its provider keeps the prompt
// cache after a request. gateway.Client is one.
type CacheRetainer interface {
	CacheRetention() time.Duration
}

// WindowOf returns the context window of model in tokens, or 0 when the
// model does not say or says none.
func WindowOf(model gotato.Model) int {
	if windower, ok := model.(ContextWindower); ok {
		return max(windower.ContextWindowTokens(), 0)
	}
	return 0
}

// RetentionOf returns how long model's provider keeps the prompt cache, or 0
// when that is unknown. Callers treat 0 as unknown, never as no cache.
func RetentionOf(model gotato.Model) time.Duration {
	if retainer, ok := model.(CacheRetainer); ok {
		return max(retainer.CacheRetention(), 0)
	}
	return 0
}
