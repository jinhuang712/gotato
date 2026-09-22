package gateway

import (
	"strings"
	"testing"
)

func TestFunctionNamesStayReadable(t *testing.T) {
	cases := map[string]string{
		"spawn_task":  "spawn_task",
		"read":        "read",
		"demo.echo":   "demo_echo",
		"fs/read-all": "fs_read-all",
	}
	for id, want := range cases {
		if got := gatewayFunctionName(id); got != want {
			t.Fatalf("gatewayFunctionName(%q) = %q, want %q", id, got, want)
		}
	}
	long := strings.Repeat("x", 100)
	if got := gatewayFunctionName(long); len(got) != maxFunctionName || !strings.HasPrefix(got, "xxxx") {
		t.Fatalf("long name = %q (%d)", got, len(got))
	}
	if got := gatewayFunctionName(""); got == "" || len(got) > maxFunctionName {
		t.Fatalf("empty id name = %q", got)
	}
}

func TestAssignFunctionNameResolvesCollisions(t *testing.T) {
	names := map[string]string{}
	a, err := assignFunctionName(names, "a_b")
	if err != nil || a != "a_b" {
		t.Fatalf("first = %q, %v", a, err)
	}
	b, err := assignFunctionName(names, "a.b")
	if err != nil || b == a || !strings.HasPrefix(b, "a_b_") || len(b) > maxFunctionName {
		t.Fatalf("colliding id got %q, %v", b, err)
	}
	// Stable within a request: the same ID maps to the same name again.
	if again, _ := assignFunctionName(names, "a.b"); again != b {
		t.Fatalf("second assignment = %q, want %q", again, b)
	}
	if names[a] != "a_b" || names[b] != "a.b" {
		t.Fatalf("reverse map = %v", names)
	}
}
