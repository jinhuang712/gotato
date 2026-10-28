package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

// Exit codes of the test tool.
const (
	ExitOK            = 0
	ExitError         = 1
	ExitUsage         = 2
	ExitNotFound      = 3
	ExitRunIncomplete = 4
)

// fail reports an error on stderr (and as JSON on stdout in machine mode so
// scripts see one shape) and returns the code.
func (c *cli) fail(code int, message string) int {
	return c.failWith(code, message, nil)
}

// failWith is fail for an error: in machine mode the error object also names
// the runtime's code and what it says about a retry.
func (c *cli) failWith(code int, message string, err error) int {
	fmt.Fprintln(c.stderr, "gotato: "+message)
	if c.machine() {
		c.writeErrorObject(code, message, err)
	}
	return code
}

// usageError reports a usage mistake. In machine mode it also writes the
// documented error object to stdout, so a script never gets empty output.
func (c *cli) usageError(message string) int {
	fmt.Fprintln(c.stderr, "gotato: "+message)
	fmt.Fprintln(c.stderr, "run 'gotato help' for usage")
	if c.machine() {
		c.writeErrorObject(ExitUsage, message, gotato.ErrorOf(gotato.CodeInvalidArgument, message))
	}
	return ExitUsage
}

// parseError turns a flag parse failure into the right exit: --help asks for
// usage, anything else is a usage mistake.
func (c *cli) parseError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		c.usage()
		return ExitOK
	}
	return c.usageError(err.Error())
}

// exitOfCode is the exit code of each ErrorCode. Every code is listed, so a
// new one cannot be left to a default by accident; the test over
// gotato.ErrorCodes fails when one is missing. A failure that is not a
// RuntimeError exits 1.
var exitOfCode = map[gotato.ErrorCode]int{
	gotato.CodeInvalidArgument:       ExitUsage,
	gotato.CodeNotFound:              ExitNotFound,
	gotato.CodeInvalidState:          ExitError,
	gotato.CodeBusy:                  ExitError,
	gotato.CodeSessionClosed:         ExitError,
	gotato.CodeSessionClosing:        ExitError,
	gotato.CodeModelFailure:          ExitError,
	gotato.CodeContextOverflow:       ExitError,
	gotato.CodeModelProtocolFailure:  ExitError,
	gotato.CodeToolResolutionFailure: ExitError,
	gotato.CodeToolArgumentFailure:   ExitError,
	gotato.CodeToolExecutionFailure:  ExitError,
	gotato.CodeExtensionFailure:      ExitError,
	gotato.CodeLimitExceeded:         ExitError,
	gotato.CodeCanceled:              ExitError,
	gotato.CodeDeadlineExceeded:      ExitError,
	gotato.CodeInternalInvariant:     ExitError,
	gotato.CodeNotSupported:          ExitError,
	gotato.CodeConflict:              ExitError,
	gotato.CodeStoreFailure:          ExitError,
	gotato.CodeOverloaded:            ExitError,
	gotato.CodeDraining:              ExitError,
	gotato.CodeCannotResume:          ExitError,
	gotato.CodeNotWriter:             ExitError,
	gotato.CodeAlreadyExists:         ExitError,
}

// failErr reports an error and returns the exit code of its code.
func (c *cli) failErr(err error) int {
	code := ExitError
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		if exit, ok := exitOfCode[runtimeErr.Code]; ok {
			code = exit
		}
	}
	return c.failWith(code, err.Error(), err)
}
