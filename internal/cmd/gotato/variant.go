package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// agentKey is the Session metadata key that names a Session's Agent, which the
// Runner owns and a client only reads.
const agentKey = service.ReservedPrefix + "agent"

// variant is what --instruction and --compact-ceiling ask of a built-in Agent.
// An Agent has its instruction and its ceiling alone, and a Session has neither,
// so a run with either flag uses another Agent: the built-in one with those two
// values replaced. Its name carries the definition ("echo+<token>"), so a
// Session created with it can be resumed by a later invocation, which registers
// the variants its store names (see registerVariants).
type variant struct {
	Instruction string `json:"instruction,omitempty"`
	Ceiling     int    `json:"compact_ceiling,omitempty"`
}

func (v variant) zero() bool { return v.Instruction == "" && v.Ceiling <= 0 }

// over returns v with the values of o that o sets.
func (v variant) over(o variant) variant {
	if o.Instruction != "" {
		v.Instruction = o.Instruction
	}
	if o.Ceiling > 0 {
		v.Ceiling = o.Ceiling
	}
	return v
}

// name is the Agent name of the variant of base.
func (v variant) name(base string) string {
	data, _ := json.Marshal(v)
	return base + "+" + base64.RawURLEncoding.EncodeToString(data)
}

// parseVariant splits an Agent name made by variant.name.
func parseVariant(name string) (base string, v variant, ok bool) {
	base, token, found := strings.Cut(name, "+")
	if !found || base == "" {
		return "", variant{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(data, &v) != nil || v.zero() {
		return "", variant{}, false
	}
	return base, v, true
}

// variant is the variant the flags ask for.
func (f modelFlags) variant() variant {
	return variant{Instruction: f.instruction, Ceiling: f.compactCeiling}
}

// registerVariants appends to specs the variant of every built-in Agent that the
// flags ask for, and the variants that Sessions in the store already use, so
// each of them runs again with the instruction and the ceiling it was made with.
func registerVariants(ctx context.Context, specs []service.Agent, store session.Store, flags modelFlags) []service.Agent {
	names := map[string]bool{}
	if v := flags.variant(); !v.zero() {
		for _, spec := range specs {
			names[v.name(spec.Name)] = true
		}
	}
	if infos, err := listAll(ctx, store); err == nil {
		for _, info := range infos {
			if _, _, ok := parseVariant(info.Metadata[agentKey]); ok {
				names[info.Metadata[agentKey]] = true
			}
		}
	}
	base := specs
	var out []service.Agent
	for name := range names {
		baseName, v, _ := parseVariant(name)
		for _, spec := range base {
			if spec.Name != baseName {
				continue
			}
			spec.Name = name
			if v.Instruction != "" {
				spec.Instruction = v.Instruction
			}
			if v.Ceiling > 0 {
				spec.Compact.CeilingTokens = v.Ceiling
			}
			out = append(out, spec)
		}
	}
	// A map has no order, and the Agents are listed: keep them in name order.
	slices.SortFunc(out, func(a, b service.Agent) int { return strings.Compare(a.Name, b.Name) })
	return append(specs, out...)
}

// agentFor is the Agent a run or a new Session of the flags uses: resolveAgent's
// answer when the flags set no instruction and no ceiling, and otherwise the
// variant of the Agent the flags name, or else of the Agent of the Session, or
// else of the default. The variant starts from the one the Session's Agent
// already is, so a flag changes only the value it names.
func (rt *cliRuntime) agentFor(ctx context.Context, flags modelFlags, env func(string) string, sessionID string) (string, error) {
	name, err := rt.resolveAgent(flags.model, env)
	if err != nil || flags.variant().zero() {
		return name, err
	}
	var current variant
	if name == "" && sessionID != "" {
		s, err := session.Open(ctx, rt.store.Records, sessionID)
		if err != nil {
			return "", err
		}
		name, _ = s.Get(agentKey)
		if base, v, ok := parseVariant(name); ok {
			name, current = base, v
		}
	}
	if name == "" {
		if names := rt.runner.Agents(); len(names) > 0 {
			name = names[0]
		}
	}
	return current.over(flags.variant()).name(name), nil
}
