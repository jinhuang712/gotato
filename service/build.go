package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// buildSession builds the live Session for one Run from its Agent and its
// Record's own settings. override replaces the Session's instruction for an
// inspection; a Run passes none. With a writer, the writer is its Transcript,
// so every step of a Run is written as it happens; without one the Record is,
// and keeps the Session's Events in memory. extra options come after the
// Agent's own.
func (r *Runner) buildSession(ctx context.Context, spec Agent, s *session.Record, writer *session.Writer, override InspectConfig, extra ...gotato.SessionOption) (*gotato.Session, *modelctx.AutoCompactor, error) {
	// Without a writer nothing is stored: the live Session numbers its Events
	// on its own stream, and does not use the Record's numbers, which are
	// positions in the log that this Run does not write.
	var transcript gotato.Transcript = unjournaled{s}
	if writer != nil {
		transcript = writer
	}
	instruction := spec.Instruction
	if stored, ok := s.Get(MetaInstruction); ok && stored != "" {
		instruction = stored
	}
	if override.Instruction != "" {
		instruction = override.Instruction
	}
	builder := spec.ContextBuilder
	if builder == nil {
		builder = gotato.FullHistoryContext()
	}
	// Every Run has its Session compacted when the provider refuses a request
	// as too long, and past the ceiling when there is one.
	auto := modelctx.AutoCompact(s, compactPolicyFor(spec, s))
	options := []gotato.SessionOption{
		gotato.WithModel(spec.Model),
		gotato.WithInstruction(instruction),
		gotato.WithTranscript(transcript),
		gotato.WithContextBuilder(builder),
		gotato.WithCacheKey(sessionCacheKey(s)),
	}
	options = append(options, gotato.WithExtension(auto))
	source := &sessionTools{spec: spec, session: s, segments: r.segmentTools(spec, s), results: r.resultTools(s)}
	options = append(options, gotato.WithToolSource(source),
		// A Tool switched off for the Session is never in the catalog, found or loaded.
		gotato.WithToolFilter(source.active))
	if spec.MaxEffect != gotato.ToolEffectUnspecified {
		options = append(options, gotato.WithMaxEffect(spec.MaxEffect))
	}
	if spec.Limits != nil {
		options = append(options, gotato.WithLimits(*spec.Limits))
	}
	options = append(options, spec.Options...)
	options = append(options, extra...)
	agent, err := gotato.NewSession(ctx, options...)
	if err != nil {
		return nil, nil, err
	}
	// The Agent's ToolSets are in its Options, so only the live Session knows
	// it has any. The flag is read at the next refresh of the tool list, which
	// comes before the first request of a Run or an inspection.
	source.hasToolSets.Store(len(agent.ToolSets()) > 0)
	return agent, auto, nil
}

// unjournaled is a Record as a Transcript that is not a Journal, so a Session
// over it keeps no Events in the Record.
type unjournaled struct{ record *session.Record }

func (u unjournaled) Len() int { return u.record.Len() }

func (u unjournaled) Messages() []gotato.Message { return u.record.Messages() }

func (u unjournaled) Append(m gotato.Message) error { return u.record.Append(m) }

func (u unjournaled) ID() string { return u.record.ID() }

func (u unjournaled) LoadedTools() []string { return u.record.LoadedTools() }

// CacheKey is the ModelRequest.CacheKey the Runner gives every Turn of a
// Session, so the provider serves a conversation from one prompt cache. It is
// a hash of the Session ID, because an ID the caller chose may name a user or
// a tenant, and the key goes to the provider. A Session forked by the Runner
// keeps its parent's key instead (MetaCacheKey), and an Agent may set another
// with gotato.WithCacheKey in its Options.
func CacheKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return "gotato-" + hex.EncodeToString(sum[:16])
}

// sessionCacheKey is the cache key a Session's Turns carry: the one in its
// metadata (MetaCacheKey), which a fork inherits from its parent, or else
// CacheKey of its ID.
func sessionCacheKey(s *session.Record) string {
	if key, ok := s.Get(MetaCacheKey); ok && key != "" {
		return key
	}
	return CacheKey(s.ID())
}
