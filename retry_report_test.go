package gotato

import (
	"context"
	"testing"
)

func TestReportModelRetryTellsWhetherASessionHeardIt(t *testing.T) {
	if ReportModelRetry(context.Background(), ModelRetry{Attempt: 1}) {
		t.Fatal("a context from no Session was reported as heard")
	}
	var heard ModelRetry
	ctx := withRetryReporter(context.Background(), func(retry ModelRetry) { heard = retry })
	if !ReportModelRetry(ctx, ModelRetry{Attempt: 2}) || heard.Attempt != 2 {
		t.Fatalf("heard = %+v", heard)
	}
}
