package gateway

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

// A Recap is written outside a Session, so no one drops what a cut stream
// already delivered: the gateway does not send the request again, and the
// Recap fails rather than join the cut text with a second reply.
func TestARecapOverACutStreamFailsWithoutARestart(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			segment := gotato.Segment{Number: 1, Messages: []gotato.Message{gotato.UserMessage("a question [quirk:cut-after-text]")}}
			message, err := modelctx.ModelRecap{Model: client}.WriteMergeRecap(context.Background(), modelctx.MergeRequest{Segments: []gotato.Segment{segment, segment}})
			if err == nil || gotato.TextOf(message) != "" || len(fake.Requests()) != 1 {
				t.Fatalf("text = %q, err = %v, %d requests", gotato.TextOf(message), err, len(fake.Requests()))
			}
		})
	}
}
