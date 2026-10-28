package main

import (
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func (c *cli) cmdEvents(args []string) int {
	fs := c.newFlagSet("events")
	sessionID := fs.String("session", "", "session id")
	kind := fs.String("kind", "", "only events of this kind")
	positionalSession := false
	if len(args) > 0 && args[0] == "--session-positional" {
		positionalSession = true
		args = args[1:]
	}
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if positionalSession && len(positionals) > 0 && *sessionID == "" {
		*sessionID = positionals[0]
	}
	if *sessionID == "" {
		return c.usageError("events needs --session ID")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	events, err := session.StoredEvents(ctx, store, *sessionID, 0, 0)
	if err != nil {
		return c.failErr(err)
	}
	if *kind != "" {
		filtered := events[:0]
		for _, event := range events {
			if string(event.Kind()) == *kind {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	if c.json {
		if events == nil {
			events = []gotato.Event{}
		}
		return c.writeJSON(events)
	}
	for _, event := range events {
		if err := c.writeJSONL(event); err != nil {
			return c.fail(ExitError, err.Error())
		}
	}
	return ExitOK
}
