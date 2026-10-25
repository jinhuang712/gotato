package bash

import (
	"fmt"
	"io"
	"unicode/utf8"
)

// DefaultMaxLine is the longest line of the final stdout of a command line that
// Run keeps when WithMaxLine is not given. It is about a quarter of a page of a
// model's context (8K tokens): a line longer than that, such as minified JSON,
// is not readable in one piece, and the model is better served by a pipeline
// that picks what it needs out of the line (jq, cut, grep -o).
const DefaultMaxLine = 8 << 10

// lineCutter is the writer of the final stdout. It keeps the first max bytes
// of each line and replaces the rest with a marker, so a line costs at most max
// bytes of memory however long it is. Only the final stdout goes through it:
// the stages of a pipeline see every line whole (up to MaxLineBytes), so the
// cut never changes what grep or jq reads.
type lineCutter struct {
	w    io.Writer
	max  int // 0 means no cut
	line int // bytes of the current line seen so far, kept or not
	kept int // bytes of the current line written, or held in tail
	tail []byte
}

func newLineCutter(w io.Writer, max int) *lineCutter { return &lineCutter{w: w, max: max} }

// lineCutMarker is the text that replaces the end of a long line. The size is
// that of the whole line, without its newline.
func lineCutMarker(size int) string { return "[line cut: " + readableSize(size) + "]" }

func (c *lineCutter) Write(p []byte) (int, error) {
	if c.max <= 0 {
		return c.w.Write(p)
	}
	total := len(p)
	for len(p) > 0 {
		nl := -1
		for i, b := range p {
			if b == '\n' {
				nl = i
				break
			}
		}
		chunk := p
		if nl >= 0 {
			chunk = p[:nl]
		}
		if err := c.keep(chunk); err != nil {
			return total - len(p), err
		}
		if nl < 0 {
			break
		}
		if err := c.endLine(true); err != nil {
			return total - len(p), err
		}
		p = p[nl+1:]
	}
	return total, nil
}

// keep writes as much of a piece of the current line as fits in max bytes. It
// never ends the kept text in the middle of a UTF-8 character: the bytes of a
// character that is not complete wait in c.tail until the next piece completes
// it, and are dropped when the line turns out to be cut there.
func (c *lineCutter) keep(chunk []byte) error {
	c.line += len(chunk)
	data := chunk
	if len(c.tail) > 0 {
		data = append(append([]byte(nil), c.tail...), chunk...)
		c.kept -= len(c.tail)
		c.tail = nil
	}
	room := c.max - c.kept
	if room <= 0 || len(data) == 0 {
		return nil
	}
	cutHere := len(data) > room
	kept := data[:min(len(data), room)]
	// The bytes of a character that the end of kept splits.
	incomplete := 0
	for back := 1; back <= min(utf8.UTFMax-1, len(kept)); back++ {
		b := kept[len(kept)-back]
		if utf8.RuneStart(b) {
			if !utf8.FullRune(kept[len(kept)-back:]) {
				incomplete = back
			}
			break
		}
	}
	if incomplete > 0 {
		if !cutHere {
			c.tail = append(c.tail, kept[len(kept)-incomplete:]...)
		}
		kept = kept[:len(kept)-incomplete]
		if cutHere {
			c.kept = c.max // nothing more of this line is kept
		}
	}
	if _, err := c.w.Write(kept); err != nil {
		return err
	}
	c.kept += len(kept) + len(c.tail)
	return nil
}

// endLine finishes the current line, with a marker when it was cut and a
// newline when it had one.
func (c *lineCutter) endLine(newline bool) error {
	if c.line <= c.max && len(c.tail) > 0 {
		if _, err := c.w.Write(c.tail); err != nil {
			return err
		}
	}
	c.tail = nil
	if c.line > c.max {
		if _, err := io.WriteString(c.w, lineCutMarker(c.line)); err != nil {
			return err
		}
	}
	if newline {
		if _, err := c.w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	c.line, c.kept = 0, 0
	return nil
}

// Close finishes a last line that has no newline.
func (c *lineCutter) Close() error {
	if c.max <= 0 || c.line == 0 {
		return nil
	}
	return c.endLine(false)
}

// readableSize writes a size with one decimal in the largest unit that keeps it
// at 1 or more: 512 B, 8.0 KiB, 1.0 MiB.
func readableSize(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}
