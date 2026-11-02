package host

import "github.com/jinhuang712/gotato/gotato"

// The error codes only a Runner makes (decision 048). They are gotato.ErrorCode
// values with the strings they always had, so a client reads the same JSON and
// the same HTTP status; they are the service's because the core never returns
// one.
const (
	// CodeNotWriter means a message for a Run reached an Instance that is not
	// the Run's writer. The message did not run, and sent again to the
	// writer, whose advertised address is in Details["writer"] when it has
	// one, it may succeed.
	CodeNotWriter gotato.ErrorCode = "not_writer"
	// CodeDraining means the Instance is draining and takes no new Run. The
	// request did not run, so another Instance can serve it.
	CodeDraining gotato.ErrorCode = "draining"
	// CodeOverloaded means an Instance is past its hard limit and refused to
	// activate a new Session. The request did not run, so sending it to
	// another Instance cannot run it twice.
	CodeOverloaded gotato.ErrorCode = "overloaded"
	// CodeCannotResume means a stream cannot go on from the position a client
	// holds: the Record is not the one it saw, or the number is past its last
	// Event. The client starts over from the first Event. gotato.ErrEventsLost,
	// the end of a Subscription that needs Events its source no longer holds,
	// carries the same string.
	CodeCannotResume gotato.ErrorCode = "cannot_resume"
	// CodeAlreadyExists means a Session with the ID the caller chose exists
	// already, so a create or a fork with that ID did nothing. The ID is taken:
	// choose another, or use the Session that has it.
	CodeAlreadyExists gotato.ErrorCode = "already_exists"
)

// ErrorCodes lists the codes of the service, so a layer that maps codes (a
// transport, a CLI) can check that it maps each one, beside gotato.ErrorCodes,
// which lists those of the core. A new code is added here too; a test fails
// when it is not.
func ErrorCodes() []gotato.ErrorCode {
	return []gotato.ErrorCode{CodeNotWriter, CodeDraining, CodeOverloaded, CodeCannotResume, CodeAlreadyExists}
}

// errorOf builds an error of one of the service's codes with what the code
// says about the request, which gotato.ErrorOf knows for the core's codes only:
//
//	code             not run  retryable
//	draining         yes      yes (on another Instance)
//	overloaded       yes      yes (on another Instance)
//	not_writer       yes      yes (at the writer)
//
// cannot_resume and already_exists are neither: the client starts over or
// chooses another ID.
func errorOf(code gotato.ErrorCode, message string) *gotato.RuntimeError {
	e := gotato.ErrorOf(code, message)
	switch code {
	case CodeDraining, CodeOverloaded, CodeNotWriter:
		e.NotRun, e.Retryable = true, true
	}
	return e
}
