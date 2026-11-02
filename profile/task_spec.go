package profile

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// TaskSpec is a job that runs again, like a thread of an AgentProfile's
// process: a name, the AgentProfile it runs, a Background, and bounds that only
// narrow it. A Run of a TaskSpec opens a new Session, its Background is added
// once after the AgentProfile's instruction, and the Record keeps the TaskSpec
// as it was, so every Instance reads the same Session after the spec changes.
//
// The bounds say less than the AgentProfile, never more (Narrow refuses a
// TaskSpec that widens). A TaskSpec sets Sampling hints only, never a model's
// Facts. Where its result goes and when it runs is the service's own.
type TaskSpec struct {
	Name string
	// Profile is the name of the AgentProfile the Task runs.
	Profile string
	// Background is added to the Session's instruction, once, after the
	// AgentProfile's.
	Background string
	// Tools names the Go Tools the Task keeps, by Tool ID: a subset of the
	// AgentProfile's Tools. Nil keeps them all, and an empty list keeps none.
	// A Task that names Tools has no ToolSources, whose Tools cannot be named.
	Tools []string
	// ToolSets names the ToolSets the Task keeps: a subset of the
	// AgentProfile's. Nil keeps them all, and an empty list keeps none.
	ToolSets []string
	// MaxEffect is a cap no higher than the AgentProfile's. Empty keeps it.
	MaxEffect gotato.ToolEffect
	// RunTimeout is a deadline no longer than the AgentProfile's. Zero keeps
	// it; a negative value is refused.
	RunTimeout time.Duration
	// Sampling is laid over the AgentProfile's, field by field. Its MaxTokens
	// is no higher than the AgentProfile's, and the other fields may differ.
	Sampling gotato.Sampling
}

// validate checks the shape of a TaskSpec on its own, before any AgentProfile
// is looked up.
func (t TaskSpec) validate() error {
	switch {
	case strings.TrimSpace(t.Name) == "":
		return invalid("TaskSpec.Name is empty")
	case strings.TrimSpace(t.Profile) == "":
		return invalid(fmt.Sprintf("TaskSpec %q names no Profile", t.Name))
	case !t.MaxEffect.Valid():
		return invalid(fmt.Sprintf("TaskSpec %q has unknown MaxEffect %q", t.Name, t.MaxEffect))
	case t.RunTimeout < 0:
		return invalid(fmt.Sprintf("TaskSpec %q has a negative RunTimeout", t.Name))
	}
	return nil
}

// Narrow checks that the TaskSpec only narrows p, its AgentProfile: its Tools
// and ToolSets are some of p's, its MaxEffect is no higher, its RunTimeout no
// longer, and its Sampling.MaxTokens no higher. It returns a *WidenError for
// the first bound that widens, or an *UnknownNameError when a name is p's and
// the Catalog's alike unknown (the Catalog may be nil).
//
// An AgentProfile with no cap, no deadline or no MaxTokens (its own, else the
// Model's default) has nothing to widen: a bound the TaskSpec sets is narrower.
func (t TaskSpec) Narrow(p AgentProfile, catalog *Catalog) error {
	widens := func(bound, detail string) error {
		return &WidenError{Task: t.Name, Profile: p.Name, Bound: bound, Detail: detail}
	}
	have := map[string]bool{}
	for _, tool := range p.Tools {
		have[tool.Spec().ID] = true
	}
	for _, id := range t.Tools {
		if have[id] {
			continue
		}
		if _, err := catalog.Tool(id); err != nil {
			return err
		}
		return widens("tools", fmt.Sprintf("Tool %q is not the AgentProfile's", id))
	}
	have = map[string]bool{}
	for _, set := range p.ToolSets {
		have[set.Set.Spec().Name] = true
	}
	for _, name := range t.ToolSets {
		if have[name] {
			continue
		}
		if _, err := catalog.ToolSet(name); err != nil {
			return err
		}
		return widens("toolsets", fmt.Sprintf("ToolSet %q is not the AgentProfile's", name))
	}
	if t.MaxEffect != gotato.ToolEffectUnspecified && p.MaxEffect != gotato.ToolEffectUnspecified && !t.MaxEffect.Within(p.MaxEffect) {
		return widens("max_effect", fmt.Sprintf("%s is above the AgentProfile's %s", t.MaxEffect, p.MaxEffect))
	}
	if t.RunTimeout > 0 {
		limit := p.RunTimeout
		if limit == 0 {
			limit = DefaultRunTimeout
		}
		if limit > 0 && t.RunTimeout > limit {
			return widens("run_timeout", fmt.Sprintf("%s is longer than the AgentProfile's %s", t.RunTimeout, limit))
		}
	}
	if limit := p.maxTokens(); t.Sampling.MaxTokens > 0 && limit > 0 && t.Sampling.MaxTokens > limit {
		return widens("max_tokens", fmt.Sprintf("%d is above the AgentProfile's %d", t.Sampling.MaxTokens, limit))
	}
	return nil
}

// maxTokens is the output limit the profile's Sessions send: its own Sampling's,
// else the Model's default, 0 for none.
func (p AgentProfile) maxTokens() uint32 {
	if p.Sampling.MaxTokens > 0 {
		return p.Sampling.MaxTokens
	}
	return p.Model.Config().Sampling.MaxTokens
}

// Apply returns p as the TaskSpec runs it: the Background after the
// instruction, the Tools and ToolSets the TaskSpec keeps, its cap, deadline and
// Sampling over p's. p is not changed. Apply trusts that Narrow said yes.
func (t TaskSpec) Apply(p AgentProfile) AgentProfile {
	switch {
	case t.Background == "":
	case p.Instruction == "":
		p.Instruction = t.Background
	default:
		p.Instruction += "\n\n" + t.Background
	}
	if t.Tools != nil {
		p.Tools = slices.DeleteFunc(slices.Clone(p.Tools), func(tool gotato.Tool) bool { return !slices.Contains(t.Tools, tool.Spec().ID) })
		p.ToolSources = nil
	}
	if t.ToolSets != nil {
		p.ToolSets = slices.DeleteFunc(slices.Clone(p.ToolSets), func(set ToolSet) bool { return !slices.Contains(t.ToolSets, set.Set.Spec().Name) })
	}
	if t.MaxEffect != gotato.ToolEffectUnspecified {
		p.MaxEffect = t.MaxEffect
	}
	if t.RunTimeout != 0 {
		p.RunTimeout = t.RunTimeout
	}
	if t.Sampling.Temperature != nil {
		p.Sampling.Temperature = t.Sampling.Temperature
	}
	if t.Sampling.MaxTokens != 0 {
		p.Sampling.MaxTokens = t.Sampling.MaxTokens
	}
	if t.Sampling.ReasoningEffort != "" {
		p.Sampling.ReasoningEffort = t.Sampling.ReasoningEffort
	}
	if t.Sampling.ReasoningSummary != "" {
		p.Sampling.ReasoningSummary = t.Sampling.ReasoningSummary
	}
	return p
}
