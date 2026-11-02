package profile

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// TaskSpecDoc is the stored shape of a TaskSpec, and the shape in which a
// Record keeps the one a Run used. The key a store keeps it under is the
// TaskSpec's name, so Name may be left out of a stored one; when it is there it
// must match.
type TaskSpecDoc struct {
	Name       string `json:"name,omitempty"`
	Profile    string `json:"profile"`
	Background string `json:"background,omitempty"`
	// Tools and ToolSets are the names the Task keeps. Absent or null keeps all
	// of them; an empty list keeps none.
	Tools    []string `json:"tools"`
	ToolSets []string `json:"toolsets"`
	// MaxEffect is a gotato.ToolEffect, such as "read".
	MaxEffect string `json:"max_effect,omitempty"`
	// RunTimeout is a Go duration, such as "10m".
	RunTimeout string          `json:"run_timeout,omitempty"`
	Sampling   gotato.Sampling `json:"sampling,omitzero"`
}

// DecodeTaskSpec reads the document stored under name as a TaskSpec. It refuses
// a field it does not know and a value that cannot work; it looks up no
// AgentProfile, so the check that it only narrows is Narrow's.
func DecodeTaskSpec(name string, doc []byte) (TaskSpec, error) {
	var d TaskSpecDoc
	if err := decodeStrict(doc, &d); err != nil {
		return TaskSpec{}, invalid(fmt.Sprintf("TaskSpec %q: %v", name, err))
	}
	if d.Name != "" && d.Name != name {
		return TaskSpec{}, invalid(fmt.Sprintf("TaskSpec %q: the document is named %q", name, d.Name))
	}
	t := TaskSpec{Name: name, Profile: d.Profile, Background: d.Background, Tools: d.Tools, ToolSets: d.ToolSets, MaxEffect: gotato.ToolEffect(d.MaxEffect), Sampling: d.Sampling}
	if d.RunTimeout != "" {
		var err error
		if t.RunTimeout, err = time.ParseDuration(d.RunTimeout); err != nil {
			return TaskSpec{}, invalid(fmt.Sprintf("TaskSpec %q: run_timeout: %v", name, err))
		}
	}
	return t, t.validate()
}

// EncodeTaskSpec writes t as a document: a store keeps it, and a Record keeps
// the one a Run used. DecodeTaskSpec reads it back as it was.
func EncodeTaskSpec(t TaskSpec) ([]byte, error) {
	d := TaskSpecDoc{Name: t.Name, Profile: t.Profile, Background: t.Background, Tools: t.Tools, ToolSets: t.ToolSets, MaxEffect: string(t.MaxEffect), Sampling: t.Sampling}
	if t.RunTimeout != 0 {
		d.RunTimeout = t.RunTimeout.String()
	}
	return json.Marshal(d)
}
