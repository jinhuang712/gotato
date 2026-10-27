package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// errorResponse is the JSON body of every error answer.
type errorResponse struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	// NotRun says the request did not run, so it can be sent again, also to
	// another instance.
	NotRun bool `json:"not_run,omitempty"`
	// Retryable says the same request, sent again later, may succeed.
	Retryable bool `json:"retryable,omitempty"`
	// RecordEpoch is the Record's current epoch, on a refusal to resume a
	// stream, for the client to start over with.
	RecordEpoch string `json:"record_epoch,omitempty"`
	// Writer is, on not_writer, the address the Run's writer advertises.
	Writer string `json:"writer,omitempty"`
}

// statusOfCode is the HTTP status of each ErrorCode. Every code is listed, so
// a new one cannot be left to a default by accident; the test over
// gotato.ErrorCodes fails when one is missing.
var statusOfCode = map[gotato.ErrorCode]int{
	gotato.CodeInvalidArgument:       http.StatusBadRequest,
	gotato.CodeNotSupported:          http.StatusBadRequest,
	gotato.CodeNotFound:              http.StatusNotFound,
	gotato.CodeSessionClosed:         http.StatusConflict,
	gotato.CodeSessionClosing:        http.StatusConflict,
	gotato.CodeInvalidState:          http.StatusConflict,
	gotato.CodeBusy:                  http.StatusConflict,
	gotato.CodeConflict:              http.StatusConflict,
	gotato.CodeCannotResume:          http.StatusConflict,
	gotato.CodeNotWriter:             http.StatusConflict,
	gotato.CodeStoreFailure:          http.StatusServiceUnavailable,
	gotato.CodeOverloaded:            http.StatusServiceUnavailable,
	gotato.CodeDraining:              http.StatusServiceUnavailable,
	gotato.CodeLimitExceeded:         http.StatusTooManyRequests,
	gotato.CodeContextOverflow:       http.StatusUnprocessableEntity,
	gotato.CodeDeadlineExceeded:      http.StatusGatewayTimeout,
	gotato.CodeCanceled:              http.StatusRequestTimeout,
	gotato.CodeModelFailure:          http.StatusInternalServerError,
	gotato.CodeModelProtocolFailure:  http.StatusInternalServerError,
	gotato.CodeToolResolutionFailure: http.StatusInternalServerError,
	gotato.CodeToolArgumentFailure:   http.StatusInternalServerError,
	gotato.CodeToolExecutionFailure:  http.StatusInternalServerError,
	gotato.CodeExtensionFailure:      http.StatusInternalServerError,
	gotato.CodeInternalInvariant:     http.StatusInternalServerError,
}

// StatusFor maps a runtime error to an HTTP status. It translates the error's
// code; it decides nothing about whether the request ran or may be retried,
// which the error says itself.
func StatusFor(err error) int {
	var runtimeErr *gotato.RuntimeError
	switch {
	case errors.As(err, &runtimeErr):
		if status, ok := statusOfCode[runtimeErr.Code]; ok {
			return status
		}
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	}
	return http.StatusInternalServerError
}

// codedBody is the part of an error body that both a failure and a Run that
// was not saved carry: the code and whether a retry may succeed. It returns
// the error's RuntimeError too, when it has one, for the fields that only a
// failure adds.
func codedBody(err error) (out errorResponse, runtimeErr *gotato.RuntimeError) {
	out.Retryable = gotato.Retryable(err)
	if errors.As(err, &runtimeErr) {
		out.Code = string(runtimeErr.Code)
	}
	return out, runtimeErr
}

// failureOf is the body of a failed request. A 5xx keeps the error's text in
// the server log and sends the status text, unless the request did not run.
func failureOf(err error) errorResponse {
	status := StatusFor(err)
	out, runtimeErr := codedBody(err)
	out.NotRun = gotato.NotRun(err)
	if runtimeErr != nil {
		out.RecordEpoch = runtimeErr.Details["record_epoch"]
		out.Writer = runtimeErr.Details["writer"]
	}
	if status >= http.StatusInternalServerError && !out.NotRun {
		// Keep store and filesystem internals in the server log; the client
		// gets a stable body.
		log.Printf("httpapi: %d %s: %v", status, http.StatusText(status), err)
		out.Message = http.StatusText(status)
		return out
	}
	out.Message = err.Error()
	return out
}

// notSavedStatus is the status of a Run whose result was not kept: 409 when
// another writer took the Session during the Run, 500 otherwise.
func notSavedStatus(err error) int {
	if errors.Is(err, session.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// notSaved is the body of a Run whose result was not kept. It names the code
// and whether a retry may succeed, and keeps the store's own message in the
// log.
func notSaved(err error) errorResponse {
	out, _ := codedBody(err)
	out.Message = internalError(err).Message
	return out
}

// writeFailure answers a failed Run or request: the status and body of the
// error, and the headers a client or load balancer acts on.
func writeFailure(w http.ResponseWriter, err error) {
	if gotato.NotRun(err) {
		w.Header().Set(NotRunHeader, "true")
	}
	// A Session busy on another instance says when to retry.
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		if writer := runtimeErr.Details["writer"]; writer != "" && runtimeErr.Code == gotato.CodeNotWriter {
			w.Header().Set(WriterHeader, writer)
		}
		if ms, convErr := strconv.ParseInt(runtimeErr.Details["retry_after_ms"], 10, 64); convErr == nil {
			w.Header().Set("Retry-After", strconv.FormatInt((ms+999)/1000, 10))
		}
	}
	writeJSON(w, StatusFor(err), failureOf(err))
}

// writeError answers with a plain message and the status the caller chose.
func writeError(w http.ResponseWriter, status int, err error) {
	var resp errorResponse
	if status >= http.StatusInternalServerError {
		resp = internalError(err)
	} else {
		resp.Message = err.Error()
	}
	writeJSON(w, status, resp)
}

// internalError logs the detailed cause and returns a stable body for a 5xx
// response, so store or filesystem internals never reach the client.
func internalError(err error) errorResponse {
	log.Printf("httpapi: internal error: %v", err)
	message := http.StatusText(http.StatusInternalServerError)
	return errorResponse{Message: message}
}
