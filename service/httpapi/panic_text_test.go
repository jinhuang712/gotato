package httpapi_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

const panicSentinel = "password=panic-sentinel"

type panickingStopper struct{}

func (panickingStopper) Stop(context.Context, gotato.TurnSnapshot) (gotato.StopDecision, error) {
	panic(panicSentinel)
}

// What an Extension panicked with does not reach the client of a Run.
func TestExtensionPanicTextDoesNotReachTheClient(t *testing.T) {
	r, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Options: []gotato.SessionOption{gotato.WithExtension(panickingStopper{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	s, err := r.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	httpapi.New(r).ServeHTTP(w, httptest.NewRequest("POST", "/v1/sessions/"+s.ID()+"/runs", strings.NewReader(`{"prompt":"hello"}`)))
	if !strings.Contains(w.Body.String(), "extension_failure") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), panicSentinel) {
		t.Fatalf("the panic text reached the client: %s", w.Body.String())
	}
}
