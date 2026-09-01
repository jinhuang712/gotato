package gotato

import (
	"context"
	"errors"
	"fmt"
)

type ErrorCode string

const (
	ErrInvalidArgument       ErrorCode = "invalid_argument"
	ErrInvalidState          ErrorCode = "invalid_state"
	ErrBusy                  ErrorCode = "busy"
	ErrAgentClosed           ErrorCode = "agent_closed"
	ErrAgentClosing          ErrorCode = "agent_closing"
	ErrModelFailure          ErrorCode = "model_failure"
	ErrModelProtocolFailure  ErrorCode = "model_protocol_failure"
	ErrToolResolutionFailure ErrorCode = "tool_resolution_failure"
	ErrToolArgumentFailure   ErrorCode = "tool_argument_validation_failure"
	ErrToolExecutionFailure  ErrorCode = "tool_execution_failure"
	ErrExtensionFailure      ErrorCode = "extension_failure"
	ErrLimitExceeded         ErrorCode = "limit_exceeded"
	ErrCancelled             ErrorCode = "cancelled"
	ErrDeadlineExceeded      ErrorCode = "deadline_exceeded"
	ErrInternalInvariant     ErrorCode = "internal_invariant_failure"
	ErrRetirementFailed      ErrorCode = "retirement_failed"
	ErrNotSupported          ErrorCode = "not_supported"
)

type RuntimeError struct {
	Code      ErrorCode         `json:"code"`
	Operation string            `json:"operation,omitempty"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Cause     error             `json:"-"`
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Operation == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Operation, e.Message)
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func runtimeError(code ErrorCode, operation, message string, cause error) *RuntimeError {
	return &RuntimeError{Code: code, Operation: operation, Message: message, Cause: cause}
}

func codeForContext(err error) ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	return ErrCancelled
}

func IsCode(err error, code ErrorCode) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re.Code == code
}
