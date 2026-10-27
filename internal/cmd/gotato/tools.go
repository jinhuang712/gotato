package main

import (
	"encoding/json"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// toolView renders a Tool for the CLI with the schema as JSON, not bytes.
type toolView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Sequential  bool            `json:"sequential,omitempty"`
	Effect      string          `json:"effect,omitempty"`
	Active      bool            `json:"active"`
	// ToolSet names the ToolSet of a tool that comes from one; Loaded says
	// whether the model has loaded it; Unavailable marks a ToolSet that did
	// not resolve (its entry has the ToolSet's name as id).
	ToolSet     string `json:"toolset,omitempty"`
	Loaded      bool   `json:"loaded,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

func viewOf(entry service.ToolEntry) toolView {
	view := toolView{ID: entry.Spec.ID, Name: entry.Spec.Name, Description: entry.Spec.Description, Sequential: entry.Spec.Sequential,
		Effect: string(entry.Spec.Effect), Active: entry.Active, ToolSet: entry.ToolSet, Loaded: entry.Loaded, Unavailable: entry.Unavailable}
	if json.Valid(entry.Spec.InputSchema) {
		view.InputSchema = json.RawMessage(entry.Spec.InputSchema)
	}
	return view
}

type toolListing struct {
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	// MaxEffect is the agent's Effect cap; tools above it are not listed.
	MaxEffect string     `json:"max_effect,omitempty"`
	Tools     []toolView `json:"tools"`
}

func (c *cli) cmdTools(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("tools", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("tools needs a subcommand: list, describe, active, activate, deactivate")
	}
	sub, rest := args[0], args[1:]
	fs := c.newFlagSet("tools " + sub)
	var flags modelFlags
	bindModel(fs, &flags)
	sessionID := fs.String("session", "", "session whose tool activation applies")
	positionals, err := parseInterspersed(fs, rest)
	if err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	agent, err := rt.resolveAgent(flags.model, c.getenv)
	if err != nil {
		return c.failErr(err)
	}
	// Runner.Tools resolves an empty agent to the session's stored agent, or
	// the first registered agent when there is no session. Mirror that here so
	// the listing reports the agent that actually selected the surface.
	if agent == "" && *sessionID != "" {
		s, err := session.Open(ctx, rt.store, *sessionID)
		if err != nil {
			return c.failErr(err)
		}
		agent, _ = s.Get(service.MetaAgent)
	}
	if agent == "" {
		if names := rt.runner.Agents(); len(names) > 0 {
			agent = names[0]
		}
	}
	entries, err := rt.runner.Tools(ctx, agent, *sessionID)
	if err != nil {
		return c.failErr(err)
	}
	spec, _ := rt.runner.Agent(agent)
	listing := toolListing{SessionID: *sessionID, Agent: agent, MaxEffect: string(spec.MaxEffect), Tools: []toolView{}}
	for _, entry := range entries {
		listing.Tools = append(listing.Tools, viewOf(entry))
	}
	switch sub {
	case "list", "ls":
		if c.machine() {
			return c.writeJSON(listing)
		}
		if listing.MaxEffect != "" {
			fmt.Fprintf(c.stdout, "max effect: %s\n", listing.MaxEffect)
		}
		for _, entry := range listing.Tools {
			state := "active"
			if !entry.Active {
				state = "inactive"
			} else if entry.Unavailable {
				state = "unavailable"
			}
			fmt.Fprintf(c.stdout, "%-12s %-8s %s\n", entry.ID, state, entry.Description)
		}
		return ExitOK
	case "active":
		active := toolListing{SessionID: *sessionID, Agent: agent, MaxEffect: listing.MaxEffect, Tools: []toolView{}}
		for _, view := range listing.Tools {
			if view.Active {
				active.Tools = append(active.Tools, view)
			}
		}
		if c.machine() {
			return c.writeJSON(active)
		}
		for _, view := range active.Tools {
			fmt.Fprintln(c.stdout, view.ID)
		}
		return ExitOK
	case "describe":
		if len(positionals) != 1 {
			return c.usageError("tools describe needs exactly one tool id")
		}
		for _, view := range listing.Tools {
			if view.ID == positionals[0] {
				if c.machine() {
					return c.writeJSON(view)
				}
				fmt.Fprintf(c.stdout, "%s\n%s\nactive: %v\nschema: %s\n", view.ID, view.Description, view.Active, string(view.InputSchema))
				return ExitOK
			}
		}
		return c.fail(ExitNotFound, "unknown tool "+positionals[0])
	case "activate", "deactivate":
		if len(positionals) != 1 {
			return c.usageError("tools " + sub + " needs exactly one tool id")
		}
		if *sessionID == "" {
			return c.usageError("tools " + sub + " needs --session ID: activation is session state")
		}
		entry, err := rt.runner.SetToolActive(ctx, *sessionID, positionals[0], sub == "activate")
		if err != nil {
			if gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return c.fail(ExitNotFound, err.Error())
			}
			return c.failErr(err)
		}
		return c.emit(map[string]any{"session_id": *sessionID, "tool": viewOf(entry)}, fmt.Sprintf("%s %sd for session %s", positionals[0], sub, *sessionID))
	default:
		return c.usageError("unknown tools subcommand " + sub)
	}
}
