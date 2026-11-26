package record

import (
	"encoding/json"

	"github.com/jinhuang712/gotato/gotato"
)

// requestOverhead estimates the tokens of the request that are not Messages:
// the instruction and the tool definitions.
func requestOverhead(request gotato.ModelRequest) int {
	tools, _ := json.Marshal(request.Tools)
	return tokensOf(len(request.SystemInstructions) + len(tools))
}

// EstimateTokens is the bytes/4 heuristic over the JSON encoding of the
// Messages a provider would receive. Provider-reported usage lives in the
// Session's Run records; this estimate exists for budgets and inspection.
func EstimateTokens(messages []gotato.Message) int {
	encoded, _ := json.Marshal(gotato.ForModel(messages))
	return tokensOf(len(encoded))
}

// bytesPerToken is the size of a token in the bytes/4 heuristic.
const bytesPerToken = 4

// tokensOf is the bytes/4 heuristic over n bytes, rounded up.
func tokensOf(n int) int {
	return (n + bytesPerToken - 1) / bytesPerToken
}
