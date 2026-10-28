package service

import (
	"context"
	"strconv"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// Compact compacts a Session explicitly, under the Session lock, and closes a
// Segment (modelctx.Compact), and merges the oldest Segments when they then pass
// their budget. What opts leaves unset comes from the Session's
// Agent: its Recap writer, its time zone and limits, the Model's window, the
// ceiling in force, the Segments' budget and the size of the kept tail
// (SegmentSettings.TailTokens). When the
// Agent's Model is sent only the current Round's reasoning
// (modelctx.DropsReasoning), the kept Messages lose their reasoning.
//
// A draining Runner refuses it with ErrDraining (not run): it would claim an
// idle Session for the length of a compaction, after the Instance said it
// leaves. A Session running on another live Instance is waited for, up to
// Liveness.Wait, and then refused with busy, as for a Run.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts modelctx.CompactConfig) (modelctx.Result, error) {
	if r.Draining() {
		return modelctx.Result{}, ErrDraining
	}
	var result modelctx.Result
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		agentName, _ := writer.Record().Get(MetaAgent)
		r.publishOpened(writer, early, agentName)
		spec, known := r.Agent(agentName)
		var compactOpts []modelctx.CompactOption
		if known {
			opts = compactOptionsFor(spec, writer.Record(), opts)
			if opts.Recap != nil {
				compactOpts = append(compactOpts, modelctx.WithRecapContext(r.recapContext(spec, writer.Record())))
			}
		}
		result, err = modelctx.Compact(ctx, writer.Record(), opts, compactOpts...)
		releaseErr := writer.Release(ctx)
		if known && releaseErr == nil {
			r.keepWarm(writer, spec)
		}
		if err != nil {
			return err
		}
		return releaseErr
	})
	if err != nil {
		return modelctx.Result{}, err
	}
	return result, nil
}

// compactOptionsFor fills what opts leaves unset from the Agent's compaction
// settings and its Model.
func compactOptionsFor(spec Agent, record *session.Record, opts modelctx.CompactConfig) modelctx.CompactConfig {
	policy := compactPolicyFor(spec, record)
	opts.DropReasoning = opts.DropReasoning || policy.DropReasoning
	if opts.Recap == nil {
		opts.Recap = policy.Recap
	}
	opts.Recap = withAgentModel(opts.Recap, spec.Model)
	if opts.WindowTokens <= 0 {
		opts.WindowTokens = policy.WindowTokens
	}
	if opts.CeilingTokens <= 0 {
		opts.CeilingTokens = policy.EffectiveCeilingTokens()
	}
	if opts.Location == nil {
		opts.Location = policy.Location
	}
	if opts.SegmentBudgetTokens <= 0 {
		opts.SegmentBudgetTokens = policy.SegmentBudgetTokens
	}
	opts.MessageTokens = cmpPositive(opts.MessageTokens, policy.MessageTokens)
	opts.ToolLines = cmpPositive(opts.ToolLines, policy.ToolLines)
	opts.ArgChars = cmpPositive(opts.ArgChars, policy.ArgChars)
	opts.TailTokens = cmpPositive(opts.TailTokens, policy.TailTokens)
	return opts
}

func cmpPositive(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// withAgentModel gives a modelctx.ModelRecap that names no Model the Agent's
// own, so an Agent turns the model's Recap on with Recap: modelctx.ModelRecap{}.
func withAgentModel(writer modelctx.RecapWriter, model gotato.Model) modelctx.RecapWriter {
	if recap, ok := writer.(modelctx.ModelRecap); ok && recap.Model == nil {
		recap.Model = model
		return recap
	}
	return writer
}

// recapContext returns what a Recap written by a model sends as its prefix:
// the request the Agent's next model call would send for the Session now,
// built by a live Session that is inspected and not run.
func (r *Runner) recapContext(spec Agent, record *session.Record) func(context.Context) (gotato.ModelRequest, error) {
	return func(ctx context.Context) (gotato.ModelRequest, error) {
		live, _, err := r.buildSession(ctx, spec, record, nil, InspectConfig{})
		if err != nil {
			return gotato.ModelRequest{}, err
		}
		defer func() { _ = live.Close(context.Background()) }()
		inspection, err := live.InspectContext(ctx)
		if err != nil {
			return gotato.ModelRequest{}, err
		}
		return inspection.Request, nil
	}
}

// compactPolicyFor is the Agent's CompactPolicy with the Model's window and
// the Session's own ceiling (MetaCompactCeiling) applied.
func compactPolicyFor(spec Agent, record *session.Record) modelctx.CompactPolicy {
	policy := spec.Compact
	policy.Recap = withAgentModel(policy.Recap, spec.Model)
	policy.DropReasoning = policy.DropReasoning || modelctx.DropsReasoning(spec.Model)
	if policy.WindowTokens <= 0 {
		policy.WindowTokens = modelctx.WindowOf(spec.Model)
	}
	if raw, ok := record.Get(MetaCompactCeiling); ok {
		if ceiling, err := strconv.Atoi(raw); err == nil && ceiling > 0 {
			policy.CeilingTokens = ceiling
		}
	}
	return policy
}
