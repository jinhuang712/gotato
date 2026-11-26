package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// recapTokensCap is the largest --recap-max-tokens passed on, so the value fits the Recap's uint32 limit.
const recapTokensCap = 1 << 31

func (c *cli) cmdContext(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("context", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("context needs a subcommand: inspect, build, compact")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "inspect", "build":
		return c.contextInspect(sub, rest)
	case "compact":
		return c.contextCompact(rest)
	default:
		return c.usageError("unknown context subcommand " + sub)
	}
}

func (c *cli) contextInspect(mode string, args []string) int {
	fs := c.newFlagSet("context " + mode)
	var flags modelFlags
	bindModel(fs, &flags)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("context " + mode + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	report, err := rt.runner.Inspect(ctx, positionals[0], host.InspectConfig{Instruction: flags.instruction})
	if err != nil {
		return c.failErr(err)
	}
	if mode == "build" {
		if c.machine() {
			return c.writeJSON(report.Request)
		}
		printBuild(c.stdout, report)
		return ExitOK
	}
	if c.machine() {
		return c.writeJSON(report)
	}
	printInspect(c.stdout, report)
	return ExitOK
}

// printBuild writes the system instruction and the messages of the request the
// model would receive.
func printBuild(w io.Writer, report record.Report) {
	fmt.Fprintf(w, "system:\n%s\n\n", report.Request.SystemInstructions)
	for _, message := range report.Request.Messages {
		fmt.Fprintf(w, "%-11s %s\n", message.Role, strings.TrimSpace(gotato.TextOf(message)))
	}
}

// printInspect writes the summary of an inspection report.
func printInspect(w io.Writer, report record.Report) {
	fmt.Fprintf(w, "session %s\nstrategy %s (append-only)\nmessages %d · ~%d tokens · system %d bytes · tools %d\nprefix_hash %s\n",
		report.SessionID, report.Strategy, report.SelectedMessages, report.ApproxTokens, report.SystemBytes, len(report.Request.Tools), report.PrefixHash)
	printParts(w, report)
	fmt.Fprintf(w, "compact_ceiling_tokens %d", report.CompactCeilingTokens)
	if report.CompactCeilingTokens == 0 {
		fmt.Fprint(w, " (off: compacts only after a refusal as too long)")
	}
	fmt.Fprintln(w)
	if len(report.Compactions) > 0 {
		fmt.Fprintf(w, "compactions %d (last replaced %d messages)\n", len(report.Compactions), report.Compactions[len(report.Compactions)-1].ReplacedMessages)
	}
}

func (c *cli) contextCompact(args []string) int {
	fs := c.newFlagSet("context compact")
	var flags modelFlags
	bindModel(fs, &flags)
	keep := fs.Int("keep-messages", 0, "number of recent messages to keep as they are (default: the latest run's turns)")
	minTokens := fs.Int("min-tokens", 0, "skip a stretch since the last checkpoint estimated below this many tokens (default 0: always compact)")
	recapFlag := fs.String("recap", "none", "none (the default: the segment has no recap), truncate (deterministic) or model (the session's agent writes it on the cached prefix; use only once the payments check passes on your gateway)")
	recapMax := fs.Uint("recap-max-tokens", record.DefaultRecapMaxTokens, "output limit of the model call that writes the recap (--recap model)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("context compact needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	if *keep < 0 || *minTokens < 0 {
		return c.usageError("--keep-messages and --min-tokens cannot be negative")
	}
	writer, code, ok := c.recapWriter(ctx, rt, flags, positionals[0], recapChoice{kind: *recapFlag, maxTokens: *recapMax})
	if !ok {
		return code
	}
	result, err := rt.runner.Compact(ctx, positionals[0], record.CompactConfig{Recap: writer, KeepMessages: *keep, MinTokens: *minTokens})
	if err != nil {
		return c.failErr(err)
	}
	human := fmt.Sprintf("session %s: %d → %d messages, ~%d → ~%d tokens", result.SessionID, result.MessagesBefore, result.MessagesAfter, result.TokensBefore, result.TokensAfter)
	if !result.Replaced {
		human = fmt.Sprintf("session %s: nothing to compact (%s)", result.SessionID, result.Reason)
	}
	return c.emit(result, human)
}

// recapChoice is the --recap and --recap-max-tokens flags of context compact.
type recapChoice struct {
	kind      string
	maxTokens uint
}

// recapWriter is the RecapWriter that choice names. When ok is false the
// command exits with code.
func (c *cli) recapWriter(ctx context.Context, rt *cliRuntime, flags modelFlags, sessionID string, choice recapChoice) (writer record.RecapWriter, code int, ok bool) {
	switch choice.kind {
	case "none", "":
		return nil, ExitOK, true
	case "truncate":
		return record.TruncateRecap{}, ExitOK, true
	case "model":
		writer, err := rt.modelRecap(ctx, sessionID, flags.model, c.getenv, choice.maxTokens)
		if err != nil {
			return nil, c.failErr(err), false
		}
		return writer, ExitOK, true
	default:
		return nil, c.usageError("unknown recap writer " + choice.kind), false
	}
}

// modelRecap is the ModelRecap that the session's agent, or the one named by
// the --model value, writes.
func (rt *cliRuntime) modelRecap(ctx context.Context, sessionID, modelFlag string, env func(string) string, maxTokens uint) (record.RecapWriter, error) {
	agent, err := rt.resolveAgent(modelFlag, env)
	if err != nil {
		return nil, err
	}
	if agent == "" {
		s, err := store.Open(ctx, rt.store.Records, sessionID)
		if err != nil {
			return nil, err
		}
		agent, _ = s.Get(agentKey)
	}
	spec, err := rt.runner.AgentProfile(ctx, agent)
	if err != nil {
		return nil, err
	}
	return record.ModelRecap{Model: spec.Model, MaxTokens: uint32(min(maxTokens, recapTokensCap)), Label: "model:" + spec.Name}, nil
}

// printParts writes the estimate per part of the request, in tokens and as a
// share of the parts' sum.
func printParts(w io.Writer, report record.Report) {
	parts := []struct {
		name   string
		tokens int
	}{
		{"system", report.EstimateSystemTokens},
		{"tool definitions", report.EstimateToolDefsTokens},
		{"segments", report.EstimateSegmentsTokens},
		{"history", report.EstimateHistoryTokens},
		{"tool results", report.EstimateToolResultsTokens},
	}
	total := 0
	for _, part := range parts {
		total += part.tokens
	}
	fmt.Fprintln(w, "parts (estimate, tokens):")
	for _, part := range parts {
		share := 0.0
		if total > 0 {
			share = float64(part.tokens) * 100 / float64(total)
		}
		fmt.Fprintf(w, "  %-16s %7d  %5.1f%%\n", part.name, part.tokens, share)
	}
}
