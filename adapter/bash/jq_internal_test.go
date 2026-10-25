package bash

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// A program that grows the heap past its limit, or runs past its time limit,
// is stopped by Run itself, with an error that says which. The limits are
// lowered so the test is quick.
func TestJQLimitsStopAProgram(t *testing.T) {
	oldHeap, oldTime := jqHeapLimit, jqRuntimeLimit
	defer func() { jqHeapLimit, jqRuntimeLimit = oldHeap, oldTime }()
	jqHeapLimit, jqRuntimeLimit = 64<<20, 300*time.Millisecond

	for _, tc := range []struct{ program, want string }{
		{"jq -n '[range(1e9)]|length'", "grew the heap"},
		{"jq -n '[repeat(\"some text\")]|length'", "grew the heap"},
		{"jq -n 'repeat(1)|empty'", "ran for more than"},
		{"jq -n 'last(repeat(1))'", "ran for more than"},
	} {
		jqHeapLimit, jqRuntimeLimit = 64<<20, 20*time.Second
		if strings.Contains(tc.want, "ran for") {
			jqHeapLimit, jqRuntimeLimit = 4<<30, 300*time.Millisecond
		}
		start := time.Now()
		result, err := Run(context.Background(), tc.program, fstest.MapFS{})
		if err != nil || result.ExitCode != 5 || !strings.Contains(result.Stderr, tc.want) {
			t.Errorf("%s: %+v %v (want exit 5 and %q)", tc.program, result, err, tc.want)
		}
		if time.Since(start) > 10*time.Second {
			t.Errorf("%s took %v", tc.program, time.Since(start))
		}
	}
}
