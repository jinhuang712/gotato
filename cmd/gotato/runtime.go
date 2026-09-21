package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// modelFlags select the agent (model) and gateway configuration.
type modelFlags struct {
	model         string
	gatewayConfig string
	instruction   string
}

func bindModel(fs *flag.FlagSet, flags *modelFlags) {
	fs.StringVar(&flags.model, "model", "", "agent/model: echo (default), demo, or gateway")
	fs.StringVar(&flags.gatewayConfig, "gateway-config", "", "YAML config for --model gateway (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	fs.StringVar(&flags.instruction, "instruction", "", "system instruction stored in the session")
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

// cliRuntime is the CLI's service.Runner plus the diagnostics gathered while
// building it. The CLI, `gotato serve`, and gotato-grpc all run the same
// Runner; the CLI simply drives it in-process.
type cliRuntime struct {
	runner     *service.Runner
	store      session.Store
	storeDir   string
	gatewayErr error
	gatewayCfg string
}

const defaultInstruction = "You are a helpful assistant."

// testAgentSpecs lets in-package tests register deterministic AgentSpecs
// without changing the production echo/demo/gateway surface. It is empty in
// the built binary.
var testAgentSpecs []service.AgentSpec

// newRuntime builds the Runner: echo and demo are always registered; gateway
// is registered when its YAML loads.
func (c *cli) newRuntime(flags modelFlags) (*cliRuntime, error) {
	store, dir, err := c.store()
	if err != nil {
		return nil, err
	}
	tools := builtinTools()
	specs := []service.AgentSpec{
		{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Instruction: defaultInstruction, Tools: tools},
		{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Instruction: defaultInstruction, Tools: tools},
	}
	specs = append(specs, testAgentSpecs...)
	rt := &cliRuntime{store: store, storeDir: dir, gatewayCfg: c.gatewayConfigPath(flags)}
	if config, err := gateway.LoadYAML(rt.gatewayCfg); err != nil {
		rt.gatewayErr = err
	} else if client, err := gateway.New(config); err != nil {
		rt.gatewayErr = err
	} else {
		specs = append(specs, service.AgentSpec{Name: "gateway", Model: client, ModelName: config.Model, Instruction: defaultInstruction, Tools: tools})
	}
	runner, err := service.New(service.Config{Store: store, Specs: specs})
	if err != nil {
		return nil, err
	}
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
	if _, ok := rt.runner.Spec(name); ok {
		return name, nil
	}
	if name == "gateway" && rt.gatewayErr != nil {
		return "", fmt.Errorf("gateway config %s: %w", rt.gatewayCfg, rt.gatewayErr)
	}
	return "", gotato.ErrorOf(gotato.ErrInvalidArgument, "unknown model "+name+" (use echo, demo, or gateway)")
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
}

func outcomeOf(result service.RunResult) runOutcome {
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
	}
}

// sessionSettings are the per-session settings the CLI writes as metadata;
// the service honors them on every run.
type sessionSettings struct {
	instruction    string
	panel          string
	compactCeiling int
}

func (s sessionSettings) apply(set func(key, value string)) error {
	if s.instruction != "" {
		set(service.MetaInstruction, s.instruction)
	}
	if s.panel != "" {
		if _, err := service.PanelFromSpec(s.panel, nil); err != nil {
			return err
		}
		set(service.MetaPanel, s.panel)
	}
	if s.compactCeiling > 0 {
		set(service.MetaCompactCeiling, strconv.Itoa(s.compactCeiling))
	}
	return nil
}

func (s sessionSettings) metadata() (map[string]string, error) {
	out := map[string]string{}
	err := s.apply(func(key, value string) { out[key] = value })
	return out, err
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
