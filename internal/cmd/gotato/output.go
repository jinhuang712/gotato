package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

// emit prints value as JSON in machine mode, or human text otherwise.
func (c *cli) emit(value any, human string) int {
	if c.machine() {
		return c.writeJSON(value)
	}
	if !c.quiet && human != "" {
		fmt.Fprintln(c.stdout, human)
	}
	return ExitOK
}

func (c *cli) writeJSON(value any) int {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	if !c.jsonl {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(value); err != nil {
		return c.fail(ExitError, "encode: "+err.Error())
	}
	return ExitOK
}

func (c *cli) writeJSONL(value any) error {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

// writeErrorObject mirrors writeJSON: pretty in --json mode, compact in
// --jsonl mode, so error and data shapes look the same.
//
// The object has "error" and "exit_code" always. "not_run" and "retryable"
// are always there too, as the runtime's error says them: not_run means
// nothing with side effects happened, so the same command may be run again
// elsewhere; retryable means the same command, run again later, may succeed.
// "code" names the runtime's ErrorCode, when the failure has one.
func (c *cli) writeErrorObject(code int, message string, err error) {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	if !c.jsonl {
		enc.SetIndent("", "  ")
	}
	object := errorObject{Error: message, ExitCode: code, NotRun: gotato.NotRun(err), Retryable: gotato.Retryable(err)}
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		object.Code = runtimeErr.Code
		if runtimeErr.Code == gotato.CodeNotWriter {
			object.Writer = runtimeErr.Details["writer"]
		}
	}
	_ = enc.Encode(object)
}

// errorObject is the JSON a failed command writes to stdout in machine mode.
type errorObject struct {
	Error     string           `json:"error"`
	ExitCode  int              `json:"exit_code"`
	Code      gotato.ErrorCode `json:"code,omitempty"`
	NotRun    bool             `json:"not_run"`
	Retryable bool             `json:"retryable"`
	// Writer is, for not_writer, the address the Run's writer advertises.
	Writer string `json:"writer,omitempty"`
}

func (c *cli) info(format string, args ...any) {
	if !c.quiet {
		fmt.Fprintf(c.stderr, format+"\n", args...)
	}
}

func (c *cli) machine() bool { return c.json || c.jsonl }
