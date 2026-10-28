package workload

import (
	"errors"
	"sort"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// Kind is what failed.
type Kind string

// The kinds of failure: what did not go as the task expected.
const (
	KindSetup    Kind = "setup"     // the driver could not prepare or read back the Session
	KindRun      Kind = "run"       // a Run did not complete
	KindToolCall Kind = "tool_call" // a tool call returned a failed result
	KindCheck    Kind = "check"     // the answers missed the task's goal
)

// Cause is who a failure is attributed to.
type Cause string

const (
	// CauseModel means the model got a tool call wrong, or its answers missed the
	// task's goal.
	CauseModel Cause = "model"
	// CauseProvider means a model call failed after the gateway's retries, or its
	// reply could not be read.
	CauseProvider Cause = "provider"
	// CauseTool means the tool ran and failed.
	CauseTool Cause = "tool"
	// CauseStore means the store could not load or save the Session.
	CauseStore Cause = "store"
	// CauseLimit means a Run reached a limit or its deadline.
	CauseLimit Cause = "limit"
	// CauseExtension means an Extension failed or refused a tool call.
	CauseExtension Cause = "extension"
	// CauseCanceled means the caller canceled.
	CauseCanceled Cause = "canceled"
	// CauseGotato means everything else. GOALS.md G-G04 wants none.
	CauseGotato Cause = "gotato"
)

// Failure is one group of failures of the same kind, cause and code.
type Failure struct {
	Kind  Kind   `json:"kind"`
	Cause Cause  `json:"cause"`
	Code  string `json:"code,omitempty"`
	Count int    `json:"count"`
	// Example is the message of the first failure in the group.
	Example string `json:"example"`
}

type failure struct {
	kind    Kind
	cause   Cause
	code    string
	message string
}

// runFailure attributes a Run that did not complete.
func runFailure(result service.RunResponse, err error) (failure, bool) {
	switch {
	case errors.Is(err, service.ErrNotPersisted):
		return failure{kind: KindRun, cause: CauseStore, code: "not_persisted", message: err.Error()}, true
	case errors.Is(err, session.ErrNotFound):
		return failure{kind: KindRun, cause: CauseStore, code: "not_found", message: err.Error()}, true
	case err == nil && result.Result.Status == gotato.RunStatusCompleted:
		return failure{}, false
	}
	var code gotato.ErrorCode
	message := string(result.Result.Status)
	if result.Result.Error != nil {
		code, message = result.Result.Error.Code, result.Result.Error.Message
	} else if err != nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(err, &runtimeErr) {
			code = runtimeErr.Code
		}
		message = err.Error()
	}
	return failure{kind: KindRun, cause: causeOf(code), code: string(code), message: message}, true
}

// causeOf attributes a Run's error code.
func causeOf(code gotato.ErrorCode) Cause {
	switch code {
	case gotato.CodeModelFailure, gotato.CodeContextOverflow, gotato.CodeModelProtocolFailure:
		return CauseProvider
	case gotato.CodeToolExecutionFailure:
		return CauseTool
	case gotato.CodeToolResolutionFailure, gotato.CodeToolArgumentFailure:
		return CauseModel
	case gotato.CodeExtensionFailure:
		return CauseExtension
	case gotato.CodeLimitExceeded, gotato.CodeDeadlineExceeded:
		return CauseLimit
	case gotato.CodeCanceled:
		return CauseCanceled
	default:
		return CauseGotato
	}
}

// toolFailure attributes a tool call from its tool_execution_end Event: a
// call that failed before the tool ran was refused by Gotato for an unknown
// tool or bad arguments, so it is the model's; a tool that ran and failed is
// the tool's. The committed result, when there is one, gives the failure its
// text and code, and a result that reports an internal invariant is Gotato's.
func toolFailure(call toolCall, committed *gotato.ToolResult) (failure, bool) {
	f := failure{kind: KindToolCall, message: call.toolID + ": " + call.status}
	if committed != nil {
		f.message = committed.SafeError
		f.code = string(codePrefix(committed.SafeError))
	}
	switch {
	case call.status == string(gotato.ToolResultStatusOK):
		return failure{}, false
	case call.status == string(gotato.ToolResultStatusBlocked):
		f.cause = CauseExtension
	case call.status == string(gotato.ToolResultStatusCanceled):
		f.cause = CauseCanceled
	case f.code == string(gotato.CodeInternalInvariant):
		f.cause = CauseGotato
	case call.executed:
		f.cause = CauseTool
	default:
		f.cause = CauseModel
	}
	return f, true
}

// codePrefix reads the error code a Gotato error message starts with, as in
// "tool_argument_failure: …" or "tool_execution_failure (Tool): …".
func codePrefix(message string) gotato.ErrorCode {
	end := strings.IndexAny(message, ": (")
	if end <= 0 {
		return ""
	}
	switch code := gotato.ErrorCode(message[:end]); code {
	case gotato.CodeToolArgumentFailure, gotato.CodeToolResolutionFailure, gotato.CodeToolExecutionFailure, gotato.CodeInternalInvariant:
		return code
	}
	return ""
}

// failureSet groups failures by kind, cause and code, in the order first seen.
type failureSet struct {
	order  []string
	groups map[string]*Failure
}

func newFailureSet() *failureSet { return &failureSet{groups: map[string]*Failure{}} }

func (s *failureSet) add(f failure) {
	key := string(f.kind) + "/" + string(f.cause) + "/" + f.code
	group, ok := s.groups[key]
	if !ok {
		group = &Failure{Kind: f.kind, Cause: f.cause, Code: f.code, Example: f.message}
		s.groups[key] = group
		s.order = append(s.order, key)
	}
	group.Count++
}

func (s *failureSet) list() []Failure {
	out := make([]Failure, 0, len(s.order))
	for _, key := range s.order {
		out = append(out, *s.groups[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
