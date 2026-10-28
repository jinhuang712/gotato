package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

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
	agent, err := gotato.NewSessionContext(ctx, options...)
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

// InspectConfig overrides Session settings for one inspection without
// touching the Session. The zero value overrides nothing.
type InspectConfig struct {
	Instruction string
}

// Inspect reports the Context a Run against the Session would send now. It
// builds the live Session a Run would build and asks it, so the report includes the
// Agent's Options, Tools and model options, and says whether the next Run
// compacts the Session first. config overrides the Session's stored settings.
// The Session is not changed.
func (r *Runner) Inspect(ctx context.Context, sessionID string, config InspectConfig) (modelctx.Report, error) {
	s, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		return modelctx.Report{}, err
	}
	agentName, _ := s.Get(MetaAgent)
	spec, ok := r.Agent(agentName)
	if !ok {
		return modelctx.Report{}, ErrUnknownAgent
	}
	live, auto, err := r.buildSession(ctx, spec, s, nil, config)
	if err != nil {
		return modelctx.Report{}, err
	}
	defer func() { _ = live.Close(context.Background()) }()
	inspection, err := live.InspectContext(ctx)
	if err != nil {
		return modelctx.Report{}, err
	}
	report := modelctx.ReportOf(inspection)
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	if auto != nil {
		report.CompactionPending = auto.PendingFor(inspection.Request)
		report.CompactCeilingTokens = auto.CeilingTokens()
	}
	return report, nil
}

// sessionTools is the ToolSource a live Session sees: its Agent's
// static Tools and ToolSources within its MaxEffect, minus the ones the
// Session deactivated. It is re-read at every Turn boundary like any
// ToolSource.
type sessionTools struct {
	spec    Agent
	session *session.Record
	// segments gives the built-in read_segment tool once the Session has a
	// Segment; nil for a surface that is not a Session's.
	segments gotato.ToolSource
	// results gives the built-in read_result tool to a Session whose Agent
	// has any tool; nil for a surface that is not a Session's.
	results gotato.ToolSource
	// hasToolSets is set once the live Session is built and has a ToolSet,
	// whose Tools can spill a result like any other.
	hasToolSets atomic.Bool
}

// segmentTools is the ToolSource that offers read_segment for a Session of the
// Agent spec: the Messages are read back from the Runner's store, in pages
// that fit one page, in the zone of its Segments' times.
func (r *Runner) segmentTools(spec Agent, record *session.Record) gotato.ToolSource {
	options := modelctx.ReadSegmentConfig{Location: spec.Compact.Location}
	return modelctx.SegmentTools(record, r.store, options)
}

// resultTools is the ToolSource that offers read_result: a spilled result is
// read back from the live Record or from the Runner's store.
func (r *Runner) resultTools(record *session.Record) gotato.ToolSource {
	return modelctx.ResultTools(record, r.store, modelctx.ReadResultConfig{})
}

func (t *sessionTools) Tools() []gotato.Tool {
	var tools []gotato.Tool
	seen := map[string]bool{}
	add := func(list []gotato.Tool) {
		for _, tool := range list {
			id := tool.Spec().ID
			if !seen[id] && t.allowed(tool.Spec()) && t.active(id) {
				seen[id] = true
				tools = append(tools, tool)
			}
		}
	}
	add(t.spec.Tools)
	for _, source := range t.spec.ToolSources {
		add(source.Tools())
	}
	// read_result is offered whenever the Agent has a tool, its own or a
	// ToolSet's, so the tool list does not change on the Turn a result is
	// first spilled.
	if t.results != nil && (len(tools) > 0 || t.hasToolSets.Load()) {
		add(t.results.Tools())
	}
	if t.segments != nil {
		add(t.segments.Tools())
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Spec().ID < tools[j].Spec().ID })
	return tools
}

// allowed reports whether a Tool is within the Agent's MaxEffect.
func (t *sessionTools) allowed(spec gotato.ToolSpec) bool {
	return t.spec.MaxEffect == gotato.ToolEffectUnspecified || spec.Effect.Within(t.spec.MaxEffect)
}

func (t *sessionTools) active(id string) bool {
	if t.session == nil {
		return true
	}
	value, _ := t.session.Get(MetaToolPrefix + id)
	return value != "inactive"
}

// Tools describes the Tool surface of an Agent as a Session sees it.
// sessionID may be empty for the Agent's default surface. A Tool above the
// Agent's MaxEffect is not listed.
func (r *Runner) Tools(ctx context.Context, agent, sessionID string) ([]ToolEntry, error) {
	var s *session.Record
	if sessionID != "" {
		var err error
		s, err = session.Open(ctx, r.store, sessionID)
		if err != nil {
			return nil, err
		}
		if agent == "" {
			agent, _ = s.Get(MetaAgent)
		}
	}
	spec, ok := r.Agent(agent)
	if !ok {
		return nil, ErrUnknownAgent
	}
	all := &sessionTools{spec: spec}
	filter := &sessionTools{spec: spec, session: s}
	if s != nil {
		// A Session that has a Segment offers read_segment too.
		all.segments = r.segmentTools(spec, s)
		all.results = r.resultTools(s)
	}
	entries := make([]ToolEntry, 0)
	for _, tool := range all.Tools() {
		entries = append(entries, ToolEntry{Spec: tool.Spec(), Active: filter.active(tool.Spec().ID)})
	}
	if s != nil {
		// A Session's ToolSets live in the Agent's Options, so the live Session
		// says what they hold: every tool of every ToolSet, loaded or not,
		// switched off or not, and search_tools, whose description is the catalog.
		toolCtx, cancel := r.toolSetContext(ctx)
		defer cancel()
		live, _, err := r.buildSession(toolCtx, spec, s, nil, InspectConfig{})
		if err != nil {
			if r.toolSetsTimedOut(ctx, err) {
				// The Agent's own tools are listed; the error says the ToolSets did not answer.
				return entries, toolSetsTimeout(err)
			}
			return nil, err
		}
		defer func() { _ = live.Close(context.Background()) }()
		entries = append(entries, toolSetEntries(live, filter)...)
		for _, offered := range live.Tools() {
			if !slices.ContainsFunc(entries, func(e ToolEntry) bool { return e.Spec.ID == offered.ID }) {
				entries = append(entries, ToolEntry{Spec: offered, Active: true})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Spec.ID < entries[j].Spec.ID })
	}
	return entries, nil
}

// toolSetEntries lists the tools of a live Session's ToolSets, with whether
// each is loaded and whether the Session switched it off. A ToolSet that is
// unavailable now is one entry of its own.
func toolSetEntries(live *gotato.Session, filter *sessionTools) []ToolEntry {
	var entries []ToolEntry
	for _, set := range live.ToolSets() {
		if set.Unavailable {
			entries = append(entries, ToolEntry{Spec: gotato.ToolSpec{ID: set.Name, Name: set.Name, Description: set.Description}, Active: true, ToolSet: set.Name, Unavailable: true})
			continue
		}
		for _, tool := range set.Tools {
			entries = append(entries, ToolEntry{Spec: tool.Spec, Active: filter.active(tool.Spec.ID), ToolSet: set.Name, Loaded: tool.Loaded})
		}
	}
	return entries
}

// SetToolActive records a Session-level activation change and writes it as
// the Session's writer, under the Session lock. It names a tool of the Agent
// or of one of its ToolSets, which the Session resolves to find it; a ToolSet
// that is unavailable now cannot be asked, so its tools are named again once
// it answers.
func (r *Runner) SetToolActive(ctx context.Context, sessionID, toolID string, active bool) (ToolEntry, error) {
	// The lookup may ask the ToolSets over the network, so it is made before
	// the Session lock, on a read of the Record, and under its own bound.
	// What the lock guards is the write of the switch.
	read, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		return ToolEntry{}, err
	}
	agent, _ := read.Get(MetaAgent)
	spec, ok := r.Agent(agent)
	if !ok {
		return ToolEntry{}, ErrUnknownAgent
	}
	var found *gotato.ToolSpec
	var result ToolEntry
	for _, tool := range (&sessionTools{spec: spec}).Tools() {
		if tool.Spec().ID == toolID {
			ts := tool.Spec()
			found = &ts
			break
		}
	}
	if found == nil {
		found, result.ToolSet, result.Loaded, err = r.findSetTool(ctx, spec, read, toolID)
		if err != nil {
			return ToolEntry{}, err
		}
	}
	err = r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		s := writer.Record()
		held, _ := s.Get(MetaAgent)
		r.publishOpened(writer, early, held)
		if found == nil {
			_ = writer.Release(ctx)
			return gotato.ErrorOf(gotato.CodeInvalidArgument, "service: unknown tool "+toolID)
		}
		if active {
			s.Set(MetaToolPrefix+toolID, "")
		} else {
			s.Set(MetaToolPrefix+toolID, "inactive")
		}
		err = writer.Release(ctx)
		if err == nil {
			r.keepWarm(writer, spec)
		}
		return err
	})
	if err != nil {
		return ToolEntry{}, err
	}
	result.Spec, result.Active = *found, active
	return result, nil
}

// toolSetContext bounds the ToolSet resolution of one call by the Runner's
// ToolSetTimeout.
func (r *Runner) toolSetContext(ctx context.Context) (context.Context, context.CancelFunc) {
	d := r.toolSetTimeout
	if d == 0 {
		d = DefaultToolSetTimeout
	}
	if d < 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// toolSetsTimedOut reports whether err is the ToolSet bound running out while
// the caller's own ctx is still live.
func (r *Runner) toolSetsTimedOut(ctx context.Context, err error) bool {
	return ctx.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || gotato.IsCode(err, gotato.CodeDeadlineExceeded))
}

// toolSetsTimeout is the error of a ToolSet bound that ran out.
func toolSetsTimeout(cause error) error {
	return gotato.ErrorOf(gotato.CodeDeadlineExceeded, "service: the tool sets did not answer in time: "+cause.Error())
}

// findSetTool looks for toolID among the tools of the Agent's ToolSets, which
// a live Session resolves. It reports the tool's spec (nil when no ToolSet
// holds it), its ToolSet and whether it is loaded. A name inside an
// unavailable ToolSet is an error that says so.
func (r *Runner) findSetTool(ctx context.Context, spec Agent, s *session.Record, toolID string) (*gotato.ToolSpec, string, bool, error) {
	toolCtx, cancel := r.toolSetContext(ctx)
	defer cancel()
	live, _, err := r.buildSession(toolCtx, spec, s, nil, InspectConfig{})
	if err != nil {
		if r.toolSetsTimedOut(ctx, err) {
			return nil, "", false, toolSetsTimeout(err)
		}
		return nil, "", false, err
	}
	defer func() { _ = live.Close(context.Background()) }()
	for _, set := range live.ToolSets() {
		if set.Unavailable && strings.HasPrefix(toolID, set.Name+".") {
			return nil, "", false, gotato.ErrorOf(gotato.CodeInvalidArgument, "service: tool set "+set.Name+" is unavailable now; "+toolID+" cannot be checked")
		}
		for _, tool := range set.Tools {
			if tool.Spec.ID == toolID {
				found := tool.Spec
				return &found, set.Name, tool.Loaded, nil
			}
		}
	}
	return nil, "", false, nil
}
