package service

// SetBeforeDetach sets the seam that holds a Run after it has settled and
// before its live Session stops taking messages.
func (r *Runner) SetBeforeDetach(fn func()) { r.beforeDetach = fn }
