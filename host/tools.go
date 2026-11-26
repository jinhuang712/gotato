package host

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// sessionTools is the ToolSource a live Session sees: its Agent's
// static Tools and ToolSources within its MaxEffect, minus the ones the
// Session deactivated. It is re-read at every Turn boundary like any
// ToolSource. It is one source to the Session: a Tool ID that two of its
// parts both supply fails it whole, as any bad Tool does, and neither Tool is
// kept in place of the other.
type sessionTools struct {
	spec    profile.AgentProfile
	session *record.Record
}

// readBack is the gotato.ReadBack of a live Session of the AgentProfile spec:
// the profile's own when it sets one, else the default record.ReadBack over
// the Runner's store, so the Messages a Segment closed and the body of a
// spilled result are read back from the Record or from the store, in the zone
// of the Segments' times.
func (r *Runner) readBack(spec profile.AgentProfile, rec *record.Record) gotato.ReadBack {
	if spec.ReadBack != nil {
		return spec.ReadBack
	}
	return agentReadBack{
		ReadBack: record.ReadBack{Originals: record.OriginalsOf(rec, r.store.Records), Location: spec.Compact.Location},
		hasTools: len(spec.Tools) > 0 || len(spec.ToolSources) > 0 || len(spec.ToolSets) > 0,
	}
}

// agentReadBack offers read_result only to a Session whose Agent has a tool,
// its own, a ToolSource's or a ToolSet's, so the tool list does not change on
// the Turn a result is first spilled and an Agent with no tool has none. It
// also offers it whenever read_segment is: a Segment's refs to its messages
// and results are read with it.
type agentReadBack struct {
	record.ReadBack
	hasTools bool
}

// Tools implements gotato.ToolSource.
func (a agentReadBack) Tools(ctx context.Context) ([]gotato.Tool, error) {
	tools, err := a.ReadBack.Tools(ctx)
	if err != nil || len(tools) > 1 || a.hasTools {
		return tools, err
	}
	return nil, nil
}

func (t *sessionTools) Tools(ctx context.Context) ([]gotato.Tool, error) {
	tools, err := t.offer(nil, t.spec.Tools)
	if err != nil {
		return nil, err
	}
	for _, source := range t.spec.ToolSources {
		supplied, err := source.Tools(ctx)
		if err != nil {
			return nil, err
		}
		tools, err = t.offer(tools, supplied)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].id < tools[j].id })
	out := make([]gotato.Tool, len(tools))
	for i, o := range tools {
		out[i] = o.tool
	}
	return out, nil
}

// offeredTool is a Tool the Session is offered, with its ID.
type offeredTool struct {
	tool gotato.Tool
	id   string
}

// offer appends to tools the Tools of list that are within the Agent's
// MaxEffect and active for the Session.
func (t *sessionTools) offer(tools []offeredTool, list []gotato.Tool) ([]offeredTool, error) {
	for _, tool := range list {
		spec, err := specOf(tool)
		if err != nil {
			return nil, err
		}
		if t.allowed(spec) && t.active(spec.ID) {
			tools = append(tools, offeredTool{tool, spec.ID})
		}
	}
	return tools, nil
}

// specOf reads a Tool's Spec, and a panic in it is an extension failure.
// [LIVENESS] a Tool of the Agent that panics in Spec fails the call that lists it, as it fails a Session's, and does not reach net/http's recover.
func specOf(tool gotato.Tool) (spec gotato.ToolSpec, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = gotato.ErrorOf(gotato.CodeExtensionFailure, "host: a Tool's Spec panicked")
		}
	}()
	return tool.Spec(), nil
}

// allowed reports whether a Tool is within the Agent's MaxEffect.
func (t *sessionTools) allowed(spec gotato.ToolSpec) bool {
	return t.spec.MaxEffect == gotato.ToolEffectUnspecified || spec.Effect.Within(t.spec.MaxEffect)
}

func (t *sessionTools) active(id string) bool {
	if t.session == nil {
		return true
	}
	value, _ := t.session.Get(metaToolPrefix + id)
	return value != "inactive"
}

// Tools describes the Tool surface of an Agent as a Session sees it.
// sessionID may be empty for the Agent's default surface. A Tool above the
// Agent's MaxEffect is not listed.
func (r *Runner) Tools(ctx context.Context, agent, sessionID string) ([]ToolEntry, error) {
	s, spec, err := r.openForTools(ctx, agent, sessionID)
	if err != nil {
		return nil, err
	}
	filter := &sessionTools{spec: spec, session: s}
	entries, err := agentToolEntries(ctx, spec, filter)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return entries, nil
	}
	return r.addLiveToolEntries(ctx, spec, s, filter, entries)
}

// openForTools opens the Record of sessionID, unless it is empty, and finds the
// AgentProfile: the one named, else the Session's.
func (r *Runner) openForTools(ctx context.Context, agent, sessionID string) (*record.Record, profile.AgentProfile, error) {
	var s *record.Record
	if sessionID != "" {
		var err error
		s, err = store.Open(ctx, r.store.Records, sessionID)
		if err != nil {
			return nil, profile.AgentProfile{}, storeFailureOf(ctx, err, true)
		}
	}
	var spec profile.AgentProfile
	var err error
	if agent == "" && s != nil {
		spec, err = r.agentOf(ctx, s)
	} else {
		spec, err = r.agentNamed(ctx, agent)
	}
	return s, spec, err
}

// agentToolEntries lists the Tools the AgentProfile offers, as a Session sees
// them, and refuses a Tool ID that two parts of the AgentProfile supply.
func agentToolEntries(ctx context.Context, spec profile.AgentProfile, filter *sessionTools) ([]ToolEntry, error) {
	all := &sessionTools{spec: spec}
	entries := make([]ToolEntry, 0)
	offered, err := all.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for _, tool := range offered {
		spec, err := specOf(tool)
		if err != nil {
			return nil, err
		}
		// [CONTRACT] the listing refuses the surface a Session refuses: a Tool ID two parts of the Agent supply.
		if slices.ContainsFunc(entries, func(e ToolEntry) bool { return e.Spec.ID == spec.ID }) {
			return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "host: Agent "+spec.Name+" has two Tools with ID "+spec.ID)
		}
		entries = append(entries, ToolEntry{Spec: spec, Active: filter.active(spec.ID)})
	}
	return entries, nil
}

// addLiveToolEntries adds what the live Session of a Record holds to entries.
func (r *Runner) addLiveToolEntries(ctx context.Context, spec profile.AgentProfile, s *record.Record, filter *sessionTools, entries []ToolEntry) ([]ToolEntry, error) {
	// The live Session says what the Agent's ToolSets hold: every tool of every ToolSet, loaded or not,
	// switched off or not, and search_tools, whose description is the catalog.
	toolCtx, cancel := r.toolSetContext(ctx)
	defer cancel()
	live, _, err := r.buildSession(toolCtx, spec, s, buildConfig{})
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
	read, err := store.Open(ctx, r.store.Records, sessionID)
	if err != nil {
		return ToolEntry{}, storeFailureOf(ctx, err, true)
	}
	spec, err := r.agentOf(ctx, read)
	if err != nil {
		return ToolEntry{}, err
	}
	found, result, err := r.lookupTool(ctx, spec, read, toolID)
	if err != nil {
		return ToolEntry{}, err
	}
	value := ""
	if !active {
		value = "inactive"
	}
	if err := r.writeToolSwitch(ctx, sessionID, spec, metaToolPrefix+toolID, value); err != nil {
		return ToolEntry{}, err
	}
	result.Spec, result.Active = *found, active
	return result, nil
}

// lookupTool finds toolID among the Agent's own Tools, then among its
// ToolSets' tools. It returns the tool's spec, and an entry with its ToolSet
// and whether it is loaded; an unknown tool is an error.
func (r *Runner) lookupTool(ctx context.Context, spec profile.AgentProfile, read *record.Record, toolID string) (*gotato.ToolSpec, ToolEntry, error) {
	var result ToolEntry
	found, err := findAgentTool(ctx, spec, toolID)
	if err != nil {
		return nil, ToolEntry{}, err
	}
	if found == nil {
		found, result.ToolSet, result.Loaded, err = r.findSetTool(ctx, spec, read, toolID)
		if err != nil {
			return nil, ToolEntry{}, err
		}
	}
	if found == nil {
		// Refused before the Session is claimed, so an unknown tool writes nothing.
		return nil, ToolEntry{}, gotato.ErrorOf(gotato.CodeInvalidArgument, "host: unknown tool "+toolID)
	}
	return found, result, nil
}

// findAgentTool looks for toolID among the Tools the Agent offers, nil when
// none has it.
func findAgentTool(ctx context.Context, spec profile.AgentProfile, toolID string) (*gotato.ToolSpec, error) {
	offered, err := (&sessionTools{spec: spec}).Tools(ctx)
	if err != nil {
		return nil, err
	}
	for _, tool := range offered {
		ts, err := specOf(tool)
		if err != nil {
			return nil, err
		}
		if ts.ID == toolID {
			return &ts, nil
		}
	}
	return nil, nil
}

// writeToolSwitch sets the Session's metadata key to value as its writer,
// under the Session lock.
func (r *Runner) writeToolSwitch(ctx context.Context, sessionID string, spec profile.AgentProfile, key, value string) error {
	return r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		s := writer.Record()
		held, _ := s.Get(metaAgent)
		r.publishOpened(writer, early, held)
		s.Set(key, value)
		err = r.releaseWriter(ctx, writer)
		if err == nil {
			r.keepWarm(writer, spec)
		}
		return err
	})
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
	return foreignError(gotato.CodeDeadlineExceeded, "host: the tool sets did not answer in time", cause)
}

// findSetTool looks for toolID among the tools of the Agent's ToolSets, which
// a live Session resolves. It reports the tool's spec (nil when no ToolSet
// holds it), its ToolSet and whether it is loaded. A name inside an
// unavailable ToolSet is an error that says so.
func (r *Runner) findSetTool(ctx context.Context, spec profile.AgentProfile, s *record.Record, toolID string) (*gotato.ToolSpec, string, bool, error) {
	toolCtx, cancel := r.toolSetContext(ctx)
	defer cancel()
	live, _, err := r.buildSession(toolCtx, spec, s, buildConfig{})
	if err != nil {
		if r.toolSetsTimedOut(ctx, err) {
			return nil, "", false, toolSetsTimeout(err)
		}
		return nil, "", false, err
	}
	defer func() { _ = live.Close(context.Background()) }()
	for _, set := range live.ToolSets() {
		if set.Unavailable && strings.HasPrefix(toolID, set.Name+".") {
			return nil, "", false, gotato.ErrorOf(gotato.CodeInvalidArgument, "host: tool set "+set.Name+" is unavailable now; "+toolID+" cannot be checked")
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
