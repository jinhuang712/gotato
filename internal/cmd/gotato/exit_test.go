package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
)

// defaultsOf says what an error of the code says about its request when it is
// made: gotato.ErrorOf for the core's codes, the service's own errors for its
// codes (not_writer is made with an address and is not run and retryable, as
// draining and overloaded are).
func defaultsOf(code gotato.ErrorCode) *gotato.RuntimeError {
	var sentinel *gotato.RuntimeError
	switch code {
	case service.CodeDraining:
		errors.As(service.ErrDraining, &sentinel)
	case service.CodeOverloaded:
		errors.As(service.ErrOverloaded, &sentinel)
	case service.CodeCannotResume:
		errors.As(service.ErrCannotResume, &sentinel)
	case service.CodeAlreadyExists:
		errors.As(service.ErrSessionExists, &sentinel)
	case service.CodeNotWriter:
		sentinel = &gotato.RuntimeError{Code: code, NotRun: true, Retryable: true}
	default:
		sentinel = gotato.ErrorOf(code, "x")
	}
	return sentinel
}

// RUNBOOK.md keeps the one table of error codes: not run, retryable, HTTP
// status and exit code. This fails when a row and the code differ, when a code
// has no row, or when a row names a code that does not exist.
func TestRunbookErrorTableMatchesTheCode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "RUNBOOK.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[gotato.ErrorCode]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 6 || !strings.HasPrefix(strings.TrimSpace(cells[0]), "`") {
			continue
		}
		code := gotato.ErrorCode(strings.Trim(strings.TrimSpace(cells[0]), "`"))
		known := false
		for _, c := range append(gotato.ErrorCodes(), service.ErrorCodes()...) {
			known = known || c == code
		}
		if !known {
			continue // another table that starts with a code-like cell
		}
		seen[code] = true
		def := defaultsOf(code)
		for i, want := range []bool{def.NotRun, def.Retryable} {
			cell := strings.TrimSpace(cells[1+i])
			switch {
			case strings.HasPrefix(cell, "by cause"):
			case cell == "yes" || cell == "no":
				if (cell == "yes") != want {
					t.Errorf("%s: column %d says %q, the runtime's default is %v", code, 1+i, cell, want)
				}
			default:
				t.Errorf("%s: column %d = %q, want yes, no or by cause", code, 1+i, cell)
			}
		}
		if status, _ := strconv.Atoi(strings.TrimSpace(cells[3])); status != httpapi.StatusFor(def) {
			t.Errorf("%s: the table says HTTP %s, httpapi says %d", code, cells[3], httpapi.StatusFor(def))
		}
		if exit, _ := strconv.Atoi(strings.TrimSpace(cells[4])); exit != exitOfCode[code] {
			t.Errorf("%s: the table says exit %s, the tool exits %d", code, cells[4], exitOfCode[code])
		}
	}
	for _, code := range append(gotato.ErrorCodes(), service.ErrorCodes()...) {
		if !seen[code] {
			t.Errorf("RUNBOOK.md has no row for %s", code)
		}
	}
}
