package modelctx

import (
	"encoding/json"

	"github.com/jinhuang712/gotato/gotato"
)

// requestOverhead estimates the tokens of the request that are not Messages:
// the instruction and the tool definitions.
func requestOverhead(request gotato.ModelRequest) int {
	tools, _ := json.Marshal(request.Tools)
	return (len(request.SystemInstructions) + len(tools) + 3) / 4
}

// EstimateTokens is the bytes/4 heuristic over the JSON encoding of the
// Messages a provider would receive. Provider-reported usage lives in the
// Session's Run records; this estimate exists for budgets and inspection.
func EstimateTokens(messages []gotato.Message) int {
	encoded, _ := json.Marshal(gotato.ForModel(messages))
	return (len(encoded) + 3) / 4
}
