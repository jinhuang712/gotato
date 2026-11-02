package modelctx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/session"
)

// ReadResultID is the ID of the built-in tool that reads a spilled Tool Result
// back, a sibling of read_segment.
const ReadResultID = gotato.ReadResultToolID

// defaultReadResultPageBytes is the most one page of a read_result result
// holds: the page of read_segment, and the size past which a Tool Result is
// spilled.
const defaultReadResultPageBytes = gotato.SpillPageBytes

// ReadResultConfig controls ReadResultTool.
type ReadResultConfig struct {
	// PageBytes is the most one page holds, in bytes. 0 means
	// defaultReadResultPageBytes.
	PageBytes int
}

func (c ReadResultConfig) pageBytes() int {
	if c.PageBytes <= 0 {
		return defaultReadResultPageBytes
	}
	return max(c.PageBytes, 256)
}

// ResultTools is the gotato.ToolSource that gives a Session the built-in
// read_result tool. It always offers it, so the tool list does not change on
// the Turn a Tool Result is first spilled; the host offers it to an Agent that
// has any tool. log is where a body is read from when the live Record does not
// hold it, the Store of the Record.
func ResultTools(record *session.Record, log session.EntryLog, opts ReadResultConfig) gotato.ToolSource {
	return resultTools{tool: ReadResultTool(record, log, opts)}
}

type resultTools struct{ tool gotato.Tool }

// Tools implements gotato.ToolSource.
func (r resultTools) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{r.tool}, nil
}

// readResultInput is the input of read_result.
type readResultInput struct {
	Ref  string `json:"ref" description:"a ref from the conversation: result:call_9a2 for a tool result, from a tool call line or from the line that says a result was cut; msg:<id> for a message a segment kept"`
	Page int    `json:"page,omitempty" description:"the page to read, from 1; a long result is read page by page"`
}

// ReadResultTool returns the read_result tool for record. It reads the full
// text of a Tool Result that was spilled, page by page, from the live Record's
// cache or else from the log by the position the Tool Result Message records.
// It reads only the spilled results of its own Session: a ref that names none
// of them fails as a Tool Result that lists the refs it can read. Its Effect is
// read, so every Effect cap keeps it.
//
// The pages are those of read_segment (text.Paginate), and the text carries
// no tag of its own: like any Tool Result it has the four tags escaped when it
// goes into a request.
func ReadResultTool(record *session.Record, log session.EntryLog, opts ReadResultConfig) gotato.Tool {
	tool, err := gotato.NewFuncTool(ReadResultID,
		"Returns the full text of one tool result or one message by its ref, such as result:call_9a2 or msg:m12, page by page. "+
			"Use it with a ref in a segment's tool call line or after a message, or with the ref in the line that says a result was cut.",
		func(ctx context.Context, in readResultInput) (string, error) {
			return readResult(ctx, record, log, opts, in)
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead)
}

func readResult(ctx context.Context, record *session.Record, log session.EntryLog, opts ReadResultConfig, in readResultInput) (string, error) {
	if id, ok := text.IDOfMessageRef(in.Ref); ok {
		return readFromRecord(ctx, record, log, opts, in.Page, text.MessageRef(id), func(m gotato.Message) bool {
			return m.ID == gotato.MessageID(id) && m.ToolResult == nil && (m.Role == gotato.RoleUser || m.Role == gotato.RoleAssistant)
		})
	}
	callID, ok := text.CallOfRef(in.Ref)
	if !ok {
		return "", readErrorf("%q is not a ref of a result or a message; a ref looks like %s<call_id> or %s<message_id>%s", in.Ref, text.SpillRefPrefix, text.MessageRefPrefix, listRefs(record))
	}
	call := gotato.ToolCallID(callID)
	_, byCall := record.SpilledResults()
	spilled, found := byCall[call]
	if !found && len(record.Segments()) > 0 {
		// The Context no longer holds the Message of a call that a Segment
		// closed over; its Entry is still in the log.
		var err error
		spilled, found, err = record.FindSpilled(ctx, log, call)
		if err != nil && !errors.Is(err, session.ErrNotFound) {
			return "", readErrorf("%s could not be read", in.Ref)
		}
	}
	if !found {
		// A result under one page is not spilled: it is read from the Record
		// like a message.
		return readFromRecord(ctx, record, log, opts, in.Page, text.SpillRef(string(call)), func(m gotato.Message) bool {
			return m.ToolResult != nil && m.ToolResult.CallID == call
		})
	}
	body, pruned, err := record.SpilledBody(ctx, log, spilled)
	if errors.Is(err, session.ErrNotFound) {
		pruned, err = true, nil
	}
	if err != nil {
		return "", readErrorf("%s could not be read", text.SpillRef(string(call)))
	}
	if pruned {
		return fmt.Sprintf("The body of %s was removed from the Record, so only the preview that stayed in the conversation is left.", text.SpillRef(string(call))), nil
	}

	return readPage("Result", text.SpillRef(string(call)), body, opts, in.Page)
}

// readPage returns one page of body, with the line that says which page it is
// and how to read the next.
func readPage(kind, ref, body string, opts ReadResultConfig, page int) (string, error) {
	page = max(page, 1)
	limit := opts.pageBytes()
	pages := text.Paginate(text.SplitLines(body, limit), limit)
	if page > len(pages) {
		return "", readErrorf("%s has %d pages; there is no page %d", ref, len(pages), page)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, %d bytes. Page %d of %d.\n", kind, ref, len(body), page, len(pages))
	b.WriteString(strings.Join(pages[page-1], "\n"))
	if page < len(pages) {
		fmt.Fprintf(&b, "\nMore: %s(ref=%q, page=%d).", ReadResultID, ref, page+1)
	} else {
		fmt.Fprintf(&b, "\nEnd of %s.", ref)
	}
	return b.String(), nil
}

// readFromRecord reads a Tool Result that was not spilled, or a Message, from
// the Record: first from what the Context holds, then from the Entries each
// Segment closed over, newest Segment first. A reference into Entries that a
// team pruned reads as removed.
func readFromRecord(ctx context.Context, record *session.Record, log session.EntryLog, opts ReadResultConfig, page int, ref string, match func(gotato.Message) bool) (string, error) {
	found, gap, err := findOriginal(ctx, record, log, match)
	if err != nil {
		return "", readErrorf("%s could not be read", ref)
	}
	if found == nil {
		if gap {
			return fmt.Sprintf("%s was removed from the Record, so only what the conversation keeps of it is left.", ref), nil
		}
		return "", readErrorf("there is nothing in this conversation with the ref %s%s", ref, listRefs(record))
	}
	if found.ToolResult != nil {
		return readPage("Result", ref, toolResultText(*found), opts, page)
	}
	var parts []string
	for _, part := range found.Parts {
		if t := textOfPart(part); t != "" {
			parts = append(parts, t)
		}
	}
	return readPage("Message ("+string(found.Role)+")", ref, strings.Join(parts, "\n"), opts, page)
}

// findOriginal looks for the latest Message that match accepts: in the
// Context, which holds the tail, and then in the Entries of each Segment
// (CommittedMessages), newest first. gap is true when some of those Entries
// are gone or were not recorded, so an absent Message may have been in them.
func findOriginal(ctx context.Context, record *session.Record, log session.EntryLog, match func(gotato.Message) bool) (found *gotato.Message, gap bool, err error) {
	for _, message := range slices.Backward(record.Messages()) {
		if match(message) {
			return &message, false, nil
		}
	}
	refs, _ := refsOf(record.Segments())
	for _, ref := range slices.Backward(refs) {
		id := record.ID()
		if ref.source.SessionID != "" {
			id = string(ref.source.SessionID)
		}
		same, err := session.SameRecord(ctx, log, id, ref.source.RecordEpoch)
		if err != nil {
			return nil, false, err
		}
		if !same {
			gap = true
			continue
		}
		messages, complete, err := session.CommittedMessages(ctx, log, id, ref.source.FromNumber, ref.source.ToNumber)
		if err != nil {
			return nil, false, err
		}
		gap = gap || !complete
		for _, message := range slices.Backward(messages) {
			if match(message) {
				return &message, false, nil
			}
		}
	}
	return nil, gap, nil
}

// listRefs says which results the Context holds, for an error that names a
// ref that is not one.
func listRefs(record *session.Record) string {
	calls, _ := record.SpilledResults()
	if len(calls) == 0 {
		return "; this conversation has no spilled result"
	}
	refs := make([]string, len(calls))
	for i, call := range calls {
		refs[i] = text.SpillRef(string(call))
	}
	return "; the results you can read are " + strings.Join(refs, ", ")
}
