package chat

import (
	"bytes"
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
	sseMaxEventBytes = 64 * 1024
)

// defaultMaxSSEEvents mirrors runtimeConfig.chat.upstreamSseMaxEvents
// (JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS, default 65536, range 2048..262144;
// runtime.ts:689). Resolved once at package init with the same fail-fast
// contract as Node's startup integerConfig.
var defaultMaxSSEEvents = chatEnvIntOrDefault("JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS", 65536, 2_048, 262_144)

// CollectOpenAIChatSse mirrors collectOpenAIChatSse over a byte stream.
// onDelta carries answer content; onReasoningDelta carries thinking-model
// reasoning_content deltas (streamed through without counting against
// maxContentBytes; the runner caps persisted reasoning separately).
func CollectOpenAIChatSse(stream io.Reader, maxContentBytes int, onDelta func(delta string), onReasoningDelta func(delta string), maxEvents int) (OpenAIChatSseResult, error) {
	result := OpenAIChatSseResult{ToolCalls: []ChatToolCall{}, ContinuationItems: []any{}}
	if maxEvents <= 0 {
		maxEvents = defaultMaxSSEEvents
	}
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
					Content          any `json:"content"`
					ReasoningContent any `json:"reasoning_content"`
					ToolCalls        []struct {
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
			if reasoning, ok := choice.Delta.ReasoningContent.(string); ok && reasoning != "" && onReasoningDelta != nil {
				onReasoningDelta(reasoning)
			}
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
	// 增量读取：每凑齐一个完整事件块立即消费并回调 onDelta/onReasoningDelta，
	// 保证下游在上游整段响应结束前收到事件（打字机/自动滚动），对齐 Node
	// for-await 语义；本函数只改“何时拿到数据”，不改解析出的内容。
	consumeBlock := func(eventText string) error {
		eventCount++
		if eventCount > maxEvents {
			return errors.New("上游 Chat Completions 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		if len(eventText) > sseMaxEventBytes {
			return errors.New("上游 Chat Completions 单个事件超过 64 KiB 上限")
		}
		return consumeEvent(eventText)
	}
	trailing, err := pumpSSEBlocks(stream, consumeBlock)
	if err != nil {
		return result, err
	}
	if len(trailing) > sseMaxEventBytes {
		return result, errors.New("上游 Chat Completions 单个事件超过 64 KiB 上限")
	}
	if strings.TrimSpace(string(trailing)) != "" {
		eventCount++
		if eventCount > maxEvents {
			return result, errors.New("上游 Chat Completions 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		if err := consumeEvent(string(trailing)); err != nil {
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

var (
	sseLFBoundaryBytes   = []byte("\n\n")
	sseCRLFBoundaryBytes = []byte("\r\n\r\n")
)

// findEventBoundaryBytes 与 findEventBoundary 语义一致，作用于字节切片，
// 供增量读取路径复用同一套边界判定（含 \n\n 与 \r\n\r\n 的先后裁决）。
func findEventBoundaryBytes(value []byte) *eventBoundary {
	lf := bytes.Index(value, sseLFBoundaryBytes)
	crlf := bytes.Index(value, sseCRLFBoundaryBytes)
	if lf < 0 && crlf < 0 {
		return nil
	}
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return &eventBoundary{index: crlf, length: 4}
	}
	return &eventBoundary{index: lf, length: 2}
}

// sseReadStreamChunkBytes 是增量读取上游 SSE 的单次 Read 缓冲大小。
const sseReadStreamChunkBytes = 32 * 1024

// pumpSSEBlocks 从 stream 增量读取字节流：每凑齐一个完整事件块（以 \n\n 或
// \r\n\r\n 分隔）立即交给 consume 处理并返回，而不是先 io.ReadAll 全量缓冲。
// 增量解析保证下游在整段响应结束前收到事件（打字机/自动滚动），对齐 Node
// for-await 语义。读取过程中对已读字节做边界安全的 UTF-8 校验：多字节字符
// 拆在两次 Read 之间时，未凑齐的尾部字节暂存、与后续数据合并后再判定，不单
// 独判无效。consume 返回错误时立即终止读取并透传；EOF 后返回剩余未成块的
// 字节，由调用方按原有尾块语义（预算、事件数与错误文案）处理。
func pumpSSEBlocks(stream io.Reader, consume func(block string) error) ([]byte, error) {
	validator := utf8BoundaryValidator{}
	pending := make([]byte, 0, sseReadStreamChunkBytes)
	scratch := make([]byte, sseReadStreamChunkBytes)
	for {
		n, readErr := stream.Read(scratch)
		if n > 0 {
			if err := validator.feed(scratch[:n]); err != nil {
				return pending, err
			}
			pending = append(pending, scratch[:n]...)
			for {
				boundary := findEventBoundaryBytes(pending)
				if boundary == nil {
					break
				}
				block := string(pending[:boundary.index])
				pending = append(pending[:0], pending[boundary.index+boundary.length:]...)
				if err := consume(block); err != nil {
					return pending, err
				}
			}
		}
		if readErr != nil {
			// errors.Is 覆盖上游 Reader 以 wrapped error 返回 EOF 的实现。
			if errors.Is(readErr, io.EOF) {
				break
			}
			return pending, readErr
		}
	}
	if err := validator.finish(); err != nil {
		return pending, err
	}
	return pending, nil
}

// utf8BoundaryValidator 对增量到达的字节做 UTF-8 边界安全校验：结尾可能是
// 未凑齐的多字节序列时暂存 carry，与下一批数据合并后再判定，避免把跨 Read
// 截断的字符误判为无效。错误文案与全量 utf8.Valid 校验版本一致。
type utf8BoundaryValidator struct {
	carry    []byte // 上一批结尾未凑齐的多字节序列（0..3 字节）
	combined []byte // carry 与新数据拼接的复用缓冲
}

func (v *utf8BoundaryValidator) feed(chunk []byte) error {
	data := chunk
	if len(v.carry) > 0 {
		v.combined = append(v.combined[:0], v.carry...)
		v.combined = append(v.combined, chunk...)
		data = v.combined
	}
	trailing := utf8TrailingPartial(data)
	v.carry = append(v.carry[:0], data[len(data)-trailing:]...)
	if !utf8.Valid(data[:len(data)-trailing]) {
		return errors.New("上游返回了无效的 SSE JSON")
	}
	return nil
}

// finish 在流结束时调用：仍残留未凑齐的字节说明流在多字节字符中间截断，
// 整体视为无效 UTF-8（与对全量字节做 utf8.Valid 的结论一致）。
func (v *utf8BoundaryValidator) finish() error {
	if len(v.carry) > 0 {
		return errors.New("上游返回了无效的 SSE JSON")
	}
	return nil
}

// utf8TrailingPartial 返回 data 结尾可能属于未凑齐多字节 UTF-8 序列的字节
// 长度（0..3）。这些字节不能单独判定无效，须与后续数据合并校验。
func utf8TrailingPartial(data []byte) int {
	for i := 1; i <= 4 && i <= len(data); i++ {
		b := data[len(data)-i]
		if b&0xC0 != 0x80 {
			var size int
			switch {
			case b < 0x80:
				return 0
			case b&0xE0 == 0xC0:
				size = 2
			case b&0xF0 == 0xE0:
				size = 3
			case b&0xF8 == 0xF0:
				size = 4
			default:
				return 0 // 非法起始字节：不按残缺处理，交给 utf8.Valid 报错
			}
			if i < size {
				return i
			}
			return 0
		}
	}
	return 0
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
