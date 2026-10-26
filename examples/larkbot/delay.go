package main

import (
	"context"
	"io"
	"net"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// delayModel sleeps before every model call, so a test can keep a Run in flight.
type delayModel struct {
	gotato.Model
	d time.Duration
}

func (m delayModel) Stream(ctx context.Context, r gotato.ModelRequest) (gotato.ModelStream, error) {
	t := time.NewTimer(m.d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.Model.Stream(ctx, r)
}

// Close closes the wrapped model when it can be closed (a model that holds a connection).
func (m delayModel) Close() error {
	if c, ok := m.Model.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func withDelay(m gotato.Model, d time.Duration) gotato.Model {
	if d <= 0 {
		return m
	}
	return delayModel{m, d}
}

func closerOf(m gotato.Model) (io.Closer, bool) { c, ok := m.(io.Closer); return c, ok }

// advertiseAddr is the address peers reach this instance at: the flag, else
// http://<addr> (a bare ":8080" becomes 127.0.0.1:8080).
func advertiseAddr(flagValue, addr string) string {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/")
	}
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
