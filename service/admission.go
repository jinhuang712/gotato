package service

// admit decides whether a Run may start, and registers its handle when it
// may. A draining Instance refuses first; then a Run that would activate a
// Session past MaxActiveSessions is refused, because another Instance can
// still take it, unless a warm Session can be evicted to make room; then
// MaxActiveRuns applies to every Run.
func (r *Runner) admit(handle *runHandle) error {
	for {
		victim, err := r.tryAdmit(handle)
		if victim == nil {
			return err
		}
		// Evicting waits for an idle compaction to unwind, so it happens
		// without the Runner's lock; the count is taken again after it.
		r.dropEntry(victim, "evicted")
	}
}

// tryAdmit is one pass of admit under the Runner's lock. When the Run needs
// room and a warm Session can give it, it returns that Session to evict and
// registers nothing.
func (r *Runner) tryAdmit(handle *runHandle) (victim *warmEntry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return nil, ErrDraining
	}
	if limit := r.admission.MaxActiveSessions; limit > 0 {
		warm, evictable := r.warmCountLocked(handle.sessionID)
		if (handle.sessionID == "" || (len(r.handles[handle.sessionID]) == 0 && !warm)) && r.liveSessionsLocked() >= limit {
			if evictable == nil {
				return nil, ErrOverloaded
			}
			return evictable, nil
		}
	}
	if r.admission.MaxActiveRuns > 0 && r.inflight >= r.admission.MaxActiveRuns {
		return nil, ErrCapacity
	}
	r.inflight++
	r.handles[handle.sessionID] = append(r.handles[handle.sessionID], handle)
	return nil, nil
}

func (r *Runner) release() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idle.Broadcast()
	}
	r.mu.Unlock()
}

// beginLocked counts a caller that takes, or waits for, a Session lock; the
// returned func ends it. Drain waits until none is left.
func (r *Runner) beginLocked() func() {
	r.mu.Lock()
	r.locked++
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.locked--
		if r.locked == 0 {
			r.idle.Broadcast()
		}
		r.mu.Unlock()
	}
}
