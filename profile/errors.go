package profile

import (
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	// ErrNotRegistered is returned for a name no registration answers to.
	ErrNotRegistered = gotato.ErrorOf(gotato.CodeInvalidArgument, "profile: not registered")
	// ErrUnknownName matches an *UnknownNameError: a model, Tool or ToolSet
	// name the Catalog does not hold.
	ErrUnknownName = gotato.ErrorOf(gotato.CodeInvalidArgument, "profile: the Catalog has no such name")
	// ErrWidens matches a *WidenError: a TaskSpec that goes beyond its
	// AgentProfile.
	ErrWidens = gotato.ErrorOf(gotato.CodeInvalidArgument, "profile: a TaskSpec may only narrow its AgentProfile")
	// ErrNotData is returned when an AgentProfile is encoded as a document
	// and holds Go values (ToolSources, Extensions, a part and the like) that a
	// document cannot carry.
	ErrNotData = gotato.ErrorOf(gotato.CodeInvalidArgument, "profile: the AgentProfile holds Go values a document cannot carry")
)

// Kinds of the names a Catalog resolves, as UnknownNameError.Kind says.
const (
	KindModel   = "model"
	KindTool    = "tool"
	KindToolSet = "toolset"
)

// UnknownNameError says a stored definition names something the Catalog does
// not hold. errors.Is(err, ErrUnknownName) matches it, and it is
// invalid_argument.
type UnknownNameError struct {
	// Kind is KindModel, KindTool or KindToolSet.
	Kind string
	Name string
}

func (e *UnknownNameError) Error() string {
	return fmt.Sprintf("profile: the Catalog has no %s %q", e.Kind, e.Name)
}

// Is matches ErrUnknownName.
func (e *UnknownNameError) Is(target error) bool { return target == ErrUnknownName }

// Unwrap gives the invalid_argument error that classifies this one.
func (e *UnknownNameError) Unwrap() error { return invalid(e.Error()) }

// WidenError says a TaskSpec goes beyond its AgentProfile in one bound.
// errors.Is(err, ErrWidens) matches it, and it is invalid_argument.
type WidenError struct {
	// Task is the TaskSpec's name and Profile its AgentProfile's.
	Task    string
	Profile string
	// Bound is the bound that widens: "tools", "toolsets", "max_effect",
	// "run_timeout" or "max_tokens".
	Bound string
	// Detail says how.
	Detail string
}

func (e *WidenError) Error() string {
	return fmt.Sprintf("profile: TaskSpec %q widens AgentProfile %q: %s: %s", e.Task, e.Profile, e.Bound, e.Detail)
}

// Is matches ErrWidens.
func (e *WidenError) Is(target error) bool { return target == ErrWidens }

// Unwrap gives the invalid_argument error that classifies this one.
func (e *WidenError) Unwrap() error { return invalid(e.Error()) }

// notRegistered is ErrNotRegistered with the kind and the name.
func notRegistered(kind, name string) error {
	return fmt.Errorf("%w: %s %q", ErrNotRegistered, kind, name)
}
