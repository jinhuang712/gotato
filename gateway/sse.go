package gateway

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
			return "", fmt.Errorf("gateway: SSE line exceeds %d bytes", maxSSELineBytes)
		}
	}
}

// readSSEEvent reads one event: its "event:" name and its data: lines joined
// by a newline, enforcing the line and event size limits. Lines of any other
// field, and comments, are dropped, and an event with no data line is not one.
func readSSEEvent(ctx context.Context, reader *bufio.Reader) (SSEEvent, error) {
	var (
		lines []string
		name  string
	)
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return SSEEvent{}, err
		}
		raw, err := readBoundedLine(reader)
		line := strings.TrimSuffix(raw, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			payload := strings.TrimSpace(after)
			total += len(payload)
			if total > maxSSEEventBytes {
				return SSEEvent{}, fmt.Errorf("gateway: SSE event exceeds %d bytes", maxSSEEventBytes)
			}
			lines = append(lines, payload)
		} else if after, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(after)
		}
		if line == "" && err == nil {
			if len(lines) > 0 {
				return SSEEvent{Name: name, Data: strings.Join(lines, "\n")}, nil
			}
			name = ""
		}
		if err != nil {
			// The stream ended before the blank line that completes the event:
			// the event is discarded, and the end is the caller's to treat as
			// a cut stream (or a finished one).
			return SSEEvent{}, err
		}
	}
}
