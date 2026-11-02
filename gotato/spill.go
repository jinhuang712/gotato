package gotato

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/ref"
)

// A Tool Result whose text is longer than one page is spilled: its full text
// is stored in the Record as a tool_result_spilled Entry of its own, and the
// Tool Result Message that goes in the Transcript, and so in every Context,
// holds a preview and a ref. The model reads the rest with read_result.
//
// A Session spills only with a ReadBack installed (WithReadBack), so every ref
// it writes has a reader; the ReadBack owns the page size, which the spill
// threshold, the pages the preview counts and the read tools all use.

// ReadBack makes the originals Gotato moved out of the Context readable
// again: a ToolSource that offers the tools that read a spilled Tool Result
// and a closed Segment back, and the one owner of the page size.
//
// PageBytes is the most one page holds, in bytes. It is the line past which a
// Tool Result is spilled, the size of a page of every read tool, and what the
// preview counts its pages by, so the three cannot disagree. The Session asks
// it once, in NewSession, and refuses a value below 1.
// modelctx.ReadBack is the default.
// [SEAM] a host replaces where originals are read from, how a page is cut and how a read is worded.
type ReadBack interface {
	ToolSource
	PageBytes() int
}

// WithReadBack installs the ReadBack that gives the Session its read tools and
// its page size, and turns the spill of large Tool Results on. A later call
// replaces an earlier one. NewSession fails with CodeInvalidArgument when its
// PageBytes is below 1 or it panics. Its Tools are asked with every other
// ToolSource, so a Tool ID it shares with another fails the Session whole.
func WithReadBack(readBack ReadBack) SessionOption {
	return func(c *sessionConfig) error {
		if readBack == nil {
			return runtimeError(CodeInvalidArgument, "WithReadBack", "ReadBack is nil", nil)
		}
		c.readBack = readBack
		return nil
	}
}

// installReadBack adds the ReadBack to the ToolSources and returns the page
// size it owns, 0 when none is installed.
func (c *sessionConfig) installReadBack() (int, error) {
	if c.readBack == nil {
		return 0, nil
	}
	var page int
	if err := callHost("ReadBack.PageBytes", func() error { page = c.readBack.PageBytes(); return nil }); err != nil {
		return 0, runtimeError(CodeInvalidArgument, "WithReadBack", "ReadBack.PageBytes failed", err)
	}
	if page < 1 {
		return 0, runtimeError(CodeInvalidArgument, "WithReadBack", "ReadBack.PageBytes is below 1", nil)
	}
	c.toolSources = append(c.toolSources, c.readBack)
	return page, nil
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
// the body, at most preview bytes, and a line that says how much was cut and
// how to read the rest. The pages it counts are those of the ReadBack's page
// size, which is the size read_result pages by. The same body gives the same
// preview on every Instance.
func spillPreview(call ToolCallID, body string, page, preview int) string {
	pages := len(text.Paginate(text.SplitLines(body, page), page))
	var b strings.Builder
	b.WriteString(cutRunes(body, preview))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[Result cut: %s, %d pages. Read it page by page with %s(ref=%q, page=1); the preview is only the start of page 1.]", humanBytes(len(body)), pages, ref.ReadResult, ref.Result(string(call)).String())
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
// writes: a session.Record or session.Writer declares Capabilities.Spill; over
// any other Transcript a Tool Result is committed whole.
func (a *Session) keepsSpilled() (keeps bool, failure *RuntimeError) {
	caps, failure := a.history.capabilities()
	return caps.Spill && failure == nil, failure
}

// spill spills a result whose text is longer than one page (the ReadBack's
// PageBytes), before it is committed, so the Message it becomes is small. The full text is written as a
// ToolResultSpilled Entry first, so the Message can record its position. It
// returns the result as the Transcript holds it: a preview in place of the
// text, the Parts that are not text kept as they were, and Spilled set.
func (a *Session) spill(ctx context.Context, runID RunID, turn TurnNumber, toolID string, result ToolResult) (ToolResult, *RuntimeError) {
	if a.def.spillPage < 1 {
		// No ReadBack: nothing would read the body back.
		return result, nil
	}
	keeps, failure := a.keepsSpilled()
	if failure != nil {
		return result, failure
	}
	if !keeps {
		return result, nil
	}
	size := 0
	for _, part := range result.Content {
		if part.Kind == ContentKindText {
			size += len(part.Text)
		}
	}
	if size <= a.def.spillPage {
		return result, nil
	}
	body := spillBody(result.Content)
	if err := a.flush(ctx); err != nil {
		return result, err
	}
	written, err := a.writeEvents(ctx, []Event{ToolResultSpilled{Header: NewHeader(a.def.id, runID, turn), CallID: result.CallID, ToolID: toolID, Bytes: len(body), Body: body}})
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
	content := []ContentPart{{Kind: ContentKindText, Text: spillPreview(result.CallID, body, a.def.spillPage, min(int(a.def.limits.SpillPreviewBytes), a.def.spillPage))}}
	for _, part := range result.Content {
		if part.Kind != ContentKindText {
			content = append(content, part)
		}
	}
	result.Content = content
	result.Spilled = &SpilledResult{Seq: seq, Bytes: len(body)}
	return result, nil
}
