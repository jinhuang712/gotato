package host

import (
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/hostlink"
)

// The calls the core keeps for the host of its Sessions (decision 048). The core
// hands them over through internal/hostlink as untyped values; their types are
// asserted here, once, when the package is loaded, so a drift between the core
// and the host fails the first program that imports both and not a call in
// the middle of a Run.
var (
	withFeed         = hostlink.WithFeed.(func(*gotato.Feed) gotato.SessionOption)
	withRunStart     = hostlink.WithRunStart.(func(func(gotato.RunID)) gotato.SessionOption)
	withQueueHandoff = hostlink.WithQueueHandoff.(func() gotato.SessionOption)
	withRequestID    = hostlink.WithRequestID.(func(string) gotato.SessionOption)

	takeQueued = hostlink.TakeQueued.(func(*gotato.Session) []gotato.Queued)
	requeue    = hostlink.Requeue.(func(*gotato.Session, []gotato.Queued) []gotato.Queued)
	dropQueued = hostlink.DropQueued.(func(*gotato.Session, []gotato.Queued, string))

	feedLast        = hostlink.FeedLast.(func(*gotato.Feed) uint64)
	newFeed         = hostlink.NewFeed.(func(gotato.EventSource) *gotato.Feed)
	newInstanceFeed = hostlink.NewInstanceFeed.(func() *gotato.Feed)
	mirrorTo        = hostlink.MirrorTo.(func(*gotato.Feed, *gotato.Feed))
	setLabel        = hostlink.SetLabel.(func(*gotato.Feed, string))
	publish         = hostlink.Publish.(func(*gotato.Feed, gotato.Event))
	publishLabeled  = hostlink.PublishLabeled.(func(*gotato.Feed, gotato.Event, string))
)
