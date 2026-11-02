package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/store"
)

// agentKey is the Session metadata key that names a Session's Agent, which the
// Runner owns and a client only reads.
const agentKey = service.ReservedPrefix + "agent"

// A run with --instruction or --compact-ceiling uses another AgentProfile: the
// one it would use, with those two values replaced. The variant is a document
// in the CLI's definitions store, named "<base>-<digest of the document>", and
// registered with that store, so a Session created with it is resumed by a
// later invocation (registerStoredVariants).

// variantAsked reports whether the flags ask for a variant.
func (f modelFlags) variantAsked() bool { return f.instruction != "" || f.compactCeiling > 0 }

// registerStoredVariants registers the AgentProfile of every name that Sessions
// in the store use and the definitions store holds, so each runs again with the
// instruction and the ceiling it was made with.
func (rt *cliRuntime) registerStoredVariants(ctx context.Context) error {
	infos, err := listAll(ctx, rt.store)
	if err != nil {
		return err
	}
	have := rt.profiles.AgentProfiles()
	for _, info := range infos {
		name := info.Metadata[agentKey]
		if name == "" || slices.Contains(have, name) {
			continue
		}
		if _, err := rt.defs.LoadDefinition(ctx, profile.KindAgentProfile, name); errors.Is(err, record.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if err := rt.profiles.RegisterAgentProfileWithStore(name, rt.defs); err != nil {
			return err
		}
		have = append(have, name)
	}
	return nil
}

// agentFor is the Agent a run or a new Session of the flags uses: resolveAgent's
// answer when the flags set no instruction and no ceiling, and otherwise a
// variant of the Agent the flags name, or else of the Agent of the Session, or
// else of the default. The variant starts from the one the Session's Agent
// already is, so a flag changes only the value it names.
func (rt *cliRuntime) agentFor(ctx context.Context, flags modelFlags, env func(string) string, sessionID string) (string, error) {
	name, err := rt.resolveAgent(flags.model, env)
	if err != nil || !flags.variantAsked() {
		return name, err
	}
	if name == "" && sessionID != "" {
		s, err := store.Open(ctx, rt.store.Records, sessionID)
		if err != nil {
			return "", err
		}
		name, _ = s.Get(agentKey)
	}
	// An empty name is the default Agent.
	p, err := rt.runner.AgentProfile(ctx, name)
	if err != nil {
		return "", err
	}
	// A variant's name is its base and a digest: keep the base.
	base, _, _ := strings.Cut(p.Name, "-")
	p.Name = ""
	if flags.instruction != "" {
		p.Instruction = flags.instruction
	}
	if flags.compactCeiling > 0 {
		p.Compact.CeilingTokens = flags.compactCeiling
	}
	doc, err := profile.EncodeAgentProfile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(doc)
	name = base + "-" + hex.EncodeToString(sum[:4])
	if err := rt.defs.SaveDefinition(ctx, profile.KindAgentProfile, name, doc); err != nil {
		return "", err
	}
	return name, rt.profiles.RegisterAgentProfileWithStore(name, rt.defs)
}
