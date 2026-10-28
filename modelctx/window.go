package modelctx

import (
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Unwrapper is implemented by a Model that wraps another one, such as a
// wrapper that counts calls or adds a header. WindowOf, RetentionOf and
// DropsReasoning look for a capability (ContextWindower, CacheRetainer,
// RoundReasoner) on the Model first and then on what Unwrap returns, down
// the chain, as errors.Is does for errors. A wrapper that does not implement
// Unwrap, or the capability itself, hides the capabilities of the Model it
// wraps: the window reads as unknown (0), and with it the ceiling.
type Unwrapper interface {
	Unwrap() gotato.Model
}

// capabilityOf finds the first Model in the Unwrap chain of model that
// implements T.
func capabilityOf[T any](model gotato.Model) (T, bool) {
	for model != nil {
		if capability, ok := model.(T); ok {
			return capability, true
		}
		wrapper, ok := model.(Unwrapper)
		if !ok {
			break
		}
		model = wrapper.Unwrap()
	}
	var none T
	return none, false
}

// ContextWindower is a Model that knows its context window in tokens.
// gateway.Client is one, from the model's context_window_tokens in its
// configuration. Gotato keeps no table of windows, so a Model that does not
// say has none. A wrapper Model forwards it with Unwrap (Unwrapper).
type ContextWindower interface {
	ContextWindowTokens() int
}

// CacheRetainer is a Model that knows how long its provider keeps the prompt
// cache after a request. gateway.Client is one. A wrapper Model forwards it
// with Unwrap (Unwrapper).
type CacheRetainer interface {
	CacheRetention() time.Duration
}

// WindowOf returns the context window of model in tokens, or 0 when the
// model does not say or says none. It follows Unwrap (Unwrapper).
func WindowOf(model gotato.Model) int {
	if windower, ok := capabilityOf[ContextWindower](model); ok {
		return max(windower.ContextWindowTokens(), 0)
	}
	return 0
}

// RetentionOf returns how long model's provider keeps the prompt cache, or 0
// when that is unknown. Callers treat 0 as unknown, never as no cache.
func RetentionOf(model gotato.Model) time.Duration {
	if retainer, ok := capabilityOf[CacheRetainer](model); ok {
		return max(retainer.CacheRetention(), 0)
	}
	return 0
}
