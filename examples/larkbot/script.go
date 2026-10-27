package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

// scriptModel is for the cluster scenarios (-script, test only). The fake gateway calls only the first tool,
// with one string argument, so it cannot load one tool of a catalog. A user message
// that carries [script:NAME] is played here, from the request alone (the Messages of the Run say what it has
// done, so any instance continues a Run the same way); every other message goes to the wrapped model.
//
//	[script:export]       call orders.export (a result of 1 MB, spilled), then say it is done
//	[script:mcp=6s]       call crm.get_order, held 6 s by the server, and report what came back
//	[script:search]       load crm.get_invoice with search_tools, call it, and report what came back
//	[script:load]         load crm.get_invoice with search_tools and call nothing else
//	[script:loaded]       say whether crm.get_invoice is loaded now
//	[script:readseg]      read Segment 1 back with read_segment and report what came back
//
// [quirk:delay=D] holds every model call of the message D, as the fake gateway does for the others.
type scriptModel struct {
	gotato.Model
}

var (
	scriptRe = regexp.MustCompile(`\[script:([a-z]+)(?:=([^\]]*))?\]`)
	delayRe  = regexp.MustCompile(`\[quirk:delay=([^\]]+)\]`)
)

// Stream implements gotato.Model.
func (m scriptModel) Stream(ctx context.Context, r gotato.ModelRequest) (gotato.ModelStream, error) {
	text, run := currentRun(r)
	found := scriptRe.FindStringSubmatch(text)
	if found == nil {
		return m.Model.Stream(ctx, r)
	}
	if d := delayRe.FindStringSubmatch(text); d != nil {
		dur, err := time.ParseDuration(d[1])
		if err != nil {
			return nil, fmt.Errorf("script: bad delay %q: %w", d[1], err)
		}
		t := time.NewTimer(dur)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
		return playScript(found[1], found[2], r, run)
	}).Stream(ctx, r)
}

// Close closes the wrapped model when it can be closed.
func (m scriptModel) Close() error {
	if c, ok := m.Model.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// currentRun returns the text of the last message the user wrote (a notice or a segment is not one) and the
// Messages after it, which are this Run's turns so far.
func currentRun(r gotato.ModelRequest) (string, []gotato.Message) {
	for i, msg := range slices.Backward(r.Messages) {
		if msg.Role != gotato.RoleUser {
			continue
		}
		text := strings.TrimSpace(gotato.TextOf(msg))
		if text == "" || strings.HasPrefix(text, "<") {
			continue
		}
		return text, r.Messages[i+1:]
	}
	return "", nil
}

// Names the scripts use. A call ID must be new in the Session, so the ones of tool calls carry the
// number of Tool Results the history holds.
const (
	exportCall   = "export-1"
	exportToolID = testkit.LargeExportID
	mcpOrderTool = "crm.get_order"
	mcpInvoice   = "crm.get_invoice"
)

// Answers the scenarios look for in the Transcript.
const (
	ansExported = "exported the orders"
	ansMCPOK    = "mcp-ok:"
	ansMCPError = "mcp-error:"
	ansLoaded   = "loaded:"
	ansLoadDone = "load-done"
	ansSegOK    = "segment-ok:"
	ansSegError = "segment-error:"
)

func toolCall(id, tool string, args any) testkit.Script {
	encoded, _ := json.Marshal(args)
	return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: tool, Arguments: encoded})
}

func playScript(name, arg string, r gotato.ModelRequest, run []gotato.Message) (testkit.Script, error) {
	results := 0
	for _, msg := range r.Messages {
		if msg.ToolResult != nil {
			results++
		}
	}
	var last *gotato.ToolResult // the newest result of this Run
	var texts []string
	for _, msg := range run {
		if msg.ToolResult != nil {
			last = msg.ToolResult
			texts = append(texts, resultText(*msg.ToolResult))
		}
	}
	next := fmt.Sprintf("%d", results+1)
	switch name {
	case "export":
		if last == nil {
			return toolCall(exportCall, exportToolID, map[string]string{}), nil
		}
		return testkit.Text(ansExported), nil
	case "mcp":
		if last == nil {
			return toolCall("order-"+next, mcpOrderTool, map[string]string{"id": "ord-" + next, "delay": arg}), nil
		}
		return testkit.Text(reportResult(*last)), nil
	case "search":
		switch len(texts) {
		case 0:
			return toolCall("find-"+next, gotato.SearchToolName, map[string][]string{"names": {mcpInvoice}}), nil
		case 1:
			return toolCall("invoice-"+next, mcpInvoice, map[string]string{"id": "inv-1"}), nil
		}
		return testkit.Text(reportResult(*last)), nil
	case "load":
		if last == nil {
			return toolCall("find-"+next, gotato.SearchToolName, map[string][]string{"names": {mcpInvoice}}), nil
		}
		return testkit.Text(ansLoadDone), nil
	case "readseg":
		if last == nil {
			return toolCall("seg-"+next, modelctx.ReadSegmentID, map[string]int{"segment": 1}), nil
		}
		if last.Status != gotato.ToolResultOK {
			return testkit.Text(ansSegError + " " + last.SafeError), nil
		}
		return testkit.Text(ansSegOK + " " + cut(resultText(*last), 600)), nil
	case "loaded":
		return testkit.Text(fmt.Sprintf("%s %t", ansLoaded, slices.Contains(r.LoadedTools, mcpInvoice))), nil
	}
	return nil, fmt.Errorf("script: unknown script %q", name)
}

// reportResult is the one line the model says about a Tool Result: what came back, or the error it read.
func reportResult(res gotato.ToolResult) string {
	if res.Status != gotato.ToolResultOK {
		return ansMCPError + " " + res.SafeError
	}
	return ansMCPOK + " " + resultText(res)
}

// cut keeps the first n bytes of s.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func resultText(res gotato.ToolResult) string {
	var b strings.Builder
	for _, p := range res.Content {
		b.WriteString(p.Text)
	}
	return b.String()
}
