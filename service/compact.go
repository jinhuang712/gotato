package service

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
)

// Compact compacts a Session explicitly, under the Session lock, and closes a
// Segment (record.Compact), and merges the oldest Segments when they then pass
// their budget. It runs the Session's Agent's Compactor: its parts, its
// Recap writer, its time zone, the Model's window, the ceiling in force, the
// Segments' budget and the size of the kept tail. opts changes this call only:
// KeepMessages, MinTokens and a Recap writer, which like the Agent's gets the
// Agent's model when it names none. When the Agent's Model is sent only the
// current Round's reasoning (ModelFacts.RoundReasoning), the kept Messages lose
// their reasoning.
//
// A draining Runner refuses it with ErrDraining (not run): it would claim an
// idle Session for the length of a compaction, after the Instance said it
// leaves. A Session running on another live Instance is waited for, up to
// Heartbeat.Wait, and then refused with busy, as for a Run.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts record.CompactConfig) (record.Result, error) {
	if r.Draining() {
		return record.Result{}, ErrDraining
	}
	var result record.Result
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		agentName, _ := writer.Record().Get(metaAgent)
		r.publishOpened(writer, early, agentName)
		spec, err := r.agentOf(ctx, writer.Record())
		known := err == nil
		if err != nil && !errors.Is(err, ErrUnknownAgent) {
			_ = r.releaseWriter(ctx, writer)
			return err
		}
		var compactor record.Compactor
		var compactOpts []record.CompactOption
		if known {
			compactor = compactorFor(spec)
			if opts.Recap != nil {
				opts.Recap = withAgentModel(opts.Recap, spec.Model)
			}
			compactOpts = append(compactOpts, record.WithRecapContext(r.recapContext(spec, writer.Record())))
		}
		result, err = record.Compact(ctx, writer.Record(), compactor, opts, compactOpts...)
		releaseErr := r.releaseWriter(ctx, writer)
		if known && releaseErr == nil {
			r.keepWarm(writer, spec)
		}
		if err != nil {
			return err
		}
		return releaseErr
	})
	if err != nil {
		return record.Result{}, err
	}
	return result, nil
}

// withAgentModel gives a RecapWriter that names no Model the Agent's own
// (RecapWriter.WithAgentModel), so an Agent turns the model's Recap on with
// Recap: record.ModelRecap{}. A writer whose method panics is kept as it was:
// its own failure is then the compaction's to drop, not the Runner's to crash on.
func withAgentModel(writer record.RecapWriter, model gotato.Model) (out record.RecapWriter) {
	if writer == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			out = writer
		}
	}()
	return writer.WithAgentModel(model)
}

// recapContext returns what a Recap written by a model sends as its prefix:
// the request the Agent's next model call would send for the Session now,
// built by a live Session that is inspected and not run.
func (r *Runner) recapContext(spec profile.AgentProfile, rec *record.Record) func(context.Context) (gotato.ModelRequest, error) {
	return func(ctx context.Context) (gotato.ModelRequest, error) {
		live, _, err := r.buildSession(ctx, spec, rec, nil, InspectConfig{})
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

// compactorFor is the Agent's Compactor with the Model's window applied, and its
// Recap writer given the Agent's Model when it names none. The ceiling is the
// Agent's alone.
func compactorFor(spec profile.AgentProfile) record.Compactor {
	compactor := spec.Compact
	facts := spec.Model.Config().Facts
	compactor.Recap = withAgentModel(compactor.Recap, spec.Model)
	compactor.DropReasoning = compactor.DropReasoning || facts.RoundReasoning
	if compactor.WindowTokens <= 0 {
		compactor.WindowTokens = facts.Window.Tokens()
	}
	return compactor
}
