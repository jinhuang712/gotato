package gotato

import "math"

// SessionOption configures a Session built by NewSession.
type SessionOption func(*sessionConfig) error

// sessionConfig is what the SessionOptions collect for NewSession.
type sessionConfig struct {
	model          Model
	instruction    string
	tools          []Tool
	toolSets       []toolSetConfig
	toolSources    []ToolSource
	toolFilter     func(string) bool
	toolSearcher   ToolSearcher
	extensions     extensionSet
	limits         CoreLimits
	transcript     Transcript
	contextBuilder ContextBuilder
	modelConfig    ModelConfig
	cacheKey       string
	maxEffect      ToolEffect
	feed           *Feed
	onRunStart     func(RunID)
	handoff        bool
	requestID      string
}

// WithFeed has the Session publish its Events to feed instead of a Feed of
// its own, so the host that built it, such as a service.Runner, can offer
// the Session's stream across Runs. The host owns the Feed and closes it; it
// also sets the Feed's source, which is where a subscriber that falls behind
// catches up.
func WithFeed(feed *Feed) SessionOption {
	return func(c *sessionConfig) error {
		if feed == nil {
			return runtimeError(CodeInvalidArgument, "WithFeed", "feed is nil", nil)
		}
		c.feed = feed
		return nil
	}
}

// WithRequestID sets the caller's identifier of the request that makes the
// Session run. Every RunStarted of the Session carries it. The runtime records
// it and does nothing else with it: it does not look for a repeated one.
func WithRequestID(id string) SessionOption {
	return func(c *sessionConfig) error { c.requestID = id; return nil }
}

// WithRunStart calls fn on the Session's goroutine with each Run's ID, as the
// Run starts and before its first Event. fn must not block. A host uses it
// to find the Run it started.
func WithRunStart(fn func(RunID)) SessionOption {
	return func(c *sessionConfig) error { c.onRunStart = fn; return nil }
}

// WithModel sets the Model the Session calls. NewSession fails when none is
// set.
func WithModel(model Model) SessionOption {
	return func(c *sessionConfig) error {
		if model == nil {
			return runtimeError(CodeInvalidArgument, "WithModel", "model is nil", nil)
		}
		c.model = model
		return nil
	}
}

// WithInstruction sets the system instructions the Session sends with every
// model call, before the ContextBuilder sees them.
func WithInstruction(instruction string) SessionOption {
	return func(c *sessionConfig) error { c.instruction = instruction; return nil }
}

// WithModelConfig sets the sampling and reasoning hints the Session sends on
// every model call. Provider adapters map the fields they support.
func WithModelConfig(options ModelConfig) SessionOption {
	return func(c *sessionConfig) error {
		if options.Temperature != nil && (math.IsNaN(*options.Temperature) || math.IsInf(*options.Temperature, 0)) {
			return runtimeError(CodeInvalidArgument, "WithModelConfig", "temperature is not a finite number", nil)
		}
		if options.Temperature != nil && *options.Temperature < 0 {
			return runtimeError(CodeInvalidArgument, "WithModelConfig", "temperature is negative", nil)
		}
		if options.Temperature != nil {
			temperature := *options.Temperature
			options.Temperature = &temperature
		}
		c.modelConfig = options
		return nil
	}
}

// WithCacheKey sets the ModelRequest.CacheKey the Session sends on every model
// call: a stable value for one conversation, such as a hash of its Session ID,
// so the provider serves its Turns from one prompt cache. It never changes the
// prompt.
func WithCacheKey(key string) SessionOption {
	return func(c *sessionConfig) error { c.cacheKey = key; return nil }
}

// WithMaxEffect caps the Effect of the Tools the Session offers. A Tool above
// the cap, including one in a ToolSet, is never in the catalog, never found by
// a search, never loaded and never shown to the Model, and a call to it fails
// with a result that says the Tool exists but this Session does not allow it. A Tool that declares no Effect ranks as
// ToolEffectDestructive, so any lower cap hides it. ToolEffectUnspecified means
// no cap, which is the default.
func WithMaxEffect(effect ToolEffect) SessionOption {
	return func(c *sessionConfig) error {
		if !effect.Valid() {
			return runtimeError(CodeInvalidArgument, "WithMaxEffect", "unknown Effect "+string(effect), nil)
		}
		c.maxEffect = effect
		return nil
	}
}

// WithTools adds Tools the Model can call from the first Turn. It fails with
// CodeInvalidArgument when a Tool is nil. Tools from a ToolSet or a ToolSource
// are added by their own options.
func WithTools(tools ...Tool) SessionOption {
	return func(c *sessionConfig) error {
		for _, tool := range tools {
			if tool == nil {
				return runtimeError(CodeInvalidArgument, "WithTools", "tool is nil", nil)
			}
		}
		c.tools = append(c.tools, tools...)
		return nil
	}
}

// WithLimits sets the Session's CoreLimits; a zero field keeps its default. A
// negative ModelCallDeadline or ToolCallDeadline means no deadline.
func WithLimits(limits CoreLimits) SessionOption {
	return func(c *sessionConfig) error {
		c.limits = limits
		return nil
	}
}
