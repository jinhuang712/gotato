package host_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/testkit"
)

// Notice on a draining Runner must be host.ErrDraining.
func TestNoticeWhileDrainingIsErrDraining(t *testing.T) {
	runner, err := host.New(host.Config{
		Store:    memStore(),
		Profiles: registry(t, profile.AgentProfile{Name: "a", Model: testkit.NewFakeModel(testkit.Text("ok"))}), DefaultAgent: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Notice(context.Background(), host.NoticeRequest{SessionID: "s", Kind: "k", Text: "t"})
	if !errors.Is(err, host.ErrDraining) {
		t.Fatalf("Notice while draining = %v (%#v), want errors.Is ErrDraining", err, err)
	}
}
