package model

import (
	"bufio"
	"context"
	"fmt"
	"strings"
)

// SSE framing limits. They bound what one upstream line or event can allocate,
// so a misbehaving provider cannot grow the stream buffer without limit.
const (
	maxSSELineBytes  = 1 << 20
	maxSSEEventBytes = 4 << 20
)

// readBoundedLine reads one '\n'-terminated line without letting a line that
// never terminates grow the buffer without bound.
func readBoundedLine(reader *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return string(buf), err
		}
		if b == '\n' {
			return string(buf), nil
		}
		buf = append(buf, b)
		if len(buf) > maxSSELineBytes {
			return "", fmt.Errorf("model: SSE line exceeds %d bytes", maxSSELineBytes)
		}
	}
}

// readSSEEvent reads one event: its "event:" name and its data: lines joined
// by a newline, enforcing the line and event size limits. Lines of any other
// field, and comments, are dropped, and an event with no data line is not one.
func readSSEEvent(ctx context.Context, reader *bufio.Reader) (SSEEvent, error) {
	var (
		lines  []string
		fields sseFields
	)
	for {
		line, err := nextSSELine(ctx, reader)
		if line == "" && err == nil && len(lines) > 0 {
			return SSEEvent{Name: fields.name, Data: strings.Join(lines, "\n")}, nil
		}
		payload, data, ferr := fields.take(line)
		if ferr != nil {
			return SSEEvent{}, ferr
		}
		if data {
			lines = append(lines, payload)
		}
		if err != nil {
			// The stream ended before the blank line that completes the event:
			// the event is discarded, and the end is the caller's to treat as
			// a cut stream (or a finished one).
			return SSEEvent{}, err
		}
	}
}

// nextSSELine reads one line without its carriage return, or returns the
// error of ctx once it is done.
func nextSSELine(ctx context.Context, reader *bufio.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := readBoundedLine(reader)
	return strings.TrimSuffix(raw, "\r"), err
}

// sseFields holds the name of the event being read and the size of its data
// so far.
type sseFields struct {
	name  string
	total int
}

// take reads one line of the event that is not its end. It returns the
// payload of a data line, within the event size limit, and keeps the name of
// an "event:" line; a blank line, which ended no event, clears the name. Lines
// of any other field are dropped.
func (f *sseFields) take(line string) (payload string, data bool, err error) {
	if after, ok := strings.CutPrefix(line, "data:"); ok {
		payload = strings.TrimSpace(after)
		f.total += len(payload)
		if f.total > maxSSEEventBytes {
			return "", false, fmt.Errorf("model: SSE event exceeds %d bytes", maxSSEEventBytes)
		}
		return payload, true, nil
	}
	if after, ok := strings.CutPrefix(line, "event:"); ok {
		f.name = strings.TrimSpace(after)
	}
	if line == "" {
		f.name = ""
	}
	return "", false, nil
}
