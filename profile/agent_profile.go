// Package profile holds the definitions a service runs its Sessions from, and
// where they come from (decision 044).
//
// An AgentProfile is a kind of Agent, like a process: its model, instruction,
// tools, cap and limits. A TaskSpec is a job that runs again, like a thread of
// that process: its AgentProfile, a Background, and bounds that only narrow.
// Each is registered in a Registry one of two ways, inline as a Go value or from
// a store by name, and the Registry answers one lookup. A stored definition is
// data only (names of tools, ToolSets and a model), resolved through a Catalog
// the service fills with its Go values: a row selects what the code offers and
// never adds to it.
//
// The core runs without this package. A host that rebuilds a Session by name
// on any Instance needs at least one AgentProfile registered in code.
package profile

import (
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

// DefaultRunTimeout is how long a Run may last when neither the AgentProfile's
// RunTimeout, the TaskSpec's nor the request's Timeout says otherwise.
const DefaultRunTimeout = 30 * time.Minute

// ToolSetMode says how the Model sees one of an AgentProfile's ToolSets. The
// zero value is ToolSetListed.
type ToolSetMode string

// The ways a ToolSet is installed.
const (
	// ToolSetListed: the catalog lists the ToolSet's description and all its
	// Tool names, and the Model loads the Tools it needs with search_tools.
	ToolSetListed ToolSetMode = "listed"
	// ToolSetFolded: the catalog shows only the description and a count; the
	// Model lists the Tools with the toolset parameter of search_tools, then
	// loads by name.
	ToolSetFolded ToolSetMode = "folded"
	// ToolSetActive: every Tool is visible always, as if it were a base Tool,
	// never in the catalog and never unloaded.
	ToolSetActive ToolSetMode = "active"
)

// Valid reports whether the mode is one of the three, or empty (listed).
func (m ToolSetMode) Valid() bool {
	switch m {
	case "", ToolSetListed, ToolSetFolded, ToolSetActive:
		return true
	}
	return false
}

// ToolSet is a gotato.ToolSet an AgentProfile installs, with how the Model
// sees it.
type ToolSet struct {
	Set  gotato.ToolSet
	Mode ToolSetMode
}

// AgentProfile is a named, reusable definition of an Agent: the model,
// instruction, tools, cap and limits its Sessions run. The Runner builds a live
// Session from it for each Run, and builds every gotato.SessionOption itself:
// nothing a profile holds is a function that runs after the Runner's own
// options, so a profile cannot replace the Writer, the cap or a Tool a Session
// switched off. A Session has no instruction or ceiling of its own; another one
// is another AgentProfile. How the host keeps its Sessions (warm time, the
// time-away notice) is the host's setting, not the profile's.
// Sharing a Model client between profiles and Runs is expected; nothing here is
// mutable Run state.
//
// Three fields give a profile its Tools; each has one job:
//   - Tools: Go Tools (gotato.NewFuncTool and the like), fixed for the
//     profile's life.
//   - ToolSets: a gotato.ToolSet, such as an MCP server's, and how the Model
//     sees it (listed, always visible, or folded behind search_tools).
//   - ToolSources: Tools that change between Turns, asked again at the
//     start of each Turn. A ToolSet is not a ToolSources entry.
//
// A profile read from a store has the fields that are data: Name, Instruction,
// ModelName and Model, Tools and ToolSets chosen from the Catalog, MaxEffect,
// Sampling, RunTimeout, Limits and the numbers of Compact. The rest holds Go
// values and is set inline only.
type AgentProfile struct {
	Name string
	// Model is the model client. ModelName is its name in the Catalog, which a
	// stored profile names, and the label the Runner records for a Session.
	Model       gotato.Model
	ModelName   string
	Instruction string
	// Tools are Go Tools, the same for every Turn.
	Tools []gotato.Tool
	// ToolSets are the ToolSets the profile installs, each with a Mode. A Tool of
	// one is under MaxEffect like any other.
	ToolSets []ToolSet
	// ToolSources supply Tools that change between Turns (gotato.ToolSource).
	// Tools, ToolSources, read_result and read_segment are one ToolSource to
	// the Session: a Tool ID that two of them supply fails it whole, as any
	// bad Tool does (gotato.Tool). A ToolSet goes in ToolSets instead.
	ToolSources []gotato.ToolSource
	// ToolSearcher replaces the built-in search of search_tools
	// (gotato.ToolSearcher). Nil keeps the built-in one.
	ToolSearcher gotato.ToolSearcher
	// MaxEffect is the highest Effect a Tool of this profile may have. A Tool
	// above it is hidden from the model and from Runner.Tools, and no Session
	// can switch it on. Empty means no cap (gotato.WithMaxEffect).
	MaxEffect gotato.ToolEffect
	// Sampling is the sampling and reasoning hints sent on every model call
	// of the profile's Sessions, over the Model's own defaults
	// (gotato.WithSampling). It sets hints only, never a model's Facts.
	Sampling gotato.Sampling
	// Extensions are installed after the Runner's own, in this order
	// (gotato.WithExtension).
	Extensions []any
	// ContextBuilder defaults to gotato.FullHistoryContext().
	ContextBuilder gotato.ContextBuilder
	Limits         *gotato.CoreLimits
	// ReadBack replaces the Tools that read back a Segment's Messages and a
	// spilled Tool Result, and the page size of that read (gotato.ReadBack).
	// Nil keeps the Runner's own, modelctx.ReadBack over its store.
	ReadBack gotato.ReadBack
	// RunTimeout bounds one Run of this profile; the Run settles as
	// deadline_exceeded. Zero means DefaultRunTimeout, negative means no
	// deadline. A RunRequest's Timeout replaces it, and a deadline on the
	// caller's context that comes sooner ends the Run sooner. The core sets
	// no Run deadline of its own: there a Run's deadline is its ctx.
	RunTimeout time.Duration
	// Compact is how the profile's Sessions compact: the frame and its parts
	// (modelctx.Compactor). Its numbers say when they compact by themselves:
	// past the ceiling, which is the Model's window less a reserve unless
	// CeilingTokens sets one (a negative CeilingTokens turns it off), and once
	// when the provider refuses a request as too long. The ceiling is the
	// profile's alone: no Session overrides it. A part left nil is its default; a
	// part the profile sets replaces that step alone, and the frame keeps the
	// guarantees whatever it returns. A Recap writer that names no Model gets
	// the profile's.
	Compact modelctx.Compactor
}

// validate checks what cannot work as set, once the profile is a value: the
// Registry calls it for every profile it hands out, inline or from a store.
func (p AgentProfile) validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return invalid("AgentProfile.Name is empty")
	}
	if p.Model == nil {
		return invalid(fmt.Sprintf("AgentProfile %q has no Model", p.Name))
	}
	if !p.MaxEffect.Valid() {
		return invalid(fmt.Sprintf("AgentProfile %q has unknown MaxEffect %q", p.Name, p.MaxEffect))
	}
	for i, set := range p.ToolSets {
		if set.Set == nil {
			return invalid(fmt.Sprintf("AgentProfile %q has a nil ToolSet at ToolSets[%d]", p.Name, i))
		}
		if !set.Mode.Valid() {
			return invalid(fmt.Sprintf("AgentProfile %q has unknown ToolSet Mode %q", p.Name, set.Mode))
		}
	}
	for i, extension := range p.Extensions {
		if extension == nil {
			return invalid(fmt.Sprintf("AgentProfile %q has a nil Extension at Extensions[%d]", p.Name, i))
		}
	}
	return nil
}

// invalid is an invalid_argument error with a message of this package.
func invalid(message string) *gotato.RuntimeError {
	return gotato.ErrorOf(gotato.CodeInvalidArgument, "profile: "+message)
}
