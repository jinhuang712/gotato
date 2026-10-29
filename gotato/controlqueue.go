package gotato

import (
	"slices"
	"sync"
)

// controlKind is the queue a control message belongs to, for its limit and
// for when a Run takes it.
type controlKind int

const (
	// kindSteer holds Steers and Interrupts.
	kindSteer controlKind = iota
	kindFollowUp
	kindNotice
	controlKinds
)

// kindOf returns the queue a message of source is accepted into.
func kindOf(source MessageSource) controlKind {
	switch source {
	case MessageSourceFollowUp:
		return kindFollowUp
	case MessageSourceNotice:
		return kindNotice
	}
	return kindSteer
}

// control is a Message that waits in the queue.
type control struct {
	seq     uint64
	message Message
	source  MessageSource
	// kind is the queue it waits in: a Requeued FollowUp may wait among the
	// Steers.
	kind controlKind
	// number is the notice_queued of a notice.
	number uint64
}

// controlQueue holds every message that waits for a Run: Steers, Interrupts,
// FollowUps and notices, in the order they were accepted. A place is reserved
// before the message's queued Event is written, so a message that is told it
// is queued always has its place. mu is a leaf lock: the caller may hold
// Session.runMu, and takes nothing while it holds mu.
type controlQueue struct {
	mu     sync.Mutex
	items  []control
	held   [controlKinds]int
	limit  [controlKinds]int
	sealed bool
	seq    uint64
}

func newControlQueue(steer, followUp, notice int) *controlQueue {
	return &controlQueue{limit: [controlKinds]int{kindSteer: steer, kindFollowUp: followUp, kindNotice: notice}}
}

// count returns the messages of kind that wait. The caller holds mu.
func (q *controlQueue) count(kind controlKind) int {
	n := 0
	for _, item := range q.items {
		if item.kind == kind {
			n++
		}
	}
	return n
}

// reserve claims a place in the queue of kind, and reports false when it is
// full. A place is given back by fill or release.
// [CONCURRENCY] the limit check and the claim of the place are one step, or two callers both pass a check that neither has filled.
func (q *controlQueue) reserve(kind controlKind) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.count(kind)+q.held[kind] >= q.limit[kind] {
		return false
	}
	q.held[kind]++
	return true
}

// release gives back a place reserved and not filled.
func (q *controlQueue) release(kind controlKind) {
	q.mu.Lock()
	q.held[kind]--
	q.mu.Unlock()
}

// fill puts a message in the place reserved for it, stamping its order. It
// reports false when the queue is sealed: the place is given back, and nothing
// is queued.
func (q *controlQueue) fill(c control) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.held[c.kind]--
	if q.sealed {
		return false
	}
	q.seq++
	c.seq = q.seq
	q.items = append(q.items, c)
	return true
}

// add puts a message in the queue of kind when it has room, and reports
// whether it did: reserve and fill in one step, for a message that was
// reported queued already.
func (q *controlQueue) add(c control) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	kind := c.kind
	if q.sealed || q.count(kind)+q.held[kind] >= q.limit[kind] {
		return false
	}
	q.seq++
	c.seq = q.seq
	q.items = append(q.items, c)
	return true
}

// interrupting reports whether an Interrupt waits in the queue.
func (q *controlQueue) interrupting() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.ContainsFunc(q.items, func(c control) bool { return c.source == MessageSourceInterrupt })
}

// take removes what a Run takes at a boundary, in the order it was accepted:
// the Steers and Interrupts, and the FollowUps too when the Run is settling,
// and the notices with them. A notice never keeps a Run going: at settlement
// they are taken only along with another message. The messages are the Run's
// until the lease says what became of each.
func (q *controlQueue) take(settling bool) *lease {
	q.mu.Lock()
	defer q.mu.Unlock()
	withNotices := !settling
	for _, item := range q.items {
		if kind := item.kind; kind == kindSteer || (settling && kind == kindFollowUp) {
			withNotices = true
			break
		}
	}
	return q.lease(func(c control) bool {
		kind := c.kind
		return kind == kindSteer || (settling && kind == kindFollowUp) || (withNotices && kind == kindNotice)
	})
}

// takeNotices removes the notices that wait, in order, as take does.
func (q *controlQueue) takeNotices() *lease {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lease(func(c control) bool { return c.kind == kindNotice })
}

// lease is the messages a Run took from the queue, which it must settle: each
// is committed or dropped by the Run, or goes back to the queue. A message is
// settled the moment the Run commits it to the Transcript, or drops it, so it
// is never put back after that.
type lease struct {
	q *controlQueue
	// items are the messages taken; the first settled of them are done.
	items   []control
	settled int
}

// lease takes the messages that pick selects. The caller holds mu.
func (q *controlQueue) lease(pick func(control) bool) *lease {
	return &lease{q: q, items: q.split(pick)}
}

// done says the Run settled the message it is at. It is safe on a nil lease,
// the case of a message that never waited in the queue.
func (l *lease) done() {
	if l != nil {
		l.settled++
	}
}

// putBack returns the messages that are not settled to the front of the
// queue, in their order: they were accepted before everything that waits now.
// [DATA-LOSS] a message taken and not committed goes back, or the Run's failure loses a message that was reported queued.
func (l *lease) putBack() {
	rest := l.items[l.settled:]
	if len(rest) == 0 {
		return
	}
	l.q.mu.Lock()
	l.q.items = append(slices.Clone(rest), l.q.items...)
	l.q.mu.Unlock()
	l.settled = len(l.items)
}

// drain removes and returns every message that waits, notices only when
// withNotices.
func (q *controlQueue) drain(withNotices bool) []control {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.split(func(c control) bool { return withNotices || c.kind != kindNotice })
}

// seal closes the queue for good: nothing is put in it after it. It removes
// and returns what waits.
func (q *controlQueue) seal() []control {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sealed = true
	return q.split(func(control) bool { return true })
}

// split removes the messages that pick selects and returns them in order. The
// caller holds mu.
func (q *controlQueue) split(pick func(control) bool) []control {
	var out []control
	q.items = slices.DeleteFunc(q.items, func(c control) bool {
		if !pick(c) {
			return false
		}
		out = append(out, c)
		return true
	})
	return out
}
