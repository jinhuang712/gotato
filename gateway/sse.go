package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

// readSSEEvent joins the data: lines of one SSE event, enforcing the line and
// event size limits.
func readSSEEvent(ctx context.Context, reader *bufio.Reader) (string, error) {
	var lines []string
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		raw, err := readBoundedLine(reader)
		line := strings.TrimSuffix(raw, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			payload := strings.TrimSpace(after)
			total += len(payload)
			if total > maxSSEEventBytes {
				return "", fmt.Errorf("gateway: SSE event exceeds %d bytes", maxSSEEventBytes)
			}
			lines = append(lines, payload)
		}
		if line == "" && len(lines) > 0 {
			return strings.Join(lines, "\n"), nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				return strings.Join(lines, "\n"), nil
			}
			return "", err
		}
	}
}
