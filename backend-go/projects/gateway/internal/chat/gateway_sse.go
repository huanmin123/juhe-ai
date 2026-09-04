package chat

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// OpenAI Chat Completions SSE collection ported from chat-gateway-sse.ts with
// identical event framing, budgets and Chinese error strings.

// ChatToolCall mirrors ChatToolCall (tools/contracts.ts).
type ChatToolCall struct {
	CallID        string `json:"callId"`
	ToolName      string `json:"toolName"`
	ArgumentsJSON string `json:"argumentsJson"`
	SourceOrder   int64  `json:"sourceOrder"`
}

// OpenAIChatSseResult mirrors OpenAIChatSseResult.
type OpenAIChatSseResult struct {
	Content           string
	FinishReason      string
	Done              bool
	InputTokens       *int64
	OutputTokens      *int64
	ToolCalls         []ChatToolCall
	ContinuationItems []any
}

const (
	sseMaxEventBytes    = 64 * 1024
	defaultMaxSSEEvents = 65536
)

// CollectOpenAIChatSse mirrors collectOpenAIChatSse over a byte stream.
func CollectOpenAIChatSse(stream io.Reader, maxContentBytes int, onDelta func(delta string), maxEvents int) (OpenAIChatSseResult, error) {
	result := OpenAIChatSseResult{ToolCalls: []ChatToolCall{}, ContinuationItems: []any{}}
	if maxEvents <= 0 {
		maxEvents = defaultMaxSSEEvents
	}
	raw, err := io.ReadAll(stream)
	if err != nil {
		return result, err
	}
	if !utf8.Valid(raw) {
		return result, errors.New("上游返回了无效的 SSE JSON")
	}
	buffer := string(raw)
	var (
		content      strings.Builder
		finishReason string
		done         bool
		eventCount   int
		toolParts    = map[int64]*chatToolCallPart{}
	)
	consumeEvent := func(eventText string) error {
		dataLines := []string{}
		for _, line := range splitSSELines(eventText) {
			if strings.HasPrefix(line, "data:") {
				value := line[5:]
				value = strings.TrimPrefix(value, " ")
				dataLines = append(dataLines, value)
			}
		}
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		if data == "[DONE]" {
			done = true
			return nil
		}
		var payload struct {
			Choices []struct {
				Delta struct {
					Content   any `json:"content"`
					ToolCalls []struct {
						Index    any `json:"index"`
						ID       any `json:"id"`
						Function *struct {
							Name      any `json:"name"`
							Arguments any `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason any `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     any `json:"prompt_tokens"`
				CompletionTokens any `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message any `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return errors.New("上游返回了无效的 SSE JSON")
		}
		if payload.Error != nil {
			if message, ok := payload.Error.Message.(string); ok && message != "" {
				return errors.New(message)
			}
			return errors.New("上游流式请求失败")
		}
		if payload.Usage != nil {
			if value := nonNegativeInteger(payload.Usage.PromptTokens); value != nil {
				result.InputTokens = value
			}
			if value := nonNegativeInteger(payload.Usage.CompletionTokens); value != nil {
				result.OutputTokens = value
			}
		}
		if len(payload.Choices) > 0 {
			choice := payload.Choices[0]
			if delta, ok := choice.Delta.Content.(string); ok && delta != "" {
				nextBytes := content.Len() + len(delta)
				if nextBytes > maxContentBytes {
					return errors.New("回答内容超过 192 KiB 上限")
				}
				content.WriteString(delta)
				if onDelta != nil {
					onDelta(delta)
				}
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				index := nonNegativeInteger(toolCall.Index)
				if index == nil || *index > 255 {
					return errors.New("Chat 工具调用 index 无效")
				}
				part, ok := toolParts[*index]
				if !ok {
					part = &chatToolCallPart{index: *index}
					toolParts[*index] = part
				}
				if id, ok := toolCall.ID.(string); ok {
					part.id = mergeStableToolField(part.id, id)
				}
				if toolCall.Function != nil {
					if name, ok := toolCall.Function.Name.(string); ok {
						part.name = mergeStableToolField(part.name, name)
					}
					if args, ok := toolCall.Function.Arguments.(string); ok {
						part.arguments += args
						if len(part.arguments) > 64*1024 {
							return errors.New("Chat 单个工具参数超过 64 KiB 上限")
						}
					}
				}
			}
			if reason, ok := choice.FinishReason.(string); ok && reason != "" {
				finishReason = reason
			}
		}
		return nil
	}
	for {
		boundary := findEventBoundary(buffer)
		if boundary == nil {
			break
		}
		eventText := buffer[:boundary.index]
		eventCount++
		if eventCount > maxEvents {
			return result, errors.New("上游 Chat Completions 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		if len(eventText) > sseMaxEventBytes {
			return result, errors.New("上游 Chat Completions 单个事件超过 64 KiB 上限")
		}
		if err := consumeEvent(eventText); err != nil {
			return result, err
		}
		buffer = buffer[boundary.index+boundary.length:]
	}
	if len(buffer) > sseMaxEventBytes {
		return result, errors.New("上游 Chat Completions 单个事件超过 64 KiB 上限")
	}
	if strings.TrimSpace(buffer) != "" {
		eventCount++
		if eventCount > maxEvents {
			return result, errors.New("上游 Chat Completions 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		if err := consumeEvent(buffer); err != nil {
			return result, err
		}
	}
	if !done {
		return result, errors.New("上游流式响应缺少 [DONE]")
	}
	indices := make([]int64, 0, len(toolParts))
	for index := range toolParts {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	toolCalls := make([]ChatToolCall, 0, len(indices))
	for _, index := range indices {
		part := toolParts[index]
		if part.id == "" || part.name == "" || part.arguments == "" {
			return result, errors.New("Chat 工具调用缺少 id、name 或 arguments")
		}
		toolCalls = append(toolCalls, ChatToolCall{CallID: part.id, ToolName: part.name, ArgumentsJSON: part.arguments, SourceOrder: part.index})
	}
	result.Content = content.String()
	result.FinishReason = finishReason
	result.Done = done
	result.ToolCalls = toolCalls
	if len(toolCalls) > 0 {
		toolCallsPayload := make([]any, 0, len(toolCalls))
		for _, toolCall := range toolCalls {
			toolCallsPayload = append(toolCallsPayload, map[string]any{
				"id":   toolCall.CallID,
				"type": "function",
				"function": map[string]any{
					"name":      toolCall.ToolName,
					"arguments": toolCall.ArgumentsJSON,
				},
			})
		}
		assistantItem := map[string]any{"role": "assistant", "tool_calls": toolCallsPayload}
		if result.Content != "" {
			assistantItem["content"] = result.Content
		} else {
			assistantItem["content"] = nil
		}
		result.ContinuationItems = append(result.ContinuationItems, assistantItem)
	}
	return result, nil
}

type chatToolCallPart struct {
	index     int64
	id        string
	name      string
	arguments string
}

func splitSSELines(value string) []string {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func nonNegativeInteger(value any) *int64 {
	number, ok := numericValue(value)
	if !ok || number < 0 || number != truncF(number) || number > 9007199254740991 {
		return nil
	}
	parsed := int64(number)
	return &parsed
}

type eventBoundary struct {
	index  int
	length int
}

func findEventBoundary(value string) *eventBoundary {
	lf := strings.Index(value, "\n\n")
	crlf := strings.Index(value, "\r\n\r\n")
	if lf < 0 && crlf < 0 {
		return nil
	}
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return &eventBoundary{index: crlf, length: 4}
	}
	return &eventBoundary{index: lf, length: 2}
}

func mergeStableToolField(current, chunk string) string {
	if chunk == "" || chunk == current {
		return current
	}
	if current == "" {
		return chunk
	}
	if strings.HasSuffix(current, chunk) {
		return current
	}
	return current + chunk
}
