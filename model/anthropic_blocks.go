package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

// anthropicUserBlocks encodes the text and image parts of a user turn.
func anthropicUserBlocks(message gotato.Message) []anthropicBlock {
	var blocks []anthropicBlock
	for _, part := range message.Parts {
		switch part.Kind {
		case gotato.ContentKindText, gotato.ContentKindJSON:
			if part.Text != "" {
				blocks = append(blocks, anthropicBlock{"type": "text", "text": part.Text})
			}
		case gotato.ContentKindImage:
			if len(part.Data) == 0 {
				continue
			}
			blocks = append(blocks, anthropicBlock{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": part.MIMEType, "data": base64.StdEncoding.EncodeToString(part.Data),
			}})
		}
	}
	return blocks
}

// anthropicToolResultBlock encodes a Tool Result as one tool_result block.
func anthropicToolResultBlock(message gotato.Message) anthropicBlock {
	content := toolResultOutput(message)
	callID := ""
	failed := false
	if message.ToolResult != nil {
		callID = string(message.ToolResult.CallID)
		failed = message.ToolResult.Status != "" && message.ToolResult.Status != gotato.ToolResultStatusOK
	}
	block := anthropicBlock{"type": "tool_result", "tool_use_id": callID, "content": content}
	if failed {
		block["is_error"] = true
	}
	return block
}

// appendAnthropicToolUses appends a tool_use block for each Tool Call.
func appendAnthropicToolUses(blocks []anthropicBlock, calls []gotato.ToolCall, names map[string]string) ([]anthropicBlock, error) {
	for _, call := range calls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, anthropicBlock{"type": "tool_use", "id": string(call.ID), "name": name, "input": json.RawMessage(replayArguments(call.Arguments))})
	}
	return blocks, nil
}

// appendAnthropicAssistantPart appends the block of one assistant part, if the wire keeps it.
func appendAnthropicAssistantPart(blocks []anthropicBlock, part gotato.ContentPart) ([]anthropicBlock, error) {
	switch part.Kind {
	case gotato.ContentKindReasoning:
		// Only a signed provider block can be replayed; plain
		// reasoning text stays in the transcript.
		if len(part.Signature) == 0 {
			return blocks, nil
		}
		block, err := anthropicReasoningBlock(part.Signature)
		if err != nil {
			return nil, err
		}
		return append(blocks, block), nil
	case gotato.ContentKindText:
		if part.Text != "" {
			return append(blocks, anthropicBlock{"type": "text", "text": part.Text}), nil
		}
	case gotato.ContentKindImage, gotato.ContentKindJSON:
		if len(part.Data) > 0 || part.Text != "" {
			return nil, fmt.Errorf("model: assistant content kind %q is unsupported", part.Kind)
		}
	}
	return blocks, nil
}
