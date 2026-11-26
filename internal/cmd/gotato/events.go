package main

import (
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
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
	st, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	events, err := store.StoredEvents(ctx, st.Records, *sessionID, 0, 0)
	if err != nil {
		return c.failErr(err)
	}
	return c.printEvents(eventsOfKind(events, *kind))
}

// eventsOfKind keeps the events of one kind, or all of them when kind is empty.
func eventsOfKind(events []gotato.Event, kind string) []gotato.Event {
	if kind == "" {
		return events
	}
	filtered := events[:0]
	for _, event := range events {
		if string(event.Kind()) == kind {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// printEvents writes the events as one JSON array with --json, and as JSON
// Lines otherwise.
func (c *cli) printEvents(events []gotato.Event) int {
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
