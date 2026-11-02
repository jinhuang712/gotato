// Package hostlink is where the core hands the host calls it keeps for the
// service that hosts its Sessions, so they are not part of the core's API
// (decision 048).
//
// A Runner builds a Session from a Record, gives it the Feed of the Record,
// follows the messages a Run left queued with the next Run, and publishes its
// own Events: calls that only a host needs, and that a library user could call
// by mistake if they were methods of the core. Each is a function value the
// core sets in its init, in the way grpc-go's internal package does. A package
// that uses one (host, testkit) asserts its type once, when it is loaded, so
// a drift between the two fails the first program that imports both instead of
// a call in the middle of a Run:
//
//	var withFeed = hostlink.WithFeed.(func(*gotato.Feed) gotato.SessionOption)
//
// The types are the core's, and this package may not import the core (the core
// imports it), so each value is an any and its type is stated here, in the
// comment on it.
//
// Nothing here is a second API: a host outside this module cannot import an
// internal package. If a second host of Gotato Sessions is ever written, this
// package becomes its public package. It imports the standard library only.
package hostlink

// The host calls. Each is nil until the core is loaded, and set by its init.
var (
	// WithFeed is func(*gotato.Feed) gotato.SessionOption: the Session
	// publishes its Events to a Feed the host owns instead of one of its own.
	WithFeed any
	// WithRunStart is func(func(gotato.RunID)) gotato.SessionOption: the
	// Session calls it with each Run's ID as the Run starts.
	WithRunStart any
	// WithQueueHandoff is func() gotato.SessionOption: a Session keeps the
	// messages still queued when a Run completes or fails, for the host to
	// take.
	WithQueueHandoff any
	// WithRequestID is func(string) gotato.SessionOption: the caller's
	// identifier of the request that makes the Session run.
	WithRequestID any

	// TakeQueued is func(*gotato.Session) []gotato.Queued: it removes and
	// returns the messages that wait in a Session's queues, without an Event.
	TakeQueued any
	// Requeue is func(*gotato.Session, []gotato.Queued) []gotato.Queued: it
	// puts messages taken from another Session into this one's queues and
	// returns those that did not fit.
	Requeue any
	// DropQueued is func(*gotato.Session, []gotato.Queued, string): it reports
	// messages that will not run, with why.
	DropQueued any
	// Published is func(*gotato.Session) uint64: how many Events the Session
	// has published.
	Published any
	// FeedLast is func(*gotato.Feed) uint64: the Number of the last stored
	// Event a Feed was given, which counts a Run that stored nothing.
	FeedLast any

	// NewFeed is func(gotato.EventSource) *gotato.Feed: the Feed of one Record.
	NewFeed any
	// NewInstanceFeed is func() *gotato.Feed: the Feed of an Instance, which
	// mixes every Session's Events with its own.
	NewInstanceFeed any
	// MirrorTo is func(*gotato.Feed, *gotato.Feed): every Event published to
	// the first is also published to the second.
	MirrorTo any
	// SetLabel is func(*gotato.Feed, string): the label that goes with every
	// Event of a Feed when it is mirrored.
	SetLabel any
	// Publish is func(*gotato.Feed, gotato.Event): puts an Event in every
	// subscriber's queue.
	Publish any
	// PublishLabeled is func(*gotato.Feed, gotato.Event, string): Publish with
	// the label a subscriber's filter sees.
	PublishLabeled any
)
