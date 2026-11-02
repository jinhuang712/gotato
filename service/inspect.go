package service

import (
	"context"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/store"
)

// InspectConfig overrides what one inspection shows, as a what-if: it changes
// neither the Session nor its Agent, and nothing a Run uses. The zero value
// overrides nothing.
type InspectConfig struct {
	Instruction string
}

// Inspect reports the Context a Run against the Session would send now. It
// builds the live Session a Run would build and asks it, so the report includes the
// Agent's Extensions, Tools and model options, and says whether the next Run
// compacts the Session first. config overrides the Agent's instruction for this
// inspection alone. The Session is not changed.
func (r *Runner) Inspect(ctx context.Context, sessionID string, config InspectConfig) (record.Report, error) {
	s, err := store.Open(ctx, r.store.Records, sessionID)
	if err != nil {
		return record.Report{}, storeFailureOf(ctx, err, true)
	}
	spec, err := r.agentOf(ctx, s)
	if err != nil {
		return record.Report{}, err
	}
	live, auto, err := r.buildSession(ctx, spec, s, nil, config)
	if err != nil {
		return record.Report{}, err
	}
	defer func() { _ = live.Close(context.Background()) }()
	inspection, err := live.InspectContext(ctx)
	if err != nil {
		return record.Report{}, err
	}
	report := record.ReportOf(inspection)
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	if auto != nil {
		report.CompactionPending = auto.PendingFor(inspection.Request)
		report.CompactCeilingTokens = auto.CeilingTokens()
	}
	return report, nil
}
