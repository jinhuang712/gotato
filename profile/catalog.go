package profile

import (
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

// Catalog is what a stored definition may name: the models, Tools and ToolSets
// the service's code offers. A stored AgentProfile selects from it and never
// adds to it, so a row in a database cannot give an Agent a Tool the code does
// not hold. A Tool is named by its Spec ID and a ToolSet by its Spec Name.
//
// A Catalog is built once, by NewCatalog, and read by many; it changes no more.
// The zero value, and a nil *Catalog, hold nothing.
type Catalog struct {
	models   map[string]gotato.Model
	tools    map[string]gotato.Tool
	toolSets map[string]gotato.ToolSet
}

// NewCatalog builds a Catalog from the models by name, the Tools and the
// ToolSets. It refuses a nil model, Tool or ToolSet, an empty name, and a name
// that two Tools, or two ToolSets, share.
func NewCatalog(models map[string]gotato.Model, tools []gotato.Tool, toolSets []gotato.ToolSet) (*Catalog, error) {
	c := &Catalog{models: map[string]gotato.Model{}, tools: map[string]gotato.Tool{}, toolSets: map[string]gotato.ToolSet{}}
	for name, model := range models {
		if name == "" || model == nil {
			return nil, invalid(fmt.Sprintf("Catalog model %q is empty or nil", name))
		}
		c.models[name] = model
	}
	for _, tool := range tools {
		if tool == nil {
			return nil, invalid("Catalog holds a nil Tool")
		}
		id := tool.Spec().ID
		if _, dup := c.tools[id]; id == "" || dup {
			return nil, invalid(fmt.Sprintf("Catalog Tool ID %q is empty or taken", id))
		}
		c.tools[id] = tool
	}
	for _, set := range toolSets {
		if set == nil {
			return nil, invalid("Catalog holds a nil ToolSet")
		}
		name := set.Spec().Name
		if _, dup := c.toolSets[name]; name == "" || dup {
			return nil, invalid(fmt.Sprintf("Catalog ToolSet name %q is empty or taken", name))
		}
		c.toolSets[name] = set
	}
	return c, nil
}

// Model returns the model of that name, or an *UnknownNameError.
func (c *Catalog) Model(name string) (gotato.Model, error) {
	if c != nil {
		if model, ok := c.models[name]; ok {
			return model, nil
		}
	}
	return nil, &UnknownNameError{Kind: KindModel, Name: name}
}

// Tool returns the Tool with that ID, or an *UnknownNameError.
func (c *Catalog) Tool(name string) (gotato.Tool, error) {
	if c != nil {
		if tool, ok := c.tools[name]; ok {
			return tool, nil
		}
	}
	return nil, &UnknownNameError{Kind: KindTool, Name: name}
}

// ToolSet returns the ToolSet with that name, or an *UnknownNameError.
func (c *Catalog) ToolSet(name string) (gotato.ToolSet, error) {
	if c != nil {
		if set, ok := c.toolSets[name]; ok {
			return set, nil
		}
	}
	return nil, &UnknownNameError{Kind: KindToolSet, Name: name}
}
