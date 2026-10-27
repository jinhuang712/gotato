package grpcadapter

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

// A new ErrorCode must be given a gRPC code on purpose: this fails until the
// table maps it.
func TestEveryErrorCodeHasAnExplicitStatus(t *testing.T) {
	for _, code := range gotato.ErrorCodes() {
		if _, ok := codeOf[code]; !ok {
			t.Errorf("error code %q has no gRPC code", code)
		}
	}
	for code := range codeOf {
		var listed bool
		for _, known := range gotato.ErrorCodes() {
			listed = listed || known == code
		}
		if !listed {
			t.Errorf("the code table names %q, which gotato.ErrorCodes does not list", code)
		}
	}
}

func TestStatusOfTranslatesTheCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{service.ErrDraining, codes.Unavailable},
		{service.ErrOverloaded, codes.Unavailable},
		{service.ErrBusy, codes.FailedPrecondition},
		{session.ErrNotFound, codes.NotFound},
		{gotato.ErrorOf(gotato.ErrStoreFailure, "x"), codes.Unavailable},
		{gotato.ErrorOf(gotato.ErrInvalidState, "x"), codes.FailedPrecondition},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
	} {
		if got := status.Code(statusOf(tc.err)); got != tc.want {
			t.Errorf("statusOf(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// A draining instance refuses a call with UNAVAILABLE and says in its trailer
// that the call did not run and may be sent elsewhere; a missing Session is
// NOT_FOUND, not run, and not worth sending again.
func TestRefusalsCarryNotRunAndRetryable(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)
	ctx := context.Background()

	var trailer metadata.MD
	_, err = client.Run(ctx, &gotatov1.RunRequest{SessionId: "missing", Input: &gotatov1.RunRequest_Prompt{Prompt: "hi"}}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.NotFound || len(trailer.Get(NotRunKey)) != 1 || len(trailer.Get(RetryableKey)) != 0 {
		t.Fatalf("Run on a missing Session = %v, trailer %v; want NOT_FOUND, not run, not retryable", err, trailer)
	}

	trailer = nil
	_, err = client.GetSession(ctx, &gotatov1.SessionRequest{SessionId: "missing"}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.NotFound || len(trailer.Get(NotRunKey)) != 1 {
		t.Fatalf("GetSession of a missing Session = %v, trailer %v; want NOT_FOUND, not run", err, trailer)
	}

	if err := runner.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	trailer = nil
	_, err = client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "hi"}}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.Unavailable || len(trailer.Get(NotRunKey)) != 1 || len(trailer.Get(RetryableKey)) != 1 {
		t.Fatalf("Run on a draining instance = %v, trailer %v; want UNAVAILABLE, not run, retryable", err, trailer)
	}
}
