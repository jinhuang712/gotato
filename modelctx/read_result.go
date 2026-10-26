package modelctx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
)

// ReadResultID is the ID of the built-in tool that reads a spilled Tool Result
// back, a sibling of read_segment.
const ReadResultID = gotato.ReadResultToolID

// DefaultReadResultPageBytes is the most one page of a read_result result
// holds: the page of read_segment, and the size past which a Tool Result is
// spilled.
const DefaultReadResultPageBytes = gotato.SpillPageBytes

// ReadResultOptions controls ReadResultTool.
type ReadResultOptions struct {
	// PageBytes is the most one page holds, in bytes. 0 means
	// DefaultReadResultPageBytes.
	PageBytes int
}

func (o ReadResultOptions) pageBytes() int {
	if o.PageBytes <= 0 {
		return DefaultReadResultPageBytes
	}
	return max(o.PageBytes, 256)
}

// ResultTools is the gotato.ToolSource that gives a Session the built-in
// read_result tool. It always offers it, so the tool list does not change on
// the Turn a Tool Result is first spilled; the host offers it to an Agent that
// has any tool. log is where a body is read from when the live Record does not
// hold it, the Store of the Record.
func ResultTools(record *session.Record, log session.EntryLog, opts ReadResultOptions) gotato.ToolSource {
	return resultTools{tool: ReadResultTool(record, log, opts)}
}

type resultTools struct{ tool gotato.Tool }

// Tools implements gotato.ToolSource.
func (r resultTools) Tools() []gotato.Tool { return []gotato.Tool{r.tool} }

// readResultInput is the input of read_result.
type readResultInput struct {
	Ref  string `json:"ref" description:"the ref of the result, as in result:call_9a2, from the line that says the result was cut"`
	Page int    `json:"page,omitempty" description:"the page to read, from 1; a long result is read page by page"`
}

// ReadResultTool returns the read_result tool for record. It reads the full
// text of a Tool Result that was spilled, page by page, from the live Record's
// cache or else from the log by the position the Tool Result Message records.
// It reads only the spilled results of its own Session: a ref that names none
// of them fails as a Tool Result that lists the refs it can read. Its Effect is
// read, so every Effect cap keeps it.
//
// The pages are those of read_segment (gotato.Paginate), and the text carries
// no tag of its own: like any Tool Result it has the four tags escaped when it
// goes into a request.
func ReadResultTool(record *session.Record, log session.EntryLog, opts ReadResultOptions) gotato.Tool {
	tool, err := gotato.NewFuncTool(ReadResultID,
		"Returns the rest of a tool result that was cut, page by page. "+
			"Use it with the ref in the line that says a result was cut, such as result:call_9a2.",
		func(ctx context.Context, in readResultInput) (string, error) {
			return readResult(ctx, record, log, opts, in)
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.EffectRead)
}

func readResult(ctx context.Context, record *session.Record, log session.EntryLog, opts ReadResultOptions, in readResultInput) (string, error) {
	call, ok := gotato.CallOfRef(in.Ref)
	if !ok {
		return "", fmt.Errorf("%q is not a ref of a result; a ref looks like %s<call_id>%s", in.Ref, gotato.SpillRefPrefix, listRefs(record))
	}
	_, byCall := record.SpilledResults()
	spilled, found := byCall[call]
	if !found && len(record.Segments()) > 0 {
		// The Context no longer holds the Message of a call that a Segment
		// closed over; its Entry is still in the log.
		var err error
		spilled, found, err = session.FindSpilled(ctx, log, record.ID(), call)
		if err != nil && !errors.Is(err, session.ErrNotFound) {
			return "", fmt.Errorf("%s could not be read: %w", in.Ref, err)
		}
	}
	if !found {
		return "", fmt.Errorf("there is no spilled result %s in this conversation%s", gotato.SpillRef(call), listRefs(record))
	}
	body, pruned, err := record.SpilledBody(ctx, log, spilled)
	if errors.Is(err, session.ErrNotFound) {
		pruned, err = true, nil
	}
	if err != nil {
		return "", fmt.Errorf("%s could not be read: %w", gotato.SpillRef(call), err)
	}
	if pruned {
		return fmt.Sprintf("The body of %s was removed from the Record, so only the preview that stayed in the conversation is left.", gotato.SpillRef(call)), nil
	}

	page := max(in.Page, 1)
	limit := opts.pageBytes()
	pages := gotato.Paginate(gotato.SplitLines(body, limit), limit)
	ref := gotato.SpillRef(call)
	if page > len(pages) {
		return "", fmt.Errorf("%s has %d pages; there is no page %d", ref, len(pages), page)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Result %s, %d bytes. Page %d of %d.\n", ref, len(body), page, len(pages))
	b.WriteString(strings.Join(pages[page-1], "\n"))
	if page < len(pages) {
		fmt.Fprintf(&b, "\nMore: %s(ref=%q, page=%d).", ReadResultID, ref, page+1)
	} else {
		fmt.Fprintf(&b, "\nEnd of %s.", ref)
	}
	return b.String(), nil
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
		refs[i] = gotato.SpillRef(call)
	}
	return "; the results you can read are " + strings.Join(refs, ", ")
}
