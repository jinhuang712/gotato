package reftasks

// publicError is an error whose text is meant for the model: the reference
// tools return it for a failure the model can act on, and Gotato passes its
// text on (gotato.PublicError).
type publicError string

func (e publicError) Error() string  { return string(e) }
func (e publicError) Public() string { return string(e) }
