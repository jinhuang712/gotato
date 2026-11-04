package http_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/testkit"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial: connection refused")
}

// a forward that fails answers 502 with the message "Bad Gateway".
func TestForwardFailureSays502BadGateway(t *testing.T) {
	store := memStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true,
		httpapi.WithForwarding(&http.Client{Transport: failingTransport{}}, anyAddress))
	id, wait := running(t, a, g)
	defer func() { close(g.release); wait() }()

	resp, body := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"text": "via b"})
	var failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway || failure.Code != "forward_failed" {
		t.Fatalf("forward failure = %d %s, want 502 forward_failed", resp.StatusCode, body)
	}
	if failure.Message != http.StatusText(http.StatusBadGateway) {
		t.Fatalf("502 message = %q, want %q (body %s)", failure.Message, http.StatusText(http.StatusBadGateway), body)
	}
}
