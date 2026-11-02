package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/cmd/gatewayconf"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// modelFlags select the agent (model) and gateway configuration.
type modelFlags struct {
	model         string
	gatewayConfig string
	instruction   string
	timeAway      string
	// compactCeiling is set by the commands that take --compact-ceiling.
	compactCeiling int
}

func bindModel(fs *flag.FlagSet, flags *modelFlags) {
	fs.StringVar(&flags.model, "model", "", "agent/model: echo (default), demo, or gateway")
	fs.StringVar(&flags.gatewayConfig, "gateway-config", "", "YAML config for --model gateway (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	fs.StringVar(&flags.instruction, "instruction", "", "system instruction: the run uses a variant of the agent with it (a session made with it keeps it)")
	fs.StringVar(&flags.timeAway, "time-away", "", "idle time after which a message is followed by a time_away notice: a duration such as 2h, or off (default 30m, or $GOTATO_TIME_AWAY)")
}

// timeAwayAfter reads --time-away: 0 is the default (30 minutes), negative
// turns the notice off.
func (c *cli) timeAwayAfter(flags modelFlags) (time.Duration, error) {
	value := flags.timeAway
	if value == "" {
		value = c.getenv("GOTATO_TIME_AWAY")
	}
	switch value {
	case "":
		return 0, nil
	case "off":
		return -1, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, gotato.ErrorOf(gotato.CodeInvalidArgument, "--time-away takes a positive duration such as 2h, or off")
	}
	return d, nil
}

func (c *cli) gatewayConfigPath(flags modelFlags) string {
	if flags.gatewayConfig != "" {
		return flags.gatewayConfig
	}
	if env := c.getenv("GOTATO_GATEWAY_CONFIG"); env != "" {
		return env
	}
	return "gateway.yaml"
}

// builtinTools is the Tool surface every CLI agent offers. Tools are optional
// capabilities; a Session may deactivate any of them.
func builtinTools() []gotato.Tool {
	now, err := gotato.NewFuncTool("time.now", "Returns the current time in RFC 3339 format.", func(context.Context, struct{}) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		panic(err)
	}
	return []gotato.Tool{testkit.DemoEchoTool(), now}
}

// cliRuntime is the CLI's host.Runner plus the diagnostics gathered while
// building it. The CLI and `gotato serve` run the same
// Runner; the CLI simply drives it in-process.
type cliRuntime struct {
	runner *host.Runner
	// config is what runner was built from; serve builds another with admission
	// settings.
	config host.Config
	// profiles and defs hold the AgentProfiles; defs keeps the variants that
	// --instruction and --compact-ceiling make (variant.go).
	profiles   *profile.Registry
	defs       store.Definitions
	store      store.Store
	storeDir   string
	gatewayErr error
	gatewayCfg string
}

const defaultInstruction = "You are a helpful assistant."

// defaultAgent is the AgentProfile a new Session runs when no flag names one.
const defaultAgent = "echo"

// testAgents lets in-package tests register deterministic AgentProfiles
// without changing the production echo/demo/gateway surface. It is empty in
// the built binary.
var testAgents []profile.AgentProfile

// newRuntime builds the Runner: echo and demo are always registered; gateway
// is registered when its YAML loads.
func (c *cli) newRuntime(flags modelFlags) (*cliRuntime, error) {
	st, dir, err := c.store()
	if err != nil {
		return nil, err
	}
	defs, err := store.NewFileDefinitions(filepath.Join(dir, "definitions"))
	if err != nil {
		return nil, err
	}
	tools := builtinTools()
	timeAway, err := c.timeAwayAfter(flags)
	if err != nil {
		return nil, err
	}
	agents := []profile.AgentProfile{
		{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Instruction: defaultInstruction, Tools: tools},
		{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Instruction: defaultInstruction, Tools: tools},
	}
	agents = append(agents, testAgents...)
	rt := &cliRuntime{store: st, defs: defs, storeDir: dir, gatewayCfg: c.gatewayConfigPath(flags)}
	if config, err := gatewayconf.Load(rt.gatewayCfg); err != nil {
		rt.gatewayErr = err
	} else if client, err := config.New(); err != nil {
		rt.gatewayErr = err
	} else {
		agents = append(agents, profile.AgentProfile{Name: "gateway", Model: client, ModelName: config.Model.Name, Instruction: defaultInstruction, Tools: tools})
	}
	// The Catalog holds what a stored variant of these AgentProfiles may name.
	models := map[string]gotato.Model{}
	var catalogTools []gotato.Tool
	seen := map[string]bool{}
	for _, agent := range agents {
		if agent.ModelName != "" {
			models[agent.ModelName] = agent.Model
		}
		for _, tool := range agent.Tools {
			if id := tool.Spec().ID; !seen[id] {
				seen[id] = true
				catalogTools = append(catalogTools, tool)
			}
		}
	}
	catalog, err := profile.NewCatalog(models, catalogTools, nil)
	if err != nil {
		return nil, err
	}
	rt.profiles = profile.NewRegistry(catalog)
	for _, agent := range agents {
		if err := rt.profiles.RegisterAgentProfileInline(agent); err != nil {
			return nil, err
		}
	}
	ctx, cancel := c.ctx()
	err = rt.registerStoredVariants(ctx)
	cancel()
	if err != nil {
		return nil, err
	}
	rt.config = host.Config{Store: st, Profiles: rt.profiles, DefaultAgent: defaultAgent, TimeAwayAfter: timeAway}
	runner, err := host.New(rt.config)
	if err != nil {
		return nil, err
	}
	c.runners = append(c.runners, runner)
	rt.runner = runner
	return rt, nil
}

// resolveAgent validates a --model value against the registered agents. An
// empty result means "the session's agent, or the default".
func (rt *cliRuntime) resolveAgent(name string, env func(string) string) (string, error) {
	if name == "" {
		name = env("GOTATO_MODEL")
	}
	if name == "" {
		return "", nil
	}
	if slices.Contains(rt.runner.AgentProfiles(), name) {
		return name, nil
	}
	if name == "gateway" && rt.gatewayErr != nil {
		return "", fmt.Errorf("gateway config %s: %w", rt.gatewayCfg, rt.gatewayErr)
	}
	return "", gotato.ErrorOf(gotato.CodeInvalidArgument, "unknown model "+name+" (use echo, demo, or gateway)")
}

// runOutcome is the JSON shape of the run command.
type runOutcome struct {
	SessionID    string               `json:"session_id"`
	RunID        gotato.RunID         `json:"run_id"`
	Status       gotato.RunStatus     `json:"status"`
	Agent        string               `json:"agent"`
	Model        string               `json:"model"`
	Compacted    bool                 `json:"compacted"`
	FinalText    string               `json:"final_text,omitempty"`
	FinalMessage *gotato.Message      `json:"final_message,omitempty"`
	Usage        gotato.Usage         `json:"usage"`
	Metrics      gotato.RunMetrics    `json:"metrics"`
	Error        *gotato.RuntimeError `json:"error,omitempty"`
	Messages     int                  `json:"messages"`
	Events       int                  `json:"events"`
	// RecordEpoch is the Session's Record epoch, for resuming its stream.
	RecordEpoch string `json:"record_epoch,omitempty"`
}

func outcomeOf(result host.RunResponse) runOutcome {
	return runOutcome{
		SessionID:    result.SessionID,
		RunID:        result.Result.RunID,
		Status:       result.Result.Status,
		Agent:        result.Agent,
		Model:        result.Model,
		Compacted:    result.Compacted,
		FinalText:    result.FinalText,
		FinalMessage: result.Result.FinalMessage,
		Usage:        result.Result.Usage,
		Metrics:      result.Result.Metrics,
		Error:        result.Result.Error,
		Messages:     result.Messages,
		Events:       result.Events,
		RecordEpoch:  result.RecordEpoch,
	}
}

// readPrompt returns the prompt from positionals or, when "-" is given, from
// stdin.
func (c *cli) readPrompt(positionals []string) (string, error) {
	if len(positionals) == 0 {
		return "", errors.New("a prompt is required (or - to read stdin)")
	}
	prompt := strings.Join(positionals, " ")
	if prompt == "-" {
		data, err := io.ReadAll(c.stdin)
		if err != nil {
			return "", err
		}
		prompt = string(data)
	}
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("prompt is empty")
	}
	return prompt, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
