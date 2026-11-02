package model

import (
	"crypto/x509"
	"errors"
	"io"
	"testing"
)

func TestDefaultClassifierJudgesEveryKindOfFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure Failure
		want    Verdict
	}{
		{"rate limit", Failure{Status: 429, Message: "slow down"}, Verdict{Retryable: true}},
		{"overload status", Failure{Status: 529, Message: "busy"}, Verdict{Retryable: true}},
		{"spent quota", Failure{Status: 429, Message: "You exceeded your current quota: insufficient_quota"}, Verdict{}},
		{"bad request", Failure{Status: 400, Message: "messages must not be empty"}, Verdict{}},
		{"overflow with limit", Failure{Status: 400, Message: "prompt is too long: 210000 tokens > 200000 maximum"}, Verdict{Overflow: true, Limit: 200000}},
		{"overflow by code", Failure{Status: 413, Message: "Bad request", Code: "context_length_exceeded"}, Verdict{Overflow: true}},
		{"overflow phrase on another status", Failure{Status: 500, Message: "maximum context length is 8192 tokens"}, Verdict{Retryable: true}},
		{"transport", Failure{Message: transportMessage, Err: io.ErrUnexpectedEOF}, Verdict{Retryable: true}},
		{"untrusted certificate", Failure{Message: transportMessage, Err: x509.UnknownAuthorityError{}}, Verdict{}},
		{"stream overloaded", Failure{Message: "Overloaded", Type: "overloaded_error"}, Verdict{Retryable: true}},
		{"stream invalid request", Failure{Message: "bad", Type: "invalid_request_error"}, Verdict{}},
		{"stream overflow", Failure{Message: "the context window was exceeded during the reply", Code: "model_context_window_exceeded"}, Verdict{Overflow: true}},
	} {
		if got := DefaultClassifier(tc.failure); got != tc.want {
			t.Errorf("%s: verdict = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestAFailureIsAnErrorThatKeepsItsCause(t *testing.T) {
	var err error = Failure{Message: "stream interrupted", Err: io.ErrUnexpectedEOF}
	var failure Failure
	if !errors.As(err, &failure) || !errors.Is(err, io.ErrUnexpectedEOF) || err.Error() != "stream interrupted" {
		t.Fatalf("err = %v", err)
	}
	if got := newError(failure, Verdict{Retryable: true}); !got.Retryable() || got.Message != "stream interrupted" {
		t.Fatalf("error = %+v", got)
	}
}
