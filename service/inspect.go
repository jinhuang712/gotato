package service

import (
	"context"

	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

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
