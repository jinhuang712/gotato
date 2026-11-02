package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

func TestStreamRejectsOversizedSSELine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: " + strings.Repeat("x", maxSSELineBytes+10)))
	}))
	defer server.Close()
	client, err := testClient(ChatCompletions{}, server.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := stream.Recv(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized SSE line err = %v", err)
	}
}

// An event that the stream ends before its blank line is incomplete: the SSE
// rules discard it, so a cut stream is never read as a whole event.
func TestReadSSEEventDiscardsAnIncompleteEventAtEOF(t *testing.T) {
	for _, in := range []string{`data: {"a":1}`, "data: {\"a\":1}\n", `data: {"a"`} {
		event, err := readSSEEvent(context.Background(), bufio.NewReader(strings.NewReader(in)))
		if !errors.Is(err, io.EOF) || event != (SSEEvent{}) {
			t.Errorf("%q: data = %q, err = %v, want no event and EOF", in, event.Data, err)
		}
	}
	event, err := readSSEEvent(context.Background(), bufio.NewReader(strings.NewReader("data: x\n\n")))
	if err != nil || event.Data != "x" {
		t.Errorf("whole event: data = %q, err = %v", event.Data, err)
	}
}
