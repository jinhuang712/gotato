package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A JSON body is one value: data after it is refused, and the request does not
// run. A chunked body, which has no length to check first, is bound the same.
func TestABodyWithTrailingDataIsRefused(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Close(context.Background()) }()
	rec, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(runner, httpapi.WithMaxBody(256))
	for name, tc := range map[string]struct {
		body   string
		chunks bool
		want   int
	}{
		"a second object":                 {body: `{"prompt":"hello"}{"prompt":"again"}`, want: http.StatusBadRequest},
		"garbage":                         {body: `{"prompt":"hello"} nonsense`, want: http.StatusBadRequest},
		"white space":                     {body: "{\"prompt\":\"hello\"}\n  \n", want: http.StatusOK},
		"trailing data in a chunked body": {body: `{"prompt":"hello"}{}`, chunks: true, want: http.StatusBadRequest},
		"a chunked tail past the limit":   {body: `{"prompt":"hello"}` + strings.Repeat(" ", 512) + `x`, chunks: true, want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tc.body)
			if tc.chunks {
				body = io.MultiReader(strings.NewReader(tc.body)) // hides the length: ContentLength is -1
			}
			request := httptest.NewRequest("POST", "/v1/sessions/"+rec.ID()+"/runs", body)
			request.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
