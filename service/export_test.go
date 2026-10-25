package service

// SetBeforeDetach sets the seam that holds a Run after it has settled and
// before its live Session stops taking messages.
func (r *Runner) SetBeforeDetach(fn func()) { r.beforeDetach = fn }

// SetBeforeRelease sets the seam that holds a Run after it has stopped taking
// messages and before it releases its Session.
func (r *Runner) SetBeforeRelease(fn func()) { r.beforeRelease = fn }
