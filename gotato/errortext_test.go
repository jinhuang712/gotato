package gotato_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// The text of a Model's error stays on the Cause: the Message of the Run's
// error is fixed, because the service layer returns it to HTTP clients.
func TestModelErrorTextStaysOffTheMessage(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(erroringModel{errors.New("dial tcp 10.0.0.7:443: secret-host refused")}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	_, err = s.Prompt(context.Background(), gotato.UserMessage("hi"))
	var re *gotato.RuntimeError
	if !errors.As(err, &re) || re.Code != gotato.CodeModelFailure {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(re.Message, "secret-host") || strings.Contains(re.Error(), "secret-host") {
		t.Fatalf("foreign text in the public error: %q", re.Error())
	}
	if !errors.Is(err, re.Cause) || !strings.Contains(re.Cause.Error(), "secret-host") {
		t.Fatalf("the Model's error is not on the Cause: %v", re.Cause)
	}
}

type erroringModel struct{ err error }

func (m erroringModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, m.err
}
