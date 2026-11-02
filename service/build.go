package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// buildSession builds the live Session for one Run from its Agent: every
// SessionOption is the Runner's, built from the Agent's typed fields and the
// Record's tool switches. override replaces the instruction for an inspection
// only; a Run passes none. With a writer, the writer is its Transcript, so every
// step of a Run is written as it happens; without one the Record is, and keeps
// the Session's Events in memory. extra options come after the Agent's own, and
// NewSession refuses one that repeats a single-valued option.
func (r *Runner) buildSession(ctx context.Context, spec Agent, s *session.Record, writer *session.Writer, override InspectConfig, extra ...gotato.SessionOption) (*gotato.Session, *modelctx.AutoCompactor, error) {
	// Without a writer nothing is stored: the live Session numbers its Events
	// on its own stream, and does not use the Record's numbers, which are
	// positions in the log that this Run does not write.
	var transcript gotato.Transcript = unjournaled{s}
	if writer != nil {
		transcript = writer
	}
	instruction := spec.Instruction
	if override.Instruction != "" {
		instruction = override.Instruction
	}
	builder := spec.ContextBuilder
	if builder == nil {
		builder = gotato.FullHistoryContext()
	}
	// Every Run has its Session compacted when the provider refuses a request
	// as too long, and past the ceiling when there is one.
	auto := modelctx.AutoCompact(s, compactorFor(spec))
	options := []gotato.SessionOption{
		gotato.WithModel(spec.Model),
		gotato.WithInstruction(instruction),
		gotato.WithTranscript(transcript),
		gotato.WithContextBuilder(builder),
		gotato.WithCacheKey(sessionCacheKey(s)),
		gotato.WithSampling(spec.Sampling),
	}
	options = append(options, gotato.WithExtension(auto))
	for _, extension := range spec.Extensions {
		options = append(options, gotato.WithExtension(extension))
	}
	for _, set := range spec.ToolSets {
		switch set.Mode {
		case ToolSetFolded:
			options = append(options, gotato.WithFoldedToolSet(set.Set))
		case ToolSetActive:
			options = append(options, gotato.WithActiveToolSet(set.Set))
		default:
			options = append(options, gotato.WithToolSet(set.Set))
		}
	}
	if spec.ToolSearcher != nil {
		options = append(options, gotato.WithToolSearcher(spec.ToolSearcher))
	}
	source := &sessionTools{spec: spec, session: s}
	options = append(options, gotato.WithToolSource(source), gotato.WithReadBack(r.readBack(spec, s)),
		// A Tool switched off for the Session is never in the catalog, found or loaded.
		gotato.WithToolFilter(source.active))
	if spec.MaxEffect != gotato.ToolEffectUnspecified {
		options = append(options, gotato.WithMaxEffect(spec.MaxEffect))
	}
	if spec.Limits != nil {
		options = append(options, gotato.WithLimits(*spec.Limits))
	}
	options = append(options, extra...)
	agent, err := gotato.NewSession(ctx, options...)
	if err != nil {
		return nil, nil, err
	}
	return agent, auto, nil
}

// unjournaled is a Record as a Transcript that is not a Journal, so a Session
// over it keeps no Events in the Record.
type unjournaled struct{ record *session.Record }

func (u unjournaled) Len() int { return u.record.Len() }

func (u unjournaled) Messages() []gotato.Message { return u.record.Messages() }

func (u unjournaled) Append(m gotato.Message) error { return u.record.Append(m) }

func (u unjournaled) LoadedTools() []string { return u.record.LoadedTools() }

// Capabilities says what a Run that is not saved has: the Record's ID and
// loaded Tools, and no Journal, no Events and no spill, so the live Session
// numbers its Events in memory and stores none.
func (u unjournaled) Capabilities() gotato.Capabilities {
	return gotato.Capabilities{ID: u.record.ID(), Loaded: u}
}

// CacheKey is the ModelRequest.CacheKey the Runner gives every Turn of a
// Session, so the provider serves a conversation from one prompt cache. It is
// a hash of the Session ID, because an ID the caller chose may name a user or
// a tenant, and the key goes to the provider. A Session forked by the Runner
// keeps its parent's key instead (metaCacheKey).
func CacheKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return "gotato-" + hex.EncodeToString(sum[:16])
}

// sessionCacheKey is the cache key a Session's Turns carry: the one in its
// metadata (metaCacheKey), which a fork inherits from its parent, or else
// CacheKey of its ID.
func sessionCacheKey(s *session.Record) string {
	if key, ok := s.Get(metaCacheKey); ok && key != "" {
		return key
	}
	return CacheKey(s.ID())
}
