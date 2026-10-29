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
		if options.Temperature != nil {
			temperature := *options.Temperature
			// NaN fails the first comparison; negative and infinite are out of range.
			// [CONTRACT] a NaN, infinite or negative temperature is not a sampling value a provider accepts.
			if !(temperature >= 0) || math.IsInf(temperature, 1) {
				return runtimeError(CodeInvalidArgument, "WithModelConfig", "temperature is not a finite number of at least 0", nil)
			}
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
// are added by their own options. A Tool's ID and Name cannot be search_tools
// or start with the name of an installed ToolSet and a dot: that namespace is
// the ToolSet's, and NewSession refuses the Tool with CodeInvalidArgument.
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

// WithContextBuilder selects the strategy that turns the Transcript into the
// Model's Context for each Turn. The default is FullHistoryContext.
func WithContextBuilder(builder ContextBuilder) SessionOption {
	return func(c *sessionConfig) error {
		if builder == nil {
			return runtimeError(CodeInvalidArgument, "WithContextBuilder", "builder is nil", nil)
		}
		c.contextBuilder = builder
		return nil
	}
}

// WithExtension installs one Extension. A value may implement several
// Extension interfaces; it is installed into each of them. The resulting
// order is immutable for the Session lifetime. It fails with
// CodeInvalidArgument when the value implements no Extension interface, or has
// a method named like a hook (Transform, Convert, Before, After, Stop,
// PrepareTurn, Advisory) whose signature does not match it.
func WithExtension(extension any) SessionOption {
	return func(c *sessionConfig) error {
		if extension == nil {
			return runtimeError(CodeInvalidArgument, "WithExtension", "extension is nil", nil)
		}
		if err := checkExtensionMethods(extension); err != nil {
			return err
		}
		installed := false
		if typed, ok := extension.(ContextTransformer); ok {
			c.extensions.transformers = append(c.extensions.transformers, typed)
			installed = true
		}
		if typed, ok := extension.(MessageConverter); ok {
			c.extensions.converters = append(c.extensions.converters, typed)
			installed = true
		}
		if typed, ok := extension.(PreToolUse); ok {
			c.extensions.pre = append(c.extensions.pre, typed)
			installed = true
		}
		if typed, ok := extension.(PostToolUse); ok {
			c.extensions.post = append(c.extensions.post, typed)
			installed = true
		}
		if typed, ok := extension.(TurnStopper); ok {
			c.extensions.stoppers = append(c.extensions.stoppers, typed)
			installed = true
		}
		if typed, ok := extension.(TurnPreparer); ok {
			c.extensions.preparers = append(c.extensions.preparers, typed)
			installed = true
		}
		if !installed {
			return runtimeError(CodeInvalidArgument, "WithExtension", "value implements no Extension interface", nil)
		}
		return nil
	}
}

// WithToolSet installs a ToolSet whose catalog entry lists its description and
// all its Tool names. The Model loads the Tools it needs with search_tools.
func WithToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeListed)
}

// WithFoldedToolSet installs a ToolSet whose catalog entry shows only its
// description and a count of its Tools. The Model lists them with the toolset
// parameter of search_tools, then loads by name.
func WithFoldedToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeFolded)
}

// WithActiveToolSet installs a ToolSet whose Tools are visible always, as if
// they were base Tools. They are never in the catalog and never unloaded.
func WithActiveToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeActive)
}

func withToolSet(set ToolSet, mode installMode) SessionOption {
	return func(c *sessionConfig) error {
		if set == nil {
			return runtimeError(CodeInvalidArgument, "WithToolSet", "ToolSet is nil", nil)
		}
		c.toolSets = append(c.toolSets, toolSetConfig{set: set, mode: mode})
		return nil
	}
}

// WithToolFilter has the Session ask keep for the qualified ID of every Tool of
// a ToolSet before it indexes, lists or offers it. A Tool keep refuses is never
// in the catalog, never found by a search and never loaded, so a Session can
// switch a Tool off and the Model cannot search its way past it. keep is asked
// when the Tools are resolved, with no lock held: at NewSession, at the start
// of every Run, and at every Turn of a Session with a ToolSource. It must be
// quick and safe to call from several goroutines. A keep that panics fails
// NewSession or the Run with CodeExtensionFailure.
func WithToolFilter(keep func(toolID string) bool) SessionOption {
	return func(c *sessionConfig) error {
		c.toolFilter = keep
		return nil
	}
}

// WithTranscript makes the Session commit to an external Transcript, such as
// the session.Record it is loaded from, instead of a private in-memory one.
//
// A Transcript may have optional capabilities, each found by a type assertion
// on the value given here, so a wrapper must forward them all (embedding the
// concrete type does; embedding the Transcript interface does not):
//
//	ID() string                 the Session takes the Record's ID
//	Journal                     Events are stored, numbered by the Record
//	Durable() bool              (of the Journal) whether they survive the process
//	SpillKeeper                 a large Tool Result is spilled, and read back
//	EventSource                 a Subscription catches up from the Record
//	LoadedToolSource            Tools a Run loaded are restored
//
// Without a Journal the Session numbers its Events in memory and stores none
// (service.Runner does this on purpose for a Run that is not saved), so a
// wrapper that hides Write makes the Session non-durable without an error.
func WithTranscript(transcript Transcript) SessionOption {
	return func(c *sessionConfig) error {
		if transcript == nil {
			return runtimeError(CodeInvalidArgument, "WithTranscript", "transcript is nil", nil)
		}
		c.transcript = transcript
		return nil
	}
}
