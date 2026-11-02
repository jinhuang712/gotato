package gotato_test

import (
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/hostlink"
)

// The host calls, as a test outside the core reaches them: through
// internal/hostlink, with the type of each asserted once, as the service does.
var (
	withFeed         = hostlink.WithFeed.(func(*gotato.Feed) gotato.SessionOption)
	withRunStart     = hostlink.WithRunStart.(func(func(gotato.RunID)) gotato.SessionOption)
	withQueueHandoff = hostlink.WithQueueHandoff.(func() gotato.SessionOption)
	withRequestID    = hostlink.WithRequestID.(func(string) gotato.SessionOption)
	requeue          = hostlink.Requeue.(func(*gotato.Session, []gotato.Queued) []gotato.Queued)
	newFeed          = hostlink.NewFeed.(func(gotato.EventSource) *gotato.Feed)
	newInstanceFeed  = hostlink.NewInstanceFeed.(func() *gotato.Feed)
	publish          = hostlink.Publish.(func(*gotato.Feed, gotato.Event))
)

// Every host call is set by the core's init, so a program that imports the
// core finds it there: none is nil, and none is exported by the core itself.
func TestEveryHostCallIsSetByTheCore(t *testing.T) {
	for name, call := range map[string]any{
		"WithFeed": hostlink.WithFeed, "WithRunStart": hostlink.WithRunStart, "WithQueueHandoff": hostlink.WithQueueHandoff,
		"WithRequestID": hostlink.WithRequestID, "TakeQueued": hostlink.TakeQueued, "Requeue": hostlink.Requeue,
		"DropQueued": hostlink.DropQueued, "Published": hostlink.Published, "FeedLast": hostlink.FeedLast,
		"NewFeed": hostlink.NewFeed, "NewInstanceFeed": hostlink.NewInstanceFeed, "MirrorTo": hostlink.MirrorTo,
		"SetLabel": hostlink.SetLabel, "Publish": hostlink.Publish, "PublishLabeled": hostlink.PublishLabeled,
	} {
		if call == nil {
			t.Errorf("hostlink.%s is nil: the core did not set it", name)
		}
	}
}
