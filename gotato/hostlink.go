package gotato

import "github.com/jinhuang712/gotato/internal/hostlink"

// The calls that only the host of a Session needs are not methods of the
// core's API (decision 048): a library user who sees them could call them by
// mistake, and each is contract once exported. The core sets them as function
// values in internal/hostlink, and package host and testkit assert their types
// once. They are in four groups: the options that tie a Session to its host
// (withFeed, withRunStart, withQueueHandoff, withRequestID), the messages a Run
// leaves (takeQueued, requeue, dropQueued), the Feeds (newFeed,
// newInstanceFeed, mirrorTo, setLabel, publish, publishLabeled) and the count
// of what a Session published (published), and of what a Feed was given
// (lastNumber).
func init() {
	hostlink.WithFeed = withFeed
	hostlink.WithRunStart = withRunStart
	hostlink.WithQueueHandoff = withQueueHandoff
	hostlink.WithRequestID = withRequestID

	hostlink.TakeQueued = func(s *Session) []Queued { return s.takeQueued() }
	hostlink.Requeue = func(s *Session, messages []Queued) []Queued { return s.requeue(messages) }
	hostlink.DropQueued = func(s *Session, messages []Queued, why string) { s.dropQueued(messages, why) }
	hostlink.Published = func(s *Session) uint64 { return s.published() }

	hostlink.FeedLast = func(f *Feed) uint64 { return f.lastNumber() }
	hostlink.NewFeed = newFeed
	hostlink.NewInstanceFeed = newInstanceFeed
	hostlink.MirrorTo = func(f, other *Feed) { f.mirrorTo(other) }
	hostlink.SetLabel = func(f *Feed, label string) { f.setLabel(label) }
	hostlink.Publish = func(f *Feed, event Event) { f.publish(event) }
	hostlink.PublishLabeled = func(f *Feed, event Event, label string) { f.publishLabeled(event, label) }
}
