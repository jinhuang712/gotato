package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

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
	report, err := rt.runner.Inspect(ctx, positionals[0], service.InspectConfig{Instruction: flags.instruction})
	if err != nil {
		return c.failErr(err)
	}
	if mode == "build" {
		if c.machine() {
			return c.writeJSON(report.Request)
		}
		fmt.Fprintf(c.stdout, "system:\n%s\n\n", report.Request.SystemInstructions)
		for _, message := range report.Request.Messages {
			fmt.Fprintf(c.stdout, "%-11s %s\n", message.Role, strings.TrimSpace(gotato.TextOf(message)))
		}
		return ExitOK
	}
	if c.machine() {
		return c.writeJSON(report)
	}
	fmt.Fprintf(c.stdout, "session %s\nstrategy %s (append-only)\nmessages %d · ~%d tokens · system %d bytes · tools %d\nprefix_hash %s\n",
		report.SessionID, report.Strategy, report.SelectedMessages, report.ApproxTokens, report.SystemBytes, len(report.Request.Tools), report.PrefixHash)
	printParts(c.stdout, report)
	fmt.Fprintf(c.stdout, "compact_ceiling_tokens %d", report.CompactCeilingTokens)
	if report.CompactCeilingTokens == 0 {
		fmt.Fprint(c.stdout, " (off: compacts only after a refusal as too long)")
	}
	fmt.Fprintln(c.stdout)
	if len(report.Compactions) > 0 {
		fmt.Fprintf(c.stdout, "compactions %d (last replaced %d messages)\n", len(report.Compactions), report.Compactions[len(report.Compactions)-1].ReplacedMessages)
	}
	return ExitOK
}

func (c *cli) contextCompact(args []string) int {
	fs := c.newFlagSet("context compact")
	var flags modelFlags
	bindModel(fs, &flags)
	keep := fs.Int("keep-messages", 0, "number of recent messages to keep as they are (default: the latest run's turns)")
	minTokens := fs.Int("min-tokens", 0, "skip a stretch since the last checkpoint estimated below this many tokens (default 0: always compact)")
	recapFlag := fs.String("recap", "none", "none (the default: the segment has no recap), truncate (deterministic) or model (the session's agent writes it on the cached prefix; use only once the payments check passes on your gateway)")
	recapMax := fs.Uint("recap-max-tokens", modelctx.DefaultRecapMaxTokens, "output limit of the model call that writes the recap (--recap model)")
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
	var writer modelctx.RecapWriter
	switch *recapFlag {
	case "none", "":
	case "truncate":
		writer = modelctx.TruncateRecap{}
	case "model":
		agent, err := rt.resolveAgent(flags.model, c.getenv)
		if err != nil {
			return c.failErr(err)
		}
		if agent == "" {
			s, err := session.Open(ctx, rt.store.Records, positionals[0])
			if err != nil {
				return c.failErr(err)
			}
			agent, _ = s.Get(agentKey)
		}
		spec, ok := rt.runner.Agent(agent)
		if !ok {
			return c.failErr(service.ErrUnknownAgent)
		}
		writer = modelctx.ModelRecap{Model: spec.Model, MaxTokens: uint32(min(*recapMax, 1<<31)), Label: "model:" + spec.Name}
	default:
		return c.usageError("unknown recap writer " + *recapFlag)
	}
	result, err := rt.runner.Compact(ctx, positionals[0], modelctx.CompactConfig{Recap: writer, KeepMessages: *keep, MinTokens: *minTokens})
	if err != nil {
		return c.failErr(err)
	}
	human := fmt.Sprintf("session %s: %d → %d messages, ~%d → ~%d tokens", result.SessionID, result.MessagesBefore, result.MessagesAfter, result.TokensBefore, result.TokensAfter)
	if !result.Replaced {
		human = fmt.Sprintf("session %s: nothing to compact (%s)", result.SessionID, result.Reason)
	}
	return c.emit(result, human)
}

// printParts writes the estimate per part of the request, in tokens and as a
// share of the parts' sum.
func printParts(w io.Writer, report modelctx.Report) {
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
