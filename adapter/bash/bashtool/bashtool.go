// Package bashtool wraps the engine of package bash as the bash Tool of a
// Gotato Session (FEATURES.md, G-F36). The model queries the data of its own
// Session, a spilled Tool Result above all, with a read-only subset of bash,
// and nothing runs outside the process.
//
// The tool reads /results/<call_id> through session.ResultsFS, so it sees the
// Record of the Session that calls it and no other. A team installs it on an
// Agent in one line:
//
//	service.Agent{
//	    Name:         "support",
//	    Model:        model,
//	    Tools:        tools,
//	    SessionTools: []service.SessionToolsFunc{bashtool.Tools()},
//	}
//
// The engine does not import Gotato; this package is the thin layer that does.
package bashtool

import (
	"context"
	"fmt"
	"strings"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/adapter/bash"
	"github.com/jinhuang712/gotato/session"
)

// ID is the ID of the tool.
const ID = "bash"

// DefaultMaxOutput is the most bytes of stdout, and of stderr, a call keeps:
// four pages. Output over one page (gotato.SpillPageBytes) is spilled by the
// core like any Tool Result, so the model sees the first page and reads the
// rest from /results/<call_id>.
const DefaultMaxOutput = 4 * gotato.SpillPageBytes

// Option changes how the tool runs.
type Option func(*config)

type config struct {
	maxOutput int
	maxLine   int
}

// WithMaxOutput bounds the output of one call to n bytes. Past it the command
// is stopped and the result says so. n <= 0 means DefaultMaxOutput.
func WithMaxOutput(n int) Option { return func(c *config) { c.maxOutput = n } }

// WithMaxLine cuts each line of the output to n bytes, ending a cut line with
// a marker. n == 0 means bash.DefaultMaxLine, and n < 0 turns the cut off.
func WithMaxLine(n int) Option { return func(c *config) { c.maxLine = n } }

// Tools returns what an Agent lists in its SessionTools (a
// service.SessionToolsFunc): the bash tool for the Session whose Record it is
// given. The store is where a spilled body is read from when the live Record
// does not hold it.
func Tools(opts ...Option) func(*session.Record, session.EntryLog) []gotato.Tool {
	return func(record *session.Record, log session.EntryLog) []gotato.Tool {
		return []gotato.Tool{New(record, log, opts...)}
	}
}

// Description is the description of the tool. It is part of every request, so
// it holds nothing of a Session and is the same bytes for every call.
func Description() string {
	return "Runs a read-only bash subset over this conversation's data and returns its output. " +
		"Use it to find one line, one field or the matching lines of a large tool result instead of reading it page by page. " +
		"The data is a directory: /results/<call_id> is the full text of the result of that tool call, " +
		"and a cut result's ref result:<call_id> is the file /results/<call_id>. Start with ls /results. " +
		"Commands: " + strings.Join(bash.Commands(), ", ") + ". " +
		"Join them with | && || ; and redirect input with <. " +
		"Refused: loops, functions, command substitution, writing redirects, background jobs and any other command. " +
		"Run <command> --help to list the flags a command supports. " +
		"Output over one page is cut and kept as a result you can read with this tool."
}

type input struct {
	Command string `json:"command" description:"a command or pipeline, such as: grep -n 'order-88501' /results/call_9a2 | head -5"`
}

// New returns the bash tool for record. It runs each call over
// session.ResultsFS(ctx, record, log) with the ctx of the call, so the run
// ends with the Run. Its Effect is read, so every Effect cap keeps it.
func New(record *session.Record, log session.EntryLog, opts ...Option) gotato.Tool {
	cfg := config{maxOutput: DefaultMaxOutput}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxOutput <= 0 {
		cfg.maxOutput = DefaultMaxOutput
	}
	engine := []bash.Option{bash.WithMaxOutput(cfg.maxOutput), bash.WithMaxLine(cfg.maxLine)}
	tool, err := gotato.NewFuncTool(ID, Description(), func(ctx context.Context, in input) (string, error) {
		result, err := bash.Run(ctx, in.Command, session.ResultsFS(ctx, record, log), engine...)
		if err != nil {
			return "", err
		}
		return Format(result), nil
	})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.EffectRead)
}

// Format is the text the model reads for a Result: stdout as it is, then, when
// there is any, a [stderr] section, a note that the output was cut, and the
// exit code when it is not 0 (not when the output was cut: the command then
// ended by a failed write, and its code says nothing). A refused command and a non-zero exit are results
// the model reads and corrects, not failures of the call: grep's exit 1 means
// "no match" and is not an error. A command with no output and exit 0 reads
// "(no output)".
func Format(r bash.Result) string {
	var b strings.Builder
	b.WriteString(r.Stdout)
	section := func(text string) {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	if r.Stderr != "" {
		section("[stderr]\n" + r.Stderr)
	}
	if r.Truncated {
		section(fmt.Sprintf("[output cut: more than %d bytes; narrow the command, for example with grep or head]\n", len(r.Stdout)))
	}
	// A command cut at the bound ends by a failed write, so its exit code says nothing.
	if r.ExitCode != 0 && !r.Truncated {
		section(fmt.Sprintf("[exit code: %d]\n", r.ExitCode))
	}
	if b.Len() == 0 {
		return "(no output)"
	}
	return strings.TrimSuffix(b.String(), "\n")
}
