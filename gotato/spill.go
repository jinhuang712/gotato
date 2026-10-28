package gotato

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/internal/text"
)

// A Tool Result whose text is longer than one page is spilled: its full text
// is stored in the Record as a tool_result_spilled Entry of its own, and the
// Tool Result Message that goes in the Transcript, and so in every Context,
// holds a preview and a ref. The model reads the rest with read_result.
const (
	// SpillPageBytes is one page of text: the line past which a Tool Result is
	// spilled, and the size of a page of read_result and read_segment. About
	// 8K tokens.
	SpillPageBytes = 32 << 10
	// SpillPreviewBytes is the start of a spilled result that stays in the
	// Context: about 2K tokens (bytes ÷ 4), cut on a rune boundary.
	SpillPreviewBytes = 8000
	// ReadResultToolID is the ID of the built-in tool that reads a spilled
	// result back, a sibling of read_segment.
	ReadResultToolID = "read_result"
)

// SpilledResult marks a Tool Result whose full text is a tool_result_spilled
// Entry of the Record: Seq is the Entry's position, Bytes the size of the text.
// SessionID names the Record that holds the Entry when it is not the Record of
// the Message: a fork inherits the Messages of its parent and keeps their
// bodies in the parent's log, as an inherited Segment does (SegmentSource). It
// is empty for a Message the Record wrote itself.
type SpilledResult struct {
	Seq       uint64    `json:"seq"`
	Bytes     int       `json:"bytes"`
	SessionID SessionID `json:"session_id,omitempty"`
}

// SpillKeeper is an optional method of a Transcript: it says the Transcript's
// Journal keeps the Entries it writes, so the body of a spilled result can be
// read back. A Session spills only over a Transcript that does, as a
// session.Record or session.Writer does; over any other, a Tool Result is
// committed whole, as before.
type SpillKeeper interface {
	KeepsSpilledResults() bool
}

// cutRunes returns the first n bytes of s, less a rune cut in half.
func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// spillBody is the text of a result as the spill stores it: its text Parts,
// joined with a line break.
func spillBody(content []ContentPart) string {
	var texts []string
	for _, part := range content {
		if part.Kind == ContentKindText {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// spillPreview is what stays in the Context of a spilled result: the start of
// the body and a line that says how much was cut and how to read the rest. The
// same body gives the same preview on every Instance.
func spillPreview(call ToolCallID, body string) string {
	pages := len(text.Paginate(text.SplitLines(body, SpillPageBytes), SpillPageBytes))
	var b strings.Builder
	b.WriteString(cutRunes(body, SpillPreviewBytes))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[Result cut: %s, %d pages. Read it page by page with %s(ref=%q, page=1); the preview is only the start of page 1.]", humanBytes(len(body)), pages, ReadResultToolID, text.SpillRef(string(call)))
	return b.String()
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", (n+1<<10-1)>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// keepsSpilled reports whether the Session's Transcript keeps what the spill
// writes.
func (a *Session) keepsSpilled() bool {
	keeper, ok := a.transcript.(SpillKeeper)
	return ok && keeper.KeepsSpilledResults()
}

// spill spills a result whose text is longer than one page, before it is
// committed, so the Message it becomes is small. The full text is written as a
// ToolResultSpilled Entry first, so the Message can record its position. It
// returns the result as the Transcript holds it: a preview in place of the
// text, the Parts that are not text kept as they were, and Spilled set.
func (a *Session) spill(ctx context.Context, runID RunID, turn TurnNumber, toolID string, result ToolResult) (ToolResult, *RuntimeError) {
	if !a.keepsSpilled() {
		return result, nil
	}
	size := 0
	for _, part := range result.Content {
		if part.Kind == ContentKindText {
			size += len(part.Text)
		}
	}
	if size <= SpillPageBytes {
		return result, nil
	}
	body := spillBody(result.Content)
	if err := a.flush(ctx); err != nil {
		return result, err
	}
	written, err := a.writeEvents(ctx, []Event{ToolResultSpilled{Header: NewHeader(a.id, runID, turn), CallID: result.CallID, ToolID: toolID, Bytes: len(body), Body: body}})
	if err != nil {
		return result, err
	}
	// The Journal may return Events it had queued before this one, such as a
	// metadata change: the Seq is the number of the spilled body's own Entry.
	var seq uint64
	for _, event := range written {
		if spilled, ok := event.(ToolResultSpilled); ok && spilled.CallID == result.CallID {
			seq = spilled.Head().Number
		}
	}
	if seq == 0 {
		return result, runtimeError(CodeStoreFailure, "spill", "the Journal did not return the spilled result it wrote", nil)
	}
	content := []ContentPart{{Kind: ContentKindText, Text: spillPreview(result.CallID, body)}}
	for _, part := range result.Content {
		if part.Kind != ContentKindText {
			content = append(content, part)
		}
	}
	result.Content = content
	result.Spilled = &SpilledResult{Seq: seq, Bytes: len(body)}
	return result, nil
}
