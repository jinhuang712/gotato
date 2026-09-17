package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"github.com/jinhuang712/gotato/toolregistry"
)

// Session metadata keys the CLI owns. They are ordinary application metadata
// from the runtime's point of view.
const (
	metaContextStrategy = "gotato.context"
	metaInstruction     = "gotato.instruction"
	metaModel           = "gotato.model"
	metaToolPrefix      = "gotato.tool."
)

// modelFlags are the model-selection flags shared by run and compact.
type modelFlags struct {
	model         string
	gatewayConfig string
	instruction   string
}

func (c *cli) bindModel(fs interface {
	StringVar(*string, string, string, string)
}, flags *modelFlags) {
	fs.StringVar(&flags.model, "model", "", "model: echo (default), demo, or gateway")
	fs.StringVar(&flags.gatewayConfig, "gateway-config", "", "YAML config for --model gateway (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	fs.StringVar(&flags.instruction, "instruction", "", "system instruction for the agent")
}

// buildModel resolves the Model. The chosen name is returned so it can be
// recorded in the Session.
func (c *cli) buildModel(flags modelFlags, s *session.Session) (gotato.Model, string, error) {
	name := flags.model
	if name == "" && s != nil {
		name, _ = s.Get(metaModel)
	}
	if name == "" {
		name = c.getenv("GOTATO_MODEL")
	}
	if name == "" {
		name = "echo"
	}
	switch name {
	case "echo":
		return testkit.EchoModel{}, name, nil
	case "demo":
		return testkit.DemoModel{}, name, nil
	case "gateway":
		path := c.gatewayConfigPath(flags)
		config, err := gateway.LoadYAML(path)
		if err != nil {
			return nil, name, fmt.Errorf("gateway config %s: %w", path, err)
		}
		client, err := gateway.New(config)
		if err != nil {
			return nil, name, err
		}
		return client, name, nil
	default:
		return nil, name, gotato.ErrorOf(gotato.ErrInvalidArgument, "unknown model "+name+" (use echo, demo, or gateway)")
	}
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

// builtinRegistry is the Tool surface the CLI offers. Tools are optional
// capabilities; a Session may deactivate any of them.
func builtinRegistry() *toolregistry.Registry {
	now, err := gotato.NewFuncTool("time.now", "Returns the current time in RFC 3339 format.", func(context.Context, struct{}) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		panic(err)
	}
	return toolregistry.New(testkit.DemoEchoTool(), now)
}

// registryFor applies a Session's tool activation metadata to the builtin
// registry.
func registryFor(s *session.Session) *toolregistry.Registry {
	reg := builtinRegistry()
	if s == nil {
		return reg
	}
	for key, value := range s.Metadata() {
		if !strings.HasPrefix(key, metaToolPrefix) {
			continue
		}
		id := strings.TrimPrefix(key, metaToolPrefix)
		if value == "inactive" {
			_ = reg.Deactivate(id)
		}
	}
	return reg
}

// contextBuilderFor resolves the strategy: explicit spec, then Session
// metadata, then full history.
func contextBuilderFor(spec string, s *session.Session, summarizer modelctx.Summarizer) (gotato.ContextBuilder, string, error) {
	if spec == "" && s != nil {
		spec, _ = s.Get(metaContextStrategy)
	}
	if spec == "" {
		spec = "full"
	}
	builder, err := modelctx.Parse(spec, summarizer)
	return builder, spec, err
}

// runOutcome is the JSON shape of a completed run command.
type runOutcome struct {
	SessionID    string               `json:"session_id"`
	RunID        gotato.RunID         `json:"run_id"`
	Status       gotato.RunStatus     `json:"status"`
	Model        string               `json:"model"`
	Context      string               `json:"context"`
	FinalText    string               `json:"final_text,omitempty"`
	FinalMessage *gotato.Message      `json:"final_message,omitempty"`
	Usage        gotato.Usage         `json:"usage"`
	Metrics      gotato.RunMetrics    `json:"metrics"`
	Error        *gotato.RuntimeError `json:"error,omitempty"`
	Messages     int                  `json:"messages"`
	Events       int                  `json:"events"`
}

// runOptions configures executeRun.
type runOptions struct {
	prompt      string
	continueRun bool
	contextSpec string
	model       modelFlags
	eventSink   func(gotato.Event) error
}

// executeRun composes the runtime exactly as an application would: a Session
// as Transcript, a Recorder, a ContextBuilder, a Tool Registry, one Agent.
func (c *cli) executeRun(ctx context.Context, s *session.Session, opts runOptions) (runOutcome, error) {
	model, modelName, err := c.buildModel(opts.model, s)
	if err != nil {
		return runOutcome{}, err
	}
	builder, spec, err := contextBuilderFor(opts.contextSpec, s, nil)
	if err != nil {
		return runOutcome{}, err
	}
	instruction := opts.model.instruction
	if instruction == "" {
		instruction, _ = s.Get(metaInstruction)
	}
	if instruction == "" {
		instruction = "You are a helpful assistant."
	}
	s.Set(metaModel, modelName)
	s.Set(metaContextStrategy, spec)
	s.Set(metaInstruction, instruction)

	extensions := []any{session.Record(s)}
	if opts.eventSink != nil {
		extensions = append(extensions, sinkObserver{fn: opts.eventSink})
	}
	agent, err := gotato.NewAgent(
		gotato.WithModel(model),
		gotato.WithInstruction(instruction),
		gotato.WithTranscript(s),
		gotato.WithContextBuilder(builder),
		gotato.WithToolSource(registryFor(s)),
		gotato.WithExtensions(extensions...),
	)
	if err != nil {
		return runOutcome{}, err
	}
	defer agent.Close(context.Background())

	var result gotato.RunResult
	if opts.continueRun {
		controllable, ok := agent.(gotato.ControllableAgent)
		if !ok {
			return runOutcome{}, gotato.ErrorOf(gotato.ErrNotSupported, "agent does not support continue")
		}
		result, err = controllable.Continue(ctx)
	} else {
		result, err = agent.Prompt(ctx, gotato.UserMessage(opts.prompt))
	}
	outcome := runOutcome{
		SessionID: s.ID(),
		RunID:     result.RunID,
		Status:    result.Status,
		Model:     modelName,
		Context:   spec,
		Usage:     result.Usage,
		Metrics:   result.Metrics,
		Error:     result.Error,
		Messages:  s.Len(),
		Events:    len(s.Events()),
	}
	if result.FinalMessage != nil {
		outcome.FinalMessage = result.FinalMessage
		outcome.FinalText = gotato.TextOf(*result.FinalMessage)
	}
	if err != nil && outcome.Error == nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(err, &runtimeErr) {
			outcome.Error = runtimeErr
		} else {
			outcome.Error = gotato.ErrorOf(gotato.ErrInternalInvariant, err.Error())
		}
		if outcome.Status == "" {
			outcome.Status = gotato.RunFailed
		}
	}
	return outcome, nil
}

type sinkObserver struct{ fn func(gotato.Event) error }

func (o sinkObserver) Observe(_ context.Context, event gotato.Event) error { return o.fn(event) }
func (o sinkObserver) Advisory() bool                                      { return true }

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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
