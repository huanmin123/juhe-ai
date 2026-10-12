package chat

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// OpenAI Responses SSE collection（PLAN-20261012T121500000Z D4 响应消费契约）：
// 与 CollectOpenAIChatSse 并列的主对话 Responses 流消费器。事件框架、预算边界
// （单事件 64 KiB、事件数上限、UTF-8 跨 chunk 校验）复用 Chat 侧的
// pumpSSEBlocks/utf8BoundaryValidator；完成判据为严格终态——仅合法
// response.completed 算成功，response.failed / response.incomplete / error
// 事件 / 缺终态 EOF 明确失败并保留已收到正文与原始错误。产出与 Chat collector
// 相同的内部事件（文本 delta → 正文块、reasoning summary delta → reasoning 块、
// usage、finish）。

// OpenAIResponsesSseResult mirrors the consumed subset of a Responses stream.
type OpenAIResponsesSseResult struct {
	Content      string
	FinishReason string
	Done         bool
	InputTokens  *int64
	OutputTokens *int64
}

// CollectOpenAIResponsesSse consumes a Responses SSE stream. onDelta carries
// answer content; onReasoningDelta carries reasoning summary deltas (streamed
// through without counting against maxContentBytes).
func CollectOpenAIResponsesSse(stream io.Reader, maxContentBytes int, onDelta func(delta string), onReasoningDelta func(delta string), maxEvents int) (OpenAIResponsesSseResult, error) {
	result := OpenAIResponsesSseResult{}
	if maxEvents <= 0 {
		maxEvents = defaultMaxSSEEvents
	}
	var (
		content    strings.Builder
		finishDone bool
		eventCount int
	)
	fail := func(err error) (OpenAIResponsesSseResult, error) {
		result.Content = content.String()
		return result, err
	}
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
			return nil
		}
		var payload struct {
			Type     string         `json:"type"`
			Delta    string         `json:"delta"`
			Item     map[string]any `json:"item"`
			Response map[string]any `json:"response"`
			Error    *struct {
				Message any `json:"message"`
			} `json:"error"`
			Message any `json:"message"`
			Code    any `json:"code"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return errors.New("上游返回了无效的 SSE JSON")
		}
		switch payload.Type {
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if payload.Delta != "" && onReasoningDelta != nil {
				onReasoningDelta(payload.Delta)
			}
		case "response.output_text.delta":
			if payload.Delta == "" {
				return nil
			}
			nextBytes := content.Len() + len(payload.Delta)
			if nextBytes > maxContentBytes {
				return errors.New("回答内容超过 192 KiB 上限")
			}
			content.WriteString(payload.Delta)
			if onDelta != nil {
				onDelta(payload.Delta)
			}
		case "response.output_item.done":
			// hosted 工具（web_search 等）事件经既有内容块投影通道下发；本
			// collector 只聚合文本与 reasoning 增量，工具 item 不重复投影。
			return nil
		case "response.completed":
			response := payload.Response
			if response == nil {
				return errors.New("上游 Responses 流的 completed 事件缺少响应对象")
			}
			if usage, ok := response["usage"].(map[string]any); ok {
				if value := nonNegativeInteger(usage["input_tokens"]); value != nil {
					result.InputTokens = value
				}
				if value := nonNegativeInteger(usage["output_tokens"]); value != nil {
					result.OutputTokens = value
				}
			}
			// 增量缺失兜底：上游未发送 output_text.delta（或被截断）时，从
			// completed 响应对象聚合正文，保证成功终态不丢文本。
			if strings.TrimSpace(content.String()) == "" {
				if text := responsesCompletedOutputText(response); text != "" {
					if len(text) > maxContentBytes {
						return errors.New("回答内容超过 192 KiB 上限")
					}
					content.WriteString(text)
					if onDelta != nil {
						onDelta(text)
					}
				}
			}
			finishDone = true
			result.Done = true
			result.FinishReason = "stop"
			return nil
		case "response.failed", "response.incomplete":
			return errors.New(responsesStreamFailureMessage(payload.Response, payload.Type))
		case "error":
			if payload.Error != nil {
				if message, ok := payload.Error.Message.(string); ok && message != "" {
					return errors.New(message)
				}
			}
			if message, ok := payload.Message.(string); ok && message != "" {
				return errors.New(message)
			}
			return errors.New("上游 Responses 流式请求失败")
		}
		return nil
	}
	consumeBlock := func(eventText string) error {
		eventCount++
		if eventCount > maxEvents {
			return errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		if len(eventText) > sseMaxEventBytes {
			return errors.New("上游 Responses 单个事件超过 64 KiB 上限")
		}
		return consumeEvent(eventText)
	}
	trailing, err := pumpSSEBlocks(stream, consumeBlock)
	if err != nil {
		return fail(err)
	}
	if len(trailing) > sseMaxEventBytes {
		return fail(errors.New("上游 Responses 单个事件超过 64 KiB 上限"))
	}
	if strings.TrimSpace(string(trailing)) != "" {
		eventCount++
		if eventCount > maxEvents {
			return fail(errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限"))
		}
		if err := consumeEvent(string(trailing)); err != nil {
			return fail(err)
		}
	}
	if !finishDone {
		return fail(errors.New("上游 Responses 流式响应缺少 response.completed 终态"))
	}
	result.Content = content.String()
	return result, nil
}

// responsesStreamFailureMessage 从 failed/incomplete 事件中提取原始错误信息：
// 优先 response.error.message，其次 response.status_detail；incomplete 事件
// 显式标注不完整终态（不伪装成功）。
func responsesStreamFailureMessage(response map[string]any, eventType string) string {
	if response != nil {
		if errObj, ok := response["error"].(map[string]any); ok {
			if message, ok := errObj["message"].(string); ok && strings.TrimSpace(message) != "" {
				return message
			}
		}
		if message, ok := response["error"].(string); ok && strings.TrimSpace(message) != "" {
			return message
		}
		if detail, ok := response["status_detail"].(string); ok && strings.TrimSpace(detail) != "" {
			if eventType == "response.incomplete" {
				return "上游响应不完整（" + detail + "）"
			}
			return detail
		}
	}
	if eventType == "response.incomplete" {
		return "上游响应不完整（response.incomplete）"
	}
	return "上游 Responses 流式请求失败"
}

// responsesCompletedOutputText 从 completed 响应对象聚合正文：优先
// output_text 汇总字段，否则拼接 output[].content[] 的 output_text 块。
func responsesCompletedOutputText(response map[string]any) string {
	if text, ok := response["output_text"].(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	output, _ := response["output"].([]any)
	texts := []string{}
	for _, item := range output {
		entry, _ := item.(map[string]any)
		if entry == nil {
			continue
		}
		itemType, _ := entry["type"].(string)
		if itemType == "message" {
			content, _ := entry["content"].([]any)
			for _, contentItem := range content {
				block, _ := contentItem.(map[string]any)
				if block == nil {
					continue
				}
				if blockType, _ := block["type"].(string); blockType != "output_text" {
					continue
				}
				if text, ok := block["text"].(string); ok && text != "" {
					texts = append(texts, text)
				}
			}
			continue
		}
	}
	return strings.Join(texts, "\n")
}
