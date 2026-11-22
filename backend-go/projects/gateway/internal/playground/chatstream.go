package playground

// POST /chat/stream —— 一次性对话 SSE（设计 §3.3 真实 Key 化修订）：全量上下
// 文由客户端携带，服务端零状态；按请求所选真实 API Key 派发 POST
// /v1/chat/completions（SSE）。归一事件只保留 delta/usage/error 三类（原
// round/tool_start/tool_end/done 随服务端 fetch_url 工具注入与多轮工具循环
// 退役；流式响应体关闭即结束信号）。上游 chat chunk 解析
// （choices[0].delta.content / 末 chunk usage / [DONE]）为本包自行实现的
// 精简版（bufio.Scanner 逐行 data: 前缀）。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 演练场对话限额（设计 §3.3，包内常量，无环境开关）。
const (
	chatMaxMessages     = 200        // messages 条数上限
	chatMaxMessageBytes = 64 * 1024  // 单条消息字节上限
	chatContentMaxBytes = 192 * 1024 // 上游正文累计上限（chat 面同口径）
	chatSSEMaxLineBytes = 512 * 1024 // 上游单行 SSE 载荷上限
	chatSSEMaxEvents    = 65536      // 上游事件数上限（chat 面默认值同口径）
)

// chatStreamMessage 是客户端携带的单条消息（role 限 user|assistant——工具
// 上下文由服务端循环内部持有，客户端回放只发 user/assistant 正文；content
// 恒纯文本——演练场不做多模态输入）。
type chatStreamMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatStreamRequest 是 POST /chat/stream 请求体（设计 §3.3）。
type chatStreamRequest struct {
	APIKeyID         string              `json:"apiKeyId"`
	ModelID          string              `json:"modelId"`
	Messages         []chatStreamMessage `json:"messages"`
	System           string              `json:"system"`
	Temperature      *float64            `json:"temperature"`
	MaxTokens        *float64            `json:"maxTokens"`
	Stream           *bool               `json:"stream"`
	TopP             *float64            `json:"topP"`
	FrequencyPenalty *float64            `json:"frequencyPenalty"`
	PresencePenalty  *float64            `json:"presencePenalty"`
}

// chatUsageInfo 是上游末 chunk 的 usage 归一（total 缺省按和补齐）。
type chatUsageInfo struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// chatStreamHandler 服务 POST /chat/stream。
func (rt *playgroundRoutes) chatStreamHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	body, err := readJSONBody(r, chatMaxMessages*chatMaxMessageBytes+64*1024)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req chatStreamRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	if httpErr := rt.validateChatCandidate(r.Context(), scope, &req); httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	rt.runChatStream(w, r.Context(), scope, &req)
}

// validateChatCandidate 校验 §3.3 输入并复核 §3.2 候选（apiKeyId/modelId 必填
// 且在所选 Key 的目录分类 chat 行内；缺失/越界分别映射 400/404 错误码；
// stream 仅支持 true）。Key 归属 fail-closed 校验经 resolveKeyModels 的读取
// 链收敛（Key 不存在/非本人 → 404）。
func (rt *playgroundRoutes) validateChatCandidate(ctx context.Context, scope Scope, req *chatStreamRequest) *HTTPError {
	if strings.TrimSpace(req.APIKeyID) == "" || strings.TrimSpace(req.ModelID) == "" {
		return invalidRequest("apiKeyId 与 modelId 必填")
	}
	if req.Stream != nil && !*req.Stream {
		return invalidRequest("演练场对话仅支持流式输出（stream=true）")
	}
	if len(req.Messages) < 1 || len(req.Messages) > chatMaxMessages {
		return invalidRequest(fmt.Sprintf("messages 必须为 1~%d 条", chatMaxMessages))
	}
	for index, message := range req.Messages {
		switch message.Role {
		case "user", "assistant":
		default:
			return invalidRequest(fmt.Sprintf("messages[%d].role 仅支持 user/assistant", index))
		}
		if len(message.Content) > chatMaxMessageBytes {
			return invalidRequest(fmt.Sprintf("messages[%d] 超过单条 64KB 上限", index))
		}
	}
	options, httpErr := rt.resolveKeyModels(ctx, scope.ViewerID, strings.TrimSpace(req.APIKeyID))
	if httpErr != nil {
		return httpErr
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindChat) == nil {
		return modelNotAvailable("模型不在所选 Key 的演练场对话候选内")
	}
	return nil
}

// runChatStream 执行对话：dispatch → 消费上游 SSE → 归一事件（delta/usage/
// error）下发。派发失败时 SSE 尚未开始，直接落 502 JSON；进入 SSE 后的失败
// 落 error 帧（此前已发 delta 照常已透出，设计 §3.3）。
func (rt *playgroundRoutes) runChatStream(w http.ResponseWriter, ctx context.Context, scope Scope, req *chatStreamRequest) {
	stream := newPlaygroundSSEWriter(w)
	// 心跳 ticker 必须随流结束停掉（含 error/panic 路径，sse.go 心跳契约）。
	defer stream.Stop()
	response, dispatchErr := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		APIKeyID:   strings.TrimSpace(req.APIKeyID),
		OwnerID:    scope.ViewerID,
		RemoteAddr: scope.RemoteAddr,
		Path:       "/v1/chat/completions",
		Method:     http.MethodPost,
		Headers: map[string]string{
			"content-type": "application/json",
			"accept":       "text/event-stream",
		},
		Body: buildChatCompletionsBody(req),
	})
	if dispatchErr != nil {
		writeHTTPError(w, dispatchError("上游派发失败", dispatchErr))
		return
	}
	if response == nil {
		writeHTTPError(w, dispatchFailed("上游派发失败"))
		return
	}
	if response.Status < 200 || response.Status >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		writeHTTPError(w, dispatchFailed(upstreamDispatchMessage("对话", response.Status, body)))
		return
	}
	result, consumeErr := consumeChatCompletionStream(response.Body, func(delta string) {
		stream.writeEvent(sseEventFrame{Type: "delta", Text: delta})
	})
	_ = response.Body.Close()
	if consumeErr != nil {
		stream.writeEvent(sseEventFrame{Type: "error", Code: codeDispatchFailed, Message: consumeErr.Error()})
		return
	}
	if result.Usage != nil {
		total := result.Usage.TotalTokens
		if total == 0 {
			total = result.Usage.PromptTokens + result.Usage.CompletionTokens
		}
		stream.writeEvent(sseEventFrame{
			Type:             "usage",
			PromptTokens:     &result.Usage.PromptTokens,
			CompletionTokens: &result.Usage.CompletionTokens,
			TotalTokens:      &total,
		})
	}
}

// buildChatCompletionsBody 构造出站 chat_completions 请求体（设计 §3.3：system
// 进首条 system 消息；采样参数显式传入才携带；maxTokens 走 max_completion_tokens
// ——与 chat 面参数切片同键；stream_options.include_usage 保证末 chunk 携带
// usage；不注入服务端工具——fetch_url 工具循环已退役）。
func buildChatCompletionsBody(req *chatStreamRequest) []byte {
	messages := []map[string]any{}
	if system := strings.TrimSpace(req.System); system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	for _, message := range req.Messages {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	body := map[string]any{
		"model":          strings.TrimSpace(req.ModelID),
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	setFloat := func(key string, value *float64) {
		if value != nil {
			body[key] = *value
		}
	}
	setFloat("temperature", req.Temperature)
	setFloat("top_p", req.TopP)
	setFloat("frequency_penalty", req.FrequencyPenalty)
	setFloat("presence_penalty", req.PresencePenalty)
	if req.MaxTokens != nil {
		body["max_completion_tokens"] = *req.MaxTokens
	}
	payload, _ := json.Marshal(body)
	return payload
}

// --- 上游 chat SSE 解析（精简版） ---

// chatStreamResult 是单次上游流消费产物。
type chatStreamResult struct {
	Content string
	Usage   *chatUsageInfo
}

// consumeChatCompletionStream 逐行消费上游 chat_completions SSE：delta.content
// 增量回调、usage 取末次非空、[DONE] 收尾。缺 [DONE] 视为上游异常（chat
// CollectOpenAIChatSse 同口径）；delta.tool_calls 不解析（服务端工具循环已
// 退役，tool_calls delta 无正文增量，模型行为交由上层提示约束）。
func consumeChatCompletionStream(reader io.Reader, onDelta func(delta string)) (*chatStreamResult, error) {
	result := &chatStreamResult{}
	var content strings.Builder
	finishDone := false
	eventCount := 0
	var usage *chatUsageInfo
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), chatSSEMaxLineBytes)
	for scanner.Scan() {
		line := scanner.Text()
		eventCount++
		if eventCount > chatSSEMaxEvents {
			return nil, fmt.Errorf("上游事件数量超过 %d 上限", chatSSEMaxEvents)
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			finishDone = true
			continue
		}
		if len(payload) > chatSSEMaxLineBytes {
			return nil, errors.New("上游单个 SSE 事件超过字节上限")
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     *int64 `json:"prompt_tokens"`
				CompletionTokens *int64 `json:"completion_tokens"`
				TotalTokens      *int64 `json:"total_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, errors.New("上游返回了无效的 SSE JSON")
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("上游流式请求失败: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil && (chunk.Usage.PromptTokens != nil || chunk.Usage.CompletionTokens != nil) {
			info := &chatUsageInfo{}
			if chunk.Usage.PromptTokens != nil {
				info.PromptTokens = *chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens != nil {
				info.CompletionTokens = *chunk.Usage.CompletionTokens
			}
			if chunk.Usage.TotalTokens != nil {
				info.TotalTokens = *chunk.Usage.TotalTokens
			}
			usage = info
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				if content.Len()+len(delta.Content) > chatContentMaxBytes {
					return nil, errors.New("回答内容超过 192 KiB 上限")
				}
				content.WriteString(delta.Content)
				if onDelta != nil {
					onDelta(delta.Content)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("上游流读取失败: %w", err)
	}
	if !finishDone {
		return nil, errors.New("上游流式响应缺少 [DONE]")
	}
	result.Content = content.String()
	result.Usage = usage
	return result, nil
}
