package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/model"
)

// modelFile is the YAML file `-model gateway` reads: the few settings a bot needs
// to call one model over HTTP. A key it does not know is an error. Write ${NAME}
// in a value to read it from the environment, to keep an API key out of the file.
// gateway.example.yaml, next to this file, is the file to copy.
type modelFile struct {
	API        string            `yaml:"api"` // openai-chat-completions (the default), openai-responses or anthropic-messages
	Endpoint   string            `yaml:"endpoint"`
	APIKey     string            `yaml:"api_key"`
	Headers    map[string]string `yaml:"headers"`
	Model      string            `yaml:"model"`
	Window     gotato.Window     `yaml:"window"` // a number of tokens, or "unknown"
	MaxTokens  uint32            `yaml:"max_tokens"`
	MaxRetries *int              `yaml:"max_retries"` // unset keeps model.DefaultRetry; 0 turns retries off
}

// loadModel reads the file at path and returns the model it describes.
func loadModel(path string) (*model.Client, gotato.ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, gotato.ModelConfig{}, err
	}
	var in modelFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&in); err != nil {
		return nil, gotato.ModelConfig{}, err
	}
	var wire model.WireAPI
	switch strings.TrimSpace(in.API) {
	case "", "openai-chat-completions":
		wire = model.ChatCompletions{}
	case "openai-responses":
		wire = model.Responses{}
	case "anthropic-messages":
		wire = model.AnthropicMessages{}
	default:
		return nil, gotato.ModelConfig{}, fmt.Errorf("unsupported api %q (use openai-chat-completions, openai-responses or anthropic-messages)", in.API)
	}
	headers := map[string]string{}
	for name, value := range in.Headers {
		headers[name] = expandEnv(value)
	}
	conn := model.Connection{Endpoint: strings.TrimSpace(expandEnv(in.Endpoint)), APIKey: expandEnv(in.APIKey), Headers: headers}
	config := gotato.ModelConfig{
		Name:     in.Model,
		Facts:    gotato.ModelFacts{Window: in.Window},
		Sampling: gotato.Sampling{MaxTokens: in.MaxTokens},
	}
	var options []model.Option
	if in.MaxRetries != nil {
		retry := model.DefaultRetry
		retry.Max = *in.MaxRetries
		options = append(options, model.WithRetry(retry))
	}
	client, err := model.New(wire, conn, config, options...)
	return client, config, err
}

var envRef = regexp.MustCompile(`\$\{([^}]*)\}`)

// expandEnv replaces ${NAME} with the environment variable NAME. A bare $ stays.
func expandEnv(value string) string {
	return envRef.ReplaceAllStringFunc(value, func(ref string) string { return os.Getenv(ref[2 : len(ref)-1]) })
}
