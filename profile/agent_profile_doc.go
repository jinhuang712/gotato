package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

// KindAgentProfile is the kind under which a store keeps AgentProfile
// documents (store.Definitions), and KindTaskSpec the kind of TaskSpecs.
const (
	KindAgentProfile = "agent_profile"
	KindTaskSpec     = "task_spec"
)

// AgentProfileDoc is the stored shape of an AgentProfile: data only. It names a
// model, Tools and ToolSets, which a Catalog resolves, and holds numbers and
// text. The key a store keeps it under is the AgentProfile's name, so Name may
// be left out; when it is there it must match.
//
// A field of an AgentProfile that holds a Go value (ToolSources, Extensions,
// ToolSearcher, ContextBuilder, ReadBack and the parts of Compact) has no field
// here: it is set inline.
type AgentProfileDoc struct {
	Name        string `json:"name,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	// Model is the model's name in the Catalog.
	Model string `json:"model"`
	// Tools are Tool IDs and ToolSets names, in the Catalog.
	Tools    []string     `json:"tools,omitempty"`
	ToolSets []ToolSetRef `json:"toolsets,omitempty"`
	// MaxEffect is a gotato.ToolEffect, such as "write_local".
	MaxEffect string          `json:"max_effect,omitempty"`
	Sampling  gotato.Sampling `json:"sampling,omitzero"`
	// RunTimeout is a Go duration, such as "10m". Empty is the default and a
	// negative one means no deadline.
	RunTimeout string `json:"run_timeout,omitempty"`
	// Limits are gotato.CoreLimits, which has no field names of its own: its
	// Go names are the keys, and a duration is in nanoseconds.
	Limits  *gotato.CoreLimits `json:"limits,omitempty"`
	Compact *CompactDoc        `json:"compact,omitempty"`
}

// ToolSetRef names a ToolSet of the Catalog and how the Model sees it.
type ToolSetRef struct {
	Name string      `json:"name"`
	Mode ToolSetMode `json:"mode,omitempty"`
}

// CompactDoc is the numbers of record.CompactPolicy a stored profile sets.
type CompactDoc struct {
	CeilingTokens       int  `json:"ceiling_tokens,omitempty"`
	SegmentBudgetTokens int  `json:"segment_budget_tokens,omitempty"`
	DropReasoning       bool `json:"drop_reasoning,omitempty"`
	// Location is the name of a time zone, such as "Asia/Tokyo". Empty is UTC.
	Location string `json:"location,omitempty"`
}

// DecodeAgentProfile reads the document stored under name as an AgentProfile,
// with the model, Tools and ToolSets resolved through the Catalog (which may be
// nil when the document names none). It refuses a field it does not know, a name
// the Catalog lacks (*UnknownNameError), and a value that cannot work. It does
// not narrow or look anything else up: the Registry does that.
func DecodeAgentProfile(name string, doc []byte, catalog *Catalog) (AgentProfile, error) {
	var d AgentProfileDoc
	if err := decodeStrict(doc, &d); err != nil {
		return AgentProfile{}, invalid(fmt.Sprintf("AgentProfile %q: %v", name, err))
	}
	if d.Name != "" && d.Name != name {
		return AgentProfile{}, invalid(fmt.Sprintf("AgentProfile %q: the document is named %q", name, d.Name))
	}
	model, err := catalog.Model(d.Model)
	if err != nil {
		return AgentProfile{}, err
	}
	p := AgentProfile{Name: name, Model: model, ModelName: d.Model, Instruction: d.Instruction, MaxEffect: gotato.ToolEffect(d.MaxEffect), Sampling: d.Sampling, Limits: d.Limits}
	for _, id := range d.Tools {
		tool, err := catalog.Tool(id)
		if err != nil {
			return AgentProfile{}, err
		}
		p.Tools = append(p.Tools, tool)
	}
	for _, ref := range d.ToolSets {
		set, err := catalog.ToolSet(ref.Name)
		if err != nil {
			return AgentProfile{}, err
		}
		p.ToolSets = append(p.ToolSets, ToolSet{Set: set, Mode: ref.Mode})
	}
	if d.RunTimeout != "" {
		if p.RunTimeout, err = time.ParseDuration(d.RunTimeout); err != nil {
			return AgentProfile{}, invalid(fmt.Sprintf("AgentProfile %q: run_timeout: %v", name, err))
		}
	}
	if c := d.Compact; c != nil {
		p.Compact.CompactPolicy = record.CompactPolicy{CeilingTokens: c.CeilingTokens, SegmentBudgetTokens: c.SegmentBudgetTokens, DropReasoning: c.DropReasoning}
		if c.Location != "" {
			if p.Compact.Location, err = time.LoadLocation(c.Location); err != nil {
				return AgentProfile{}, invalid(fmt.Sprintf("AgentProfile %q: compact.location: %v", name, err))
			}
		}
	}
	return p, p.validate()
}

// EncodeAgentProfile writes p as a document a store can keep, naming its model
// by ModelName and its Tools and ToolSets by their IDs and names. It returns
// ErrNotData for a profile that holds Go values a document cannot carry
// (ToolSources, Extensions, a ToolSearcher, ContextBuilder, ReadBack or a part
// of Compact), and for a Compact that sets a window, which the Runner fills
// from the model. DecodeAgentProfile reads it back through a Catalog that holds
// the same names.
func EncodeAgentProfile(p AgentProfile) ([]byte, error) {
	c := p.Compact
	parts := c.Trigger != nil || c.Cut != nil || c.Builder != nil || c.Recap != nil || c.Merge != nil || c.WindowTokens != 0
	if len(p.ToolSources) > 0 || len(p.Extensions) > 0 || p.ToolSearcher != nil || p.ContextBuilder != nil || p.ReadBack != nil || parts {
		return nil, fmt.Errorf("%w: AgentProfile %q", ErrNotData, p.Name)
	}
	d := AgentProfileDoc{Name: p.Name, Instruction: p.Instruction, Model: p.ModelName, MaxEffect: string(p.MaxEffect), Sampling: p.Sampling, Limits: p.Limits}
	for _, tool := range p.Tools {
		d.Tools = append(d.Tools, tool.Spec().ID)
	}
	for _, set := range p.ToolSets {
		d.ToolSets = append(d.ToolSets, ToolSetRef{Name: set.Set.Spec().Name, Mode: set.Mode})
	}
	if p.RunTimeout != 0 {
		d.RunTimeout = p.RunTimeout.String()
	}
	if c.CeilingTokens != 0 || c.SegmentBudgetTokens != 0 || c.DropReasoning || c.Location != nil {
		d.Compact = &CompactDoc{CeilingTokens: c.CeilingTokens, SegmentBudgetTokens: c.SegmentBudgetTokens, DropReasoning: c.DropReasoning}
		if c.Location != nil {
			d.Compact.Location = c.Location.String()
		}
	}
	return json.Marshal(d)
}

// decodeStrict reads one JSON value into v and refuses a field v does not have
// and anything after the value.
func decodeStrict(doc []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected data after the document")
	}
	return nil
}
