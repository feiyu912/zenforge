package anthropic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

type blockState struct {
	typ       string
	id        string
	name      string
	text      strings.Builder
	arguments strings.Builder
}

type accumulator struct {
	blocks       map[int]*blockState
	usage        model.Usage
	finishReason string
}

func newAccumulator() *accumulator {
	return &accumulator{blocks: map[int]*blockState{}}
}

func parseStream(body io.Reader, events chan<- model.Event) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	acc := newAccumulator()
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("decode anthropic stream event: %w", err)
		}
		if event.Type == "error" {
			message := strings.TrimSpace(event.Error.Message)
			if message == "" {
				message = "unknown provider error"
			}
			if event.Error.Type != "" {
				return fmt.Errorf("anthropic provider error (%s): %s", event.Error.Type, message)
			}
			return fmt.Errorf("anthropic provider error: %s", message)
		}
		acc.apply(event, events)
		if event.Type == "message_stop" {
			message := acc.message()
			done := model.Event{Type: model.EventDone, Message: &message, Usage: acc.usage}
			if acc.finishReason != "" {
				done.Meta = map[string]any{"finish_reason": acc.finishReason}
			}
			events <- done
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read anthropic provider stream: %w", err)
	}
	return fmt.Errorf("anthropic provider stream: %w", io.ErrUnexpectedEOF)
}

func (a *accumulator) apply(event streamEvent, events chan<- model.Event) {
	switch event.Type {
	case "message_start":
		a.usage = usageToModel(event.Message.Usage)
	case "message_delta":
		if event.Delta.StopReason != "" {
			a.finishReason = event.Delta.StopReason
			if a.finishReason == "tool_use" {
				a.finishReason = "tool_calls"
			}
		}
		if event.Usage.OutputTokens != 0 {
			a.usage.CompletionTokens = event.Usage.OutputTokens
			a.usage.TotalTokens = a.usage.PromptTokens + a.usage.CompletionTokens
			events <- model.Event{Type: model.EventUsage, Usage: a.usage}
		}
	case "content_block_start":
		a.blocks[event.Index] = &blockState{
			typ:  event.ContentBlock.Type,
			id:   event.ContentBlock.ID,
			name: event.ContentBlock.Name,
		}
		if event.ContentBlock.Text != "" {
			a.blocks[event.Index].text.WriteString(event.ContentBlock.Text)
			events <- model.Event{Type: model.EventDelta, Delta: event.ContentBlock.Text}
		}
		if len(event.ContentBlock.Input) > 0 {
			data, err := json.Marshal(event.ContentBlock.Input)
			if err == nil {
				a.blocks[event.Index].arguments.Write(data)
			}
		}
	case "content_block_delta":
		block := a.block(event.Index)
		switch event.Delta.Type {
		case "text_delta":
			block.text.WriteString(event.Delta.Text)
			events <- model.Event{Type: model.EventDelta, Delta: event.Delta.Text}
		case "input_json_delta":
			block.arguments.WriteString(event.Delta.PartialJSON)
		}
	}
}

func (a *accumulator) block(index int) *blockState {
	block := a.blocks[index]
	if block == nil {
		block = &blockState{}
		a.blocks[index] = block
	}
	return block
}

func (a *accumulator) message() model.Message {
	var content strings.Builder
	var calls []model.ToolCallSpec
	indices := make([]int, 0, len(a.blocks))
	for index := range a.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, i := range indices {
		block := a.blocks[i]
		if block == nil {
			continue
		}
		switch block.typ {
		case "tool_use":
			calls = append(calls, model.ToolCallSpec{
				ID:        block.id,
				Name:      block.name,
				Arguments: json.RawMessage(block.arguments.String()),
			})
		default:
			content.WriteString(block.text.String())
		}
	}
	return model.Message{
		Role:      "assistant",
		Content:   content.String(),
		ToolCalls: calls,
	}
}

func usageToModel(value usage) model.Usage {
	return model.Usage{
		PromptTokens:     value.InputTokens,
		CompletionTokens: value.OutputTokens,
		TotalTokens:      value.InputTokens + value.OutputTokens,
	}
}
