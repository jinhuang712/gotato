package orchestration

import (
	"context"
	"sync"

	gotato "github.com/jinhuang712/gotato"
)

// Provenance correlates a spawned Agent with the Run that asked for it.
//
// It is correlation only. It grants no ownership, no shared state, and no
// cancellation inheritance: the child has its own identity, lifecycle, limits,
// and Event sequence, and the parent settling does not close it.
type Provenance struct {
	SpawnID       gotato.SpawnID `json:"spawn_id,omitempty"`
	OriginAgentID gotato.AgentID `json:"origin_agent_id,omitempty"`
	OriginRunID   gotato.RunID   `json:"origin_run_id,omitempty"`
}

// SpawnRequest asks Orchestration for an independent Agent Routine. An empty
// ConversationKey gets a generated one, so every spawn is its own Conversation
// rather than a second live Agent on somebody else's.
type SpawnRequest struct {
	Request
	Origin Provenance
}

// Spawn creates an independent Agent Routine and records where it came from.
//
// The factory lives here rather than in Core: Core owns one Agent's execution,
// and creating another Agent is coordination. A Core Agent reaches this path
// through an application capability, never by constructing an Agent itself.
func (o *Orchestrator) Spawn(ctx context.Context, request SpawnRequest) (gotato.Agent, Record, error) {
	if err := contextError(ctx); err != nil {
		return nil, Record{}, err
	}
	provenance := request.Origin
	if provenance.SpawnID == "" {
		provenance.SpawnID = gotato.SpawnID(nextID("spawn", &o.seq))
	}
	inner := request.Request
	if inner.ConversationID == "" && inner.ConversationKey == "" {
		inner.ConversationKey = gotato.ConversationKey("spawn:" + string(provenance.SpawnID))
	}
	agent, record, err := o.resolveWithProvenance(ctx, inner, &provenance)
	if err != nil {
		return nil, record, err
	}
	return agent, record, nil
}

// GroupPolicy decides when a group stops waiting on its members.
//
// No policy cancels a sibling. A group is a coordination pattern over
// independent Routines, not an ownership hierarchy: a member that is still
// running when the group returns keeps running. Set Group.CancelSiblings to
// ask for cancellation explicitly.
type GroupPolicy string

const (
	// CollectAll waits for every member and fails the group if any failed.
	CollectAll GroupPolicy = "collect_all"
	// CollectPartial waits for every member and never fails the group; the
	// caller reads each outcome.
	CollectPartial GroupPolicy = "collect_partial"
	// FailFast stops waiting at the first failure and reports it.
	FailFast GroupPolicy = "fail_fast"
	// FirstSuccess stops waiting at the first success and fails only when
	// every member failed.
	FirstSuccess GroupPolicy = "first_success"
)

// GroupTask is one member of a group.
type GroupTask struct {
	Request Request
	Message gotato.Message
}

// GroupOutcome is what one member produced. Settled is false when the group
// stopped waiting before this member finished; that member is still running.
type GroupOutcome struct {
	Index   int
	Record  Record
	Result  gotato.RunResult
	Err     error
	Settled bool
}

// Group configures how a set of independent Runs is coordinated.
type Group struct {
	Policy GroupPolicy
	// CancelSiblings cancels the members still running when the policy stops
	// waiting. It is off by default because provenance and grouping do not
	// imply lifetime ownership.
	CancelSiblings bool
}

// RunGroup dispatches every task concurrently and applies the group policy.
// Outcomes are returned in task order regardless of completion order.
func (o *Orchestrator) RunGroup(ctx context.Context, group Group, tasks []GroupTask) ([]GroupOutcome, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	policy := group.Policy
	if policy == "" {
		policy = CollectAll
	}

	memberCtx := ctx
	var cancelMembers context.CancelFunc
	if group.CancelSiblings {
		memberCtx, cancelMembers = context.WithCancel(ctx)
		defer cancelMembers()
	}

	// The channel is buffered for every member, so a member that finishes
	// after the group stopped waiting still completes instead of blocking.
	settled := make(chan GroupOutcome, len(tasks))
	var running sync.WaitGroup
	for index, task := range tasks {
		running.Add(1)
		go func(index int, task GroupTask) {
			defer running.Done()
			result, record, err := o.Dispatch(memberCtx, task.Request, task.Message)
			settled <- GroupOutcome{Index: index, Record: record, Result: result, Err: err, Settled: true}
		}(index, task)
	}

	outcomes := make([]GroupOutcome, len(tasks))
	for i := range outcomes {
		outcomes[i] = GroupOutcome{Index: i}
	}

	var groupErr error
	pending := len(tasks)
	stopped := false
	for pending > 0 && !stopped {
		select {
		case outcome := <-settled:
			outcomes[outcome.Index] = outcome
			pending--
			switch policy {
			case FailFast:
				if outcome.Err != nil {
					groupErr = outcome.Err
					stopped = true
				}
			case FirstSuccess:
				if outcome.Err == nil {
					stopped = true
				}
			}
		case <-ctx.Done():
			groupErr = contextError(ctx)
			stopped = true
		}
	}

	if stopped && group.CancelSiblings && cancelMembers != nil {
		cancelMembers()
	}
	if !stopped {
		// Every member settled. Wait for the goroutines so the group does not
		// outlive its own bookkeeping.
		running.Wait()
	}

	switch policy {
	case CollectAll:
		for _, outcome := range outcomes {
			if outcome.Err != nil {
				groupErr = outcome.Err
				break
			}
		}
	case FirstSuccess:
		if groupErr == nil && !anySucceeded(outcomes) {
			groupErr = gotatoError(gotato.ErrInvalidState, "every group member failed")
		}
	case CollectPartial:
		groupErr = nil
	}
	return outcomes, groupErr
}

func anySucceeded(outcomes []GroupOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Settled && outcome.Err == nil {
			return true
		}
	}
	return false
}
