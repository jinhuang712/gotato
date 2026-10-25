package gotato

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
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
	// SpillRefPrefix starts the ref of a spilled result, `result:<call_id>`.
	SpillRefPrefix = "result:"
	// ReadResultToolID is the ID of the built-in tool that reads a spilled
	// result back, a sibling of read_segment.
	ReadResultToolID = "read_result"
)

// SpilledResult marks a Tool Result whose full text is a tool_result_spilled
// Entry of the Record: Seq is the Entry's position, Bytes the size of the text.
type SpilledResult struct {
	Seq   uint64 `json:"seq"`
	Bytes int    `json:"bytes"`
}

// SpillKeeper is an optional method of a Transcript: it says the Transcript's
// Journal keeps the Entries it writes, so the body of a spilled result can be
// read back. A Session spills only over a Transcript that does, as a
// session.Record or session.Writer does; over any other, a Tool Result is
// committed whole, as before.
type SpillKeeper interface {
	KeepsSpilledResults() bool
}

// SpillRef returns the ref of the spilled result of a Tool call.
func SpillRef(call ToolCallID) string { return SpillRefPrefix + string(call) }

// CallOfRef returns the Tool call a ref names, and false for a string that is
// not a ref.
func CallOfRef(ref string) (ToolCallID, bool) {
	call, ok := strings.CutPrefix(strings.TrimSpace(ref), SpillRefPrefix)
	return ToolCallID(call), ok && call != ""
}

// SplitLines splits text at its line breaks, and cuts a line longer than limit
// bytes into pieces of at most that many, on a rune boundary.
func SplitLines(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		for len(line) > limit {
			cut := limit
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
		out = append(out, line)
	}
	return out
}

// Paginate groups lines into pages of at most limit bytes, each line whole.
// read_segment and read_result page with it, and the preview of a spilled
// result counts its pages with it.
func Paginate(lines []string, limit int) [][]string {
	var pages [][]string
	var current []string
	size := 0
	for _, line := range lines {
		if len(current) > 0 && size+len(line)+1 > limit {
			pages = append(pages, current)
			current, size = nil, 0
		}
		current = append(current, line)
		size += len(line) + 1
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	return pages
}

// CutRunes returns the first n bytes of s, less a rune cut in half.
func CutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// SpillBody is the text of a result as the spill stores it: its text Parts,
// joined with a line break.
func SpillBody(content []ContentPart) string {
	var texts []string
	for _, part := range content {
		if part.Kind == ContentText {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// spillPreview is what stays in the Context of a spilled result: the start of
// the body and a line that says how much was cut and how to read the rest. The
// same body gives the same preview on every Instance.
func spillPreview(call ToolCallID, toolID, body string) string {
	pages := len(Paginate(SplitLines(body, SpillPageBytes), SpillPageBytes))
	var b strings.Builder
	b.WriteString(CutRunes(body, SpillPreviewBytes))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[Result cut: %s, %d pages. Read the rest with %s(ref=%q, page=2).]", humanBytes(len(body)), pages, ReadResultToolID, SpillRef(call))
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
		if part.Kind == ContentText {
			size += len(part.Text)
		}
	}
	if size <= SpillPageBytes {
		return result, nil
	}
	body := SpillBody(result.Content)
	if err := a.flush(ctx); err != nil {
		return result, err
	}
	written, err := a.writeEvents(ctx, []Event{ToolResultSpilled{Header: NewHeader(a.id, runID, turn), CallID: result.CallID, ToolID: toolID, Bytes: len(body), Body: body}})
	if err != nil {
		return result, err
	}
	seq := written[0].Head().Number
	content := []ContentPart{{Kind: ContentText, Text: spillPreview(result.CallID, toolID, body)}}
	for _, part := range result.Content {
		if part.Kind != ContentText {
			content = append(content, part)
		}
	}
	result.Content = content
	result.Spilled = &SpilledResult{Seq: seq, Bytes: len(body)}
	return result, nil
}
