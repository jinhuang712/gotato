package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

// stream is the one stream loop of every wire API. It owns the HTTP response,
// the SSE reader and the restart after a cut, and hands each event to the
// Decoder of the wire. Nothing under it reads the bytes of the reply, so the
// guard against repeating output on a restart (restart.go) holds for any wire.
type stream struct {
	client  *Client
	newDec  func() Decoder
	decoder Decoder
	restart restarter

	response  *http.Response
	reader    *bufio.Reader
	closeOnce sync.Once

	queue []gotato.ModelEvent
	// done is set when the Decoder says the reply is complete, and err when
	// the reply failed: both once the queue is drained.
	done bool
	err  error
}

func newStream(client *Client, response *http.Response, newDecoder func() Decoder, open func(context.Context) (*http.Response, error)) *stream {
	return &stream{
		client:   client,
		newDec:   newDecoder,
		decoder:  newDecoder(),
		restart:  restarter{client: client, open: open},
		response: response,
		reader:   bufio.NewReader(response.Body),
	}
}

// Recv returns the next event. Events the Decoder returned with a failure are
// delivered before it, and the failure is returned by every Recv after, so a
// later call never reads as a clean end.
func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.err != nil {
			return gotato.ModelEvent{}, s.err
		}
		if s.done {
			return gotato.ModelEvent{}, io.EOF
		}
		event, err := readSSEEvent(ctx, s.reader)
		if err != nil {
			if errors.Is(err, io.EOF) && s.decodeEnd() {
				continue
			}
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		s.decode(event)
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// decode hands one event to the Decoder and takes what it yields.
func (s *stream) decode(event SSEEvent) {
	events, done, err := s.decoder.Decode(event)
	s.queue = append(s.queue, events...)
	s.done = s.done || done
	if err == nil {
		return
	}
	// An error is not an interruption: it is not sent again as a cut stream
	// is. A Failure is classified, so an overflow is reported as one.
	var failure Failure
	if errors.As(err, &failure) {
		err = s.client.fail(failure)
	}
	s.err = err
}

// decodeEnd tells the Decoder the body ended between events, and reports
// whether that settled the stream: the reply was complete, or it failed.
func (s *stream) decodeEnd() bool {
	s.decode(SSEEvent{End: true})
	return s.done || s.err != nil
}

// reopen continues on a re-issued response, discarding everything decoded from
// the interrupted one (nothing of it reached the caller, or the Session was
// told to drop it).
func (s *stream) reopen(response *http.Response) {
	if s.response != nil && s.response.Body != nil {
		_ = s.response.Body.Close()
	}
	s.response = response
	s.reader = bufio.NewReader(response.Body)
	s.decoder = s.newDec()
	s.queue = nil
	s.done = false
	s.err = nil
}

// Close closes the response body. Closing it twice is safe.
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}
