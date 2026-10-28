package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/jinhuang712/gotato/gotato"
)

type responsesStream struct {
	sseBody
	nameMap  map[string]string
	calls    map[int]*responsesCall
	texts    map[int]bool
	queue    []gotato.ModelEvent
	model    string
	finished bool
	terminal error
	restart  restarter
	overflow overflowMatcher
}

type responsesCall struct {
	id        string
	callID    string
	name      string
	arguments string
	// argumentsDone records that the arguments arrived whole, through
	// response.function_call_arguments.done or response.output_item.done.
	argumentsDone bool
	emitted       bool
}

type responsesOutputItem struct {
	Type             string             `json:"type"`
	ID               string             `json:"id"`
	CallID           string             `json:"call_id"`
	Name             string             `json:"name"`
	Arguments        responsesArguments `json:"arguments"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

type responsesResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Model             string `json:"model,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage *struct {
		InputTokens        uint64 `json:"input_tokens"`
		OutputTokens       uint64 `json:"output_tokens"`
		TotalTokens        uint64 `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens     uint64 `json:"cached_tokens"`
			CacheWriteTokens uint64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
	Output []responsesOutputItem `json:"output,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type responsesEvent struct {
	Type        string               `json:"type"`
	OutputIndex int                  `json:"output_index"`
	Delta       string               `json:"delta"`
	Arguments   string               `json:"arguments"`
	Item        *responsesOutputItem `json:"item,omitempty"`
	Response    *responsesResponse   `json:"response,omitempty"`
	Code        string               `json:"code,omitempty"`
	Message     string               `json:"message,omitempty"`
}

func (s *responsesStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			if s.terminal != nil {
				err := s.terminal
				s.terminal = nil
				return gotato.ModelEvent{}, err
			}
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := readSSEEvent(ctx, s.reader)
		if err != nil {
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				var gerr *Error
				if errors.Is(err, io.EOF) && errors.As(rerr, &gerr) {
					gerr.Message = "Responses stream ended before completion (" + gerr.Message + ")"
				}
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		var event responsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid Responses SSE event: %w", err)
		}
		s.processResponsesEvent(event, []byte(data))
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// reopen continues on a re-issued response, discarding everything decoded
// from the interrupted one (nothing of it reached the caller).
func (s *responsesStream) reopen(response *http.Response) {
	s.replace(response)
	s.calls = make(map[int]*responsesCall)
	s.texts = make(map[int]bool)
	s.queue = nil
}

func (s *responsesStream) processResponsesEvent(event responsesEvent, raw []byte) {
	switch event.Type {
	case "response.created", "response.in_progress":
		if event.Response != nil && event.Response.Model != "" {
			s.model = event.Response.Model
		}
	case "response.output_item.added":
		if event.Item == nil {
			return
		}
		s.ensureResponsesCall(event.OutputIndex, event.Item)
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta != "" {
			s.texts[event.OutputIndex] = true
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: event.Delta})
		}
	case "response.function_call_arguments.delta":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &responsesCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments += event.Delta
	case "response.function_call_arguments.done":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &responsesCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments = event.Arguments
		call.argumentsDone = true
	case "response.output_item.done":
		if event.Item == nil {
			return
		}
		itemRaw := responsesItemRaw(raw)
		s.finishResponsesItem(event.OutputIndex, *event.Item, itemRaw)
	case "response.completed", "response.incomplete":
		s.finishResponsesResponse(event.Response)
	case "response.failed":
		message := event.Message
		if event.Response != nil && event.Response.Error != nil {
			message = event.Response.Error.Message
			if message == "" {
				message = event.Response.Error.Code
			}
		}
		if message == "" {
			message = "Responses response failed"
		}
		code := ""
		if event.Response != nil && event.Response.Error != nil {
			code = event.Response.Error.Code
		}
		s.finishWithError(s.overflow.streamError("Responses response failed", message, code))
	case "error":
		message := event.Message
		if message == "" {
			message = event.Code
		}
		if message == "" {
			message = "Responses stream error"
		}
		s.finishWithError(s.overflow.streamError("Responses stream error", message, event.Code))
	}
}

func responsesItemRaw(event []byte) []byte {
	var envelope struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil || len(envelope.Item) == 0 {
		return nil
	}
	return append([]byte(nil), envelope.Item...)
}

func (s *responsesStream) ensureResponsesCall(index int, item *responsesOutputItem) {
	if item.Type != "function_call" && item.Type != "tool_search_call" {
		return
	}
	call := s.calls[index]
	if call == nil {
		call = &responsesCall{}
		s.calls[index] = call
	}
	if item.ID != "" {
		call.id = item.ID
	}
	if item.CallID != "" {
		call.callID = item.CallID
	}
	if item.Name != "" {
		call.name = item.Name
	}
	if item.Type == "tool_search_call" {
		// The Model's search is a call to our search tool.
		call.name = s.searchToolName()
	}
	if item.Arguments != "" {
		call.arguments = string(item.Arguments)
	}
}

func (s *responsesStream) finishResponsesItem(index int, item responsesOutputItem, raw []byte) {
	if item.Type == "reasoning" {
		artifact, err := json.Marshal(item)
		if err == nil && len(raw) > 0 {
			artifact = append([]byte(nil), raw...)
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: artifact})
		return
	}
	if item.Type == "message" && !s.texts[index] {
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: content.Text})
			}
		}
		return
	}
	if item.Type != "function_call" && item.Type != "tool_search_call" {
		return
	}
	s.ensureResponsesCall(index, &item)
	if call := s.calls[index]; call != nil {
		call.argumentsDone = true
	}
	s.emitResponsesCall(index)
}

// searchToolName is the provider name the search tool was sent under.
func (s *responsesStream) searchToolName() string {
	for name, id := range s.nameMap {
		if isSearchToolID(id) {
			return name
		}
	}
	return gotato.SearchToolName
}

// emitResponsesCall queues a call's ToolCall event. It returns false when the
// call cannot be delivered because it has no name or no call_id yet.
func (s *responsesStream) emitResponsesCall(index int) bool {
	call := s.calls[index]
	if call == nil || call.emitted {
		return true
	}
	name := call.name
	if original, ok := s.nameMap[name]; ok {
		name = original
	}
	// Validate before marking emitted: a call that cannot be delivered must
	// not make finishResponsesResponse report StopReasonToolCalls.
	if name == "" || call.callID == "" {
		return false
	}
	call.emitted = true
	arguments := call.arguments
	if arguments == "" {
		arguments = "{}"
	}
	// The call ID is the one the Session and its Tool Result use. The
	// provider's item ID is adapter-owned, so it travels as the Signature.
	toolCall := &gotato.ToolCall{ID: gotato.ToolCallID(call.callID), ToolID: name, Arguments: []byte(arguments)}
	if call.id != "" {
		toolCall.Signature = []byte(call.id)
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: toolCall})
	return true
}

func (s *responsesStream) finishResponsesResponse(response *responsesResponse) {
	if response == nil {
		s.finishWithError(fmt.Errorf("gateway: Responses completion has no response"))
		return
	}
	stop, ok := responsesStopReason(response.Status, response.IncompleteDetails)
	if !ok {
		s.finishWithError(protocolFailure("Responses response ended with the unknown status %q", response.Status))
		return
	}
	if response.Model != "" {
		s.model = response.Model
	}
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		// A call that the output limit cut off has incomplete arguments, so
		// it is not delivered.
		if stop == gotato.StopReasonMaxTokens && !s.calls[index].argumentsDone {
			continue
		}
		if !s.emitResponsesCall(index) {
			// The model asked for a tool the stream cannot name. Ending
			// the Turn as a final answer would hide that.
			s.finishWithError(protocolFailure("Responses function call at output index %d has no name or call_id", index))
			return
		}
	}
	if response.Usage != nil {
		usage := gotato.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
		if details := response.Usage.InputTokensDetails; details != nil {
			usage.CacheReadTokens = details.CachedTokens
			usage.CacheWriteTokens = details.CacheWriteTokens
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindUsage, Usage: usage})
	}
	// Delivered calls make the stop StopReasonToolCalls, unless the output limit
	// cut the response off: StopReasonMaxTokens stays, so the agent learns it.
	for _, call := range s.calls {
		if call.emitted && stop != gotato.StopReasonMaxTokens {
			stop = gotato.StopReasonToolCalls
			break
		}
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: stop, Model: s.model})
	s.finished = true
}

// responsesStopReason maps a response's status; ok is false for one it does
// not know, which fails the stream.
func responsesStopReason(status string, details *struct {
	Reason string `json:"reason"`
}) (stop gotato.StopReason, ok bool) {
	switch status {
	case "incomplete":
		if details != nil && details.Reason == "max_output_tokens" {
			return gotato.StopReasonMaxTokens, true
		}
		return gotato.StopReasonError, true
	case "failed", "canceled", "cancelled":
		return gotato.StopReasonError, true
	case "", "completed":
		return gotato.StopReasonEndTurn, true
	}
	return "", false
}

func (s *responsesStream) finishWithError(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
}
