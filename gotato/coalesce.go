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
	c := deltaCoalescer{out: make([]ContentPart, 0, len(parts)), open: -1}
	for _, part := range parts {
		c.add(part)
	}
	c.flush()
	return c.out
}

// deltaCoalescer holds the Parts coalesceDeltas has built so far and the
// text of the block still receiving deltas.
type deltaCoalescer struct {
	out  []ContentPart
	text strings.Builder
	open int // index in out of the block still receiving deltas
}

// flush closes the block still receiving deltas, if any.
func (c *deltaCoalescer) flush() {
	if c.open >= 0 {
		c.out[c.open].Text = c.text.String()
		c.text.Reset()
		c.open = -1
	}
}

// add appends part to the open block when it continues it, and as a Part of
// its own otherwise.
func (c *deltaCoalescer) add(part ContentPart) {
	mergeable := mergeableDelta(part)
	if c.open >= 0 && mergeable && part.Kind == c.out[c.open].Kind {
		c.text.WriteString(part.Text)
		c.out[c.open].Signature = part.Signature
	} else {
		c.flush()
		c.out = append(c.out, part)
		if mergeable {
			c.open = len(c.out) - 1
			c.text.WriteString(part.Text)
		}
	}
	if c.open >= 0 && len(c.out[c.open].Signature) > 0 {
		c.flush()
	}
}

// mergeableDelta reports whether part is a plain text or reasoning delta that
// can join the block before it.
func mergeableDelta(part ContentPart) bool {
	return (part.Kind == ContentKindText || part.Kind == ContentKindReasoning) && part.Data == nil && part.MIMEType == "" && part.Metadata == nil
}
