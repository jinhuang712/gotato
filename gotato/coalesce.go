package gotato

import (
	"strings"
)

// coalesceDeltas merges the Parts that streamed deltas produced into one Part
// per contiguous block of text or of reasoning, so a reply is stored and
// replayed as the blocks the provider sent rather than as its fragments. A
// Reasoning block closes when its signature arrives
// (ModelEventKindReasoningDone), so a following Reasoning delta starts a new
// block. Other Parts are kept as is.
func coalesceDeltas(parts []ContentPart) []ContentPart {
	if len(parts) < 2 {
		return parts
	}
	out := make([]ContentPart, 0, len(parts))
	var text strings.Builder
	open := -1 // index in out of the block still receiving deltas
	flush := func() {
		if open >= 0 {
			out[open].Text = text.String()
			text.Reset()
			open = -1
		}
	}
	for _, part := range parts {
		mergeable := (part.Kind == ContentKindText || part.Kind == ContentKindReasoning) && part.Data == nil && part.MIMEType == "" && part.Metadata == nil
		if open >= 0 && mergeable && part.Kind == out[open].Kind {
			text.WriteString(part.Text)
			out[open].Signature = part.Signature
		} else {
			flush()
			out = append(out, part)
			if mergeable {
				open = len(out) - 1
				text.WriteString(part.Text)
			}
		}
		if open >= 0 && len(out[open].Signature) > 0 {
			flush()
		}
	}
	flush()
	return out
}
