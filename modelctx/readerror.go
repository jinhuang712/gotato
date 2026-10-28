package modelctx

import "fmt"

// readError is the failure of a read Tool (read_result, read_segment). Its text
// is written for the model, so it is public (gotato.PublicError); the error of
// a store behind it is not kept, because it may name what the model must not
// read.
type readError string

func (e readError) Error() string  { return string(e) }
func (e readError) Public() string { return string(e) }

func readErrorf(format string, args ...any) error {
	return readError(fmt.Sprintf(format, args...))
}
