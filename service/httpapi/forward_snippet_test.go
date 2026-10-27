package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// forwardMessage is the snippet FEATURES.md (G-F26) shows an embedder that
// forwards a message it was not the writer of, with its own HTTP client.
func forwardMessage(ctx context.Context, runner *service.Runner, client *http.Client, req service.MessageRequest, forwarded bool) (*http.Response, error) {
	_, err := runner.Send(ctx, req)
	addr, ok := service.WriterOf(err)
	if !ok || addr == "" || forwarded {
		return nil, err // not a not_writer, nowhere to go, or already forwarded once
	}
	body, _ := json.Marshal(map[string]string{"kind": string(req.Kind), "text": req.Text})
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/v1/sessions/"+req.SessionID+"/messages", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set(httpapi.ForwardedHeader, "true") // the writer never forwards it again
	return client.Do(hr)                           // your own client, timeout and credentials
}

func TestEmbedderForwardsANotWriterMessageWithItsOwnClient(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)

	resp, err := forwardMessage(context.Background(), b.runner, http.DefaultClient, service.MessageRequest{SessionID: id, Kind: service.MessageKindSteer, Text: "via the embedder"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forwarded message = %d, want the writer's 202", resp.StatusCode)
	}
	close(g.release)
	wait()
}
