package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 辅助请求统一（PLAN-20261012T121500000Z D4「辅助请求统一」）：自动/手动压缩
// 与用户图片观察按绑定账户主推协议构造请求，响应形态按账户真实 profile 决定
// 不假设 JSON——Codex OAuth 归一化强制 stream=true（oauthnormalizer），这类
// 账户的辅助响应是 SSE 流，须经有界聚合读出完整语义；原生非 OAuth 账户仍走
// 普通 JSON。构造与消费收敛到本文件，压缩服务（compaction_service.go）与
// 图片观察适配器（cmd/juhe-ai-gateway/chain_chat_observation.go）共用。

// AuxiliaryResponseMaxBytes 是辅助响应的读取上限（压缩/观察同预算，现状
// 128 KiB）。
const AuxiliaryResponseMaxBytes = 128 * 1024

// BuildAuxiliaryModelRequest 构造辅助模型请求体：responses 形态顶层
// instructions + input（input_text 内容块）；chat 形态首条 system message +
// user message。两形态均按非流式发起（stream:false）；OAuth 账户被归一化强制
// 流式时由 ConsumeAuxiliaryModelText 按 Content-Type 聚合。返回 /v1 入口路径
// 与请求体。
func BuildAuxiliaryModelRequest(protocol ChatTransportProtocol, model, instructions, userContent string) (string, map[string]any) {
	if protocol == ProtocolResponses {
		body := map[string]any{
			"model":        model,
			"instructions": instructions,
			"input": []any{map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "input_text",
					"text": userContent,
				}},
			}},
			"stream": false,
		}
		return "/v1/responses", body
	}
	body := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": instructions},
			map[string]any{"role": "user", "content": userContent},
		},
		"stream": false,
	}
	return "/v1/chat/completions", body
}

// AuxiliaryHTTPStatusError 是辅助模型响应的非 2xx 状态（调用方按各自既有
// 错误码词表格式化：压缩 chat_context_model_http_%d、观察
// chat_image_observation_http_%d）。
type AuxiliaryHTTPStatusError struct{ Status int }

func (e *AuxiliaryHTTPStatusError) Error() string {
	return fmt.Sprintf("辅助模型请求失败（HTTP %d）", e.Status)
}

// ConsumeAuxiliaryModelText 消费辅助模型响应并提取输出文本：Content-Type 为
// text/event-stream（OAuth 归一化强制流式）时走有界 SSE 聚合（仅合法
// response.completed 算成功，EOF 无终态报错），否则按请求形态解析普通 JSON。
// maxBytes 同现状预算（压缩/观察 128 KiB）。
func ConsumeAuxiliaryModelText(response *GenerationDispatchResponse, protocol ChatTransportProtocol, maxBytes int64) (string, error) {
	if response == nil {
		return "", errors.New("chat_gateway_dispatch_missing")
	}
	if response.Status < 200 || response.Status >= 300 {
		return "", &AuxiliaryHTTPStatusError{Status: response.Status}
	}
	if isEventStreamResponse(response) {
		text, err := AggregateResponsesSseText(response.Body, maxBytes)
		if err != nil {
			return "", err
		}
		return boundedString(text, int(maxBytes)), nil
	}
	bodyBytes, readErr := readBoundedAll(response.Body, maxBytes)
	if readErr != nil {
		return "", errors.New("chat_auxiliary_model_missing_response")
	}
	var parsed map[string]any
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return "", errors.New("chat_auxiliary_model_missing_response")
	}
	if protocol == ProtocolResponses {
		return boundedString(responsesCompletedOutputText(parsed), int(maxBytes)), nil
	}
	return extractCompactionResponseText(parsed), nil
}

// isEventStreamResponse 判定上游是否以 SSE 流式响应（OAuth 归一化强制
// stream=true 的真实信号；响应形态按账户行为决定，不假设 JSON）。
func isEventStreamResponse(response *GenerationDispatchResponse) bool {
	if response == nil {
		return false
	}
	contentType := ""
	if response.Header != nil {
		contentType = response.Header.Get("Content-Type")
	}
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// AggregateResponsesSseText 有界聚合 Responses SSE 流的输出文本（非流式聚合
// helper，与主对话 collector 同一事件边界与解析语义）：累积 output_text.delta；
// 仅 response.completed 算成功（增量缺失时从 completed 响应对象聚合正文）；
// response.failed / response.incomplete / error 事件保留原始错误；EOF 无
// completed 报错；maxBytes 限制累积正文字节。
func AggregateResponsesSseText(stream io.Reader, maxBytes int64) (string, error) {
	var content strings.Builder
	done := false
	fail := func(err error) (string, error) {
		return content.String(), err
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
			Response map[string]any `json:"response"`
			Error    *struct {
				Message any `json:"message"`
			} `json:"error"`
			Message any `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return errors.New("上游返回了无效的 SSE JSON")
		}
		switch payload.Type {
		case "response.output_text.delta":
			if payload.Delta == "" {
				return nil
			}
			if int64(content.Len())+int64(len(payload.Delta)) > maxBytes {
				return errors.New("chat_auxiliary_model_payload_too_large")
			}
			content.WriteString(payload.Delta)
		case "response.completed":
			if strings.TrimSpace(content.String()) == "" && payload.Response != nil {
				text := responsesCompletedOutputText(payload.Response)
				if int64(len(text)) > maxBytes {
					return errors.New("chat_auxiliary_model_payload_too_large")
				}
				content.WriteString(text)
			}
			done = true
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
			return errors.New("上游流式请求失败")
		}
		return nil
	}
	consumeBlock := func(eventText string) error {
		if len(eventText) > sseMaxEventBytes {
			return errors.New("上游 Responses 单个事件超过 64 KiB 上限")
		}
		return consumeEvent(eventText)
	}
	trailing, err := pumpSSEBlocks(stream, consumeBlock)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(string(trailing)) != "" {
		if err := consumeEvent(string(trailing)); err != nil {
			return fail(err)
		}
	}
	if !done {
		return fail(errors.New("上游 Responses 流式响应缺少 response.completed 终态"))
	}
	return content.String(), nil
}

// ResolveChatRouteForAccount 是协议解析的导出入口（cmd 侧图片观察适配器按
// 绑定账户运行时快照解析主推请求形态）。
type ChatResolvedRoute = chatResolvedRoute

func ResolveChatRequestProtocol(account ChatTransportAccount, model string) (*ChatResolvedRoute, error) {
	return resolveChatRequestProtocol(account, model)
}
