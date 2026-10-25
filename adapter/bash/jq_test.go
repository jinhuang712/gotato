package bash_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/adapter/bash"
)

// A jq program that does not end is stopped: by ctx, by the bound on output,
// by the bound on the heap, and at last by its own time limit. Each ends
// promptly with a clear error and never hangs.
func TestJQRunawayProgramsEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		program string
		stderr  string
	}{
		{"repeat without output", "jq -n 'repeat(1)|empty'", ""},
		{"last of an endless generator", "jq -n 'last(repeat(1))'", ""},
		{"a huge range collected", "jq -n -c '[range(1e9)]|length'", "heap"},
		{"an endless array", "jq -n '[repeat([1,2,3,4,5,6,7,8])]|length'", "heap"},
		{"reduce over a huge range", "jq -n 'reduce range(1e18) as $i (0;.+1)'", ""},
		{"a huge string", `jq -n '"x" * 1e10|length'`, "repeat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			result, err := bash.Run(ctx, tc.program, mapFS(nil))
			if took := time.Since(start); took > 10*time.Second {
				t.Fatalf("took %v", took)
			}
			switch {
			case err != nil:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error %v", err)
				}
			case result.ExitCode == 0 || !strings.Contains(result.Stderr, tc.stderr):
				t.Fatalf("%+v: want a failure that says %q", result, tc.stderr)
			}
		})
	}
}

// An endless stream of output is stopped by the bound on output, with the first
// bytes kept and Truncated set.
func TestJQOutputBound(t *testing.T) {
	start := time.Now()
	result := run(t, "jq -n 'repeat(\"line\")'", nil, bash.WithMaxOutput(1000))
	if !result.Truncated || len(result.Stdout) != 1000 || !strings.HasPrefix(result.Stdout, "\"line\"\n") {
		t.Fatalf("%d bytes, truncated %v", len(result.Stdout), result.Truncated)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("slow")
	}
}

// ctx ends a program that is running, and Run returns its error.
func TestJQHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, err := bash.Run(ctx, "jq -n 'repeat(1)|empty'", mapFS(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

// Input that is too large to hold is an error that says so, not an
// unbounded read.
func TestJQInputBound(t *testing.T) {
	big := strings.Repeat(" ", bash.MaxJQInputBytes+1)
	result := run(t, "jq . /big", map[string]string{"big": big})
	if result.ExitCode != 2 || !strings.Contains(result.Stderr, "too large to hold in memory") {
		t.Fatalf("%+v", result)
	}
}
