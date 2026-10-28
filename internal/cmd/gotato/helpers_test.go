package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// harness runs the CLI in-process against a temporary store.
type harness struct {
	t    *testing.T
	home string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, home: t.TempDir()}
}

func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		switch key {
		case "GOTATO_HOME":
			return h.home
		case "GOTATO_GATEWAY_CONFIG":
			return filepath.Join(h.home, "missing-gateway.yaml")
		}
		return ""
	}
	code := Main(args, strings.NewReader(""), &stdout, &stderr, getenv)
	return code, stdout.String(), stderr.String()
}

func (h *harness) mustJSON(out string, into any) {
	h.t.Helper()
	if err := json.Unmarshal([]byte(out), into); err != nil {
		h.t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
}

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	code, out, errOut := h.run(args...)
	if code != ExitOK {
		h.t.Fatalf("%v exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}
