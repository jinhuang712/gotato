package record

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/ref"
)

// readResultInput is the input of read_result.
type readResultInput struct {
	Ref  string `json:"ref" description:"a ref from the conversation: result:call_9a2 for a tool result, from a tool call line or from the line that says a result was cut; msg:<id> for a message a segment kept"`
	Page int    `json:"page,omitempty" description:"the page to read, from 1; a long result is read page by page"`
}

// resultTool returns the read_result tool. It reads the full text of a Tool
// Result or a Message by its ref, page by page, from the Originals, so a
// spilled result, one a Segment left out and a message a Segment kept are read
// the same way. It reads only its own Session's: a ref that names none of them
// fails as a Tool Result that says what the Originals offers to read. Its
// Effect is read, so every Effect cap keeps it.
//
// The pages are those of read_segment (text.Paginate), and the text carries
// no tag of its own: like any Tool Result it has the four tags escaped when it
// goes into a request.
func (r ReadBack) resultTool() gotato.Tool {
	tool, err := gotato.NewFuncTool(ref.ReadResult,
		"Returns the full text of one tool result or one message by its ref, such as result:call_9a2 or msg:m12, page by page. "+
			"Use it with a ref in a segment's tool call line or after a message, or with the ref in the line that says a result was cut.",
		r.readResult)
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead)
}

func (r ReadBack) readResult(ctx context.Context, in readResultInput) (string, error) {
	parsed, ok := ref.Parse(in.Ref)
	if !ok {
		// The zero Ref asks the Originals what there is to read.
		_, err := r.Originals.Ref(ctx, ref.Ref{})
		return "", readErrorf("%q is not a ref of a result or a message; a ref looks like %s<call_id> or %s<message_id>%s", in.Ref, ref.ResultPrefix, ref.MessagePrefix, hintOf(err))
	}
	name := parsed.String()
	found, err := r.Originals.Ref(ctx, parsed)
	switch {
	case errors.Is(err, ErrRemoved):
		return fmt.Sprintf("%s was removed from the Record, so only what the conversation keeps of it is left.", name), nil
	case errors.Is(err, ErrNoOriginal):
		return "", readErrorf("there is nothing in this conversation with the ref %s%s", name, hintOf(err))
	case err != nil:
		return "", readErrorf("%s could not be read", name)
	}
	if found.ToolResult != nil {
		return r.readPage("Result", name, toolResultText(found), in.Page)
	}
	var parts []string
	for _, part := range found.Parts {
		if t := textOfPart(part); t != "" {
			parts = append(parts, t)
		}
	}
	return r.readPage("Message ("+string(found.Role)+")", name, strings.Join(parts, "\n"), in.Page)
}

// readPage returns one page of body, with the line that says which page it is
// and how to read the next.
func (r ReadBack) readPage(kind, name, body string, page int) (string, error) {
	page = max(page, 1)
	limit := r.PageBytes()
	pages := text.Paginate(text.SplitLines(body, limit), limit)
	if page > len(pages) {
		return "", readErrorf("%s has %d pages; there is no page %d", name, len(pages), page)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, %d bytes. Page %d of %d.\n", kind, name, len(body), page, len(pages))
	b.WriteString(strings.Join(pages[page-1], "\n"))
	if page < len(pages) {
		fmt.Fprintf(&b, "\nMore: %s(ref=%q, page=%d).", ref.ReadResult, name, page+1)
	} else {
		fmt.Fprintf(&b, "\nEnd of %s.", name)
	}
	return b.String(), nil
}

// hintOf is the text an ErrNoOriginal adds for the model, or "".
func hintOf(err error) string {
	var hinted interface{ Hint() string }
	if errors.As(err, &hinted) && errors.Is(err, ErrNoOriginal) {
		return hinted.Hint()
	}
	return ""
}
