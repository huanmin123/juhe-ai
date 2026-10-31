package playground

// POST /chat/stream —— 一次性对话 SSE（设计 §3.3）：全量上下文由客户端携带，
// 服务端零状态；固定派发 POST /v1/chat/completions（SSE），fetch_url 由后端
// 执行，工具循环最多 3 轮，第 3 轮后不再注入工具（强制收尾）。上游 chat
// chunk 解析（choices[0].delta.content / delta.tool_calls / 末 chunk usage /
// [DONE]）为本包自行实现的精简版（解析惯例参考 internal/chat/
// generation_websearch.go 的 consumeWebSearchStream——bufio.Scanner 逐行
// data: 前缀；chat 包有并行未提交改动且将整体退役，不 import）。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// 演练场对话限额（设计 §3.3/§3.7，包内常量，无环境开关）。
const (
	chatMaxMessages     = 200        // messages 条数上限
	chatMaxMessageBytes = 64 * 1024  // 单条消息字节上限
	chatToolRoundsMax   = 3          // 工具轮上限（第 3 轮后不再注入工具；收尾派发恒无工具）
	chatContentMaxBytes = 192 * 1024 // 上游正文累计上限（chat 面同口径）
	chatSSEMaxLineBytes = 512 * 1024 // 上游单行 SSE 载荷上限
	chatSSEMaxEvents    = 65536      // 上游事件数上限（chat 面默认值同口径）
	toolArgsMaxBytes    = 64 * 1024  // 单个工具调用参数累计上限（chat 面同口径）
	toolSummaryMaxRunes = 160        // tool_end summary 首行截断长度
	// purposeHeaderKey 是设计 §3.7 的审计 purpose 头（chat=x-juhe-ai-purpose:
	// playground_chat；媒体三场景同款后缀）。
	purposeHeaderKey        = "x-juhe-ai-purpose"
	playgroundPurposeChat   = "playground_chat"
	playgroundPurposeImages = "playground_images"
	playgroundPurposeVideos = "playground_videos"
	playgroundPurposeSpeech = "playground_speech"
)

// fetchURLToolName 是演练场唯一的后端执行函数工具（设计 §1.1.7）。
const fetchURLToolName = "fetch_url"

// chatStreamMessage 是客户端携带的单条消息（role 限 user|assistant——工具
// 上下文由服务端循环内部持有，客户端回放只发 user/assistant 正文；content
// 恒纯文本——演练场不做多模态输入）。
type chatStreamMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatStreamRequest 是 POST /chat/stream 请求体（设计 §3.3）。
type chatStreamRequest struct {
	AccountID        string              `json:"accountId"`
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

// chatCandidate 是发送前解析的候选事实：functionCalling 决定 fetch_url 注入
// （默认打开不设开关，设计 §1.1.6/§1.1.7）。
type chatCandidate struct {
	FunctionCalling bool
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
	candidate, candidateErr := rt.resolveChatCandidate(r.Context(), scope, &req)
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	rt.runChatStream(w, r.Context(), scope, candidate, &req)
}

// resolveChatCandidate 校验 §3.3 输入并复核 §3.2 候选（accountId/modelId 必填
// 且在目录分类的 chat 行内；缺失/越界分别映射 400/404 错误码；stream 仅支持
// true）。
func (rt *playgroundRoutes) resolveChatCandidate(ctx context.Context, scope Scope, req *chatStreamRequest) (*chatCandidate, *HTTPError) {
	if strings.TrimSpace(req.AccountID) == "" || strings.TrimSpace(req.ModelID) == "" {
		return nil, invalidRequest("accountId 与 modelId 必填")
	}
	if req.Stream != nil && !*req.Stream {
		return nil, invalidRequest("演练场对话仅支持流式输出（stream=true）")
	}
	if len(req.Messages) < 1 || len(req.Messages) > chatMaxMessages {
		return nil, invalidRequest(fmt.Sprintf("messages 必须为 1~%d 条", chatMaxMessages))
	}
	for index, message := range req.Messages {
		switch message.Role {
		case "user", "assistant":
		default:
			return nil, invalidRequest(fmt.Sprintf("messages[%d].role 仅支持 user/assistant", index))
		}
		if len(message.Content) > chatMaxMessageBytes {
			return nil, invalidRequest(fmt.Sprintf("messages[%d] 超过单条 64KB 上限", index))
		}
	}
	_, options, httpErr := rt.resolveAccountModels(ctx, scope, strings.TrimSpace(req.AccountID))
	if httpErr != nil {
		return nil, httpErr
	}
	if options == nil {
		return nil, accountNotFound()
	}
	option := modelOptionOf(options, strings.TrimSpace(req.ModelID), KindChat)
	if option == nil {
		return nil, modelNotAvailable("模型不在该账户的演练场对话候选内")
	}
	return &chatCandidate{FunctionCalling: option.FunctionCalling}, nil
}

// runChatStream 执行对话主循环：dispatch → 消费上游 SSE →（工具轮）→ 归一事件
// 下发。第 1 次派发失败时 SSE 尚未开始，直接落 502 JSON；进入 SSE 后的失败
// 落 error 帧（此前已发 delta 照常已透出，设计 §3.3）。
func (rt *playgroundRoutes) runChatStream(w http.ResponseWriter, ctx context.Context, scope Scope, candidate *chatCandidate, req *chatStreamRequest) {
	stream := newPlaygroundSSEWriter(w)
	// 心跳 ticker 必须随流结束停掉（含 error/panic 路径，sse.go 心跳契约）。
	defer stream.Stop()
	// continuation 携带工具轮的 assistant tool_calls 消息与 tool 结果消息
	// （chat buildChatToolContinuation 同构：role:"tool" + tool_call_id）。
	continuation := []map[string]any{}
	var lastUsage *chatUsageInfo
	round := 0
	for {
		round++
		// 第 3 轮后不再注入工具（强制收尾）；此后即使模型仍返回 tool_calls
		// 也终止循环（不发 done 之外的执行事件）。
		injectTools := candidate.FunctionCalling && round <= chatToolRoundsMax
		response, dispatchErr := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
			AccountID: strings.TrimSpace(req.AccountID),
			OwnerID:   scope.ViewerID,
			Path:      "/v1/chat/completions",
			Method:    http.MethodPost,
			Headers: map[string]string{
				"content-type":   "application/json",
				"accept":         "text/event-stream",
				purposeHeaderKey: playgroundPurposeChat,
			},
			Body: buildChatCompletionsBody(req, continuation, injectTools),
		})
		if dispatchErr != nil || response == nil {
			message := "上游派发失败"
			if dispatchErr != nil {
				message = "上游派发失败: " + dispatchErr.Error()
			}
			if round == 1 {
				writeHTTPError(w, dispatchFailed(message))
			} else {
				stream.writeEvent(sseEventFrame{Type: "error", Code: codeDispatchFailed, Message: message})
			}
			return
		}
		if response.Status < 200 || response.Status >= 300 {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			message := upstreamDispatchMessage("对话", response.Status, body)
			if round == 1 {
				writeHTTPError(w, dispatchFailed(message))
			} else {
				stream.writeEvent(sseEventFrame{Type: "error", Code: codeDispatchFailed, Message: message})
			}
			return
		}
		stream.writeEvent(sseEventFrame{Type: "round", Round: round})
		result, consumeErr := consumeChatCompletionStream(response.Body, func(delta string) {
			stream.writeEvent(sseEventFrame{Type: "delta", Text: delta})
		})
		_ = response.Body.Close()
		if consumeErr != nil {
			stream.writeEvent(sseEventFrame{Type: "error", Code: codeDispatchFailed, Message: consumeErr.Error()})
			return
		}
		if result.Usage != nil {
			lastUsage = result.Usage
		}
		// 工具轮：模型发起 fetch_url 且还有工具预算（第 1~3 轮）→ 执行并回喂
		// 重派发；第 3 轮之后的收尾派发不注入工具，模型若仍返回 tool_calls
		// 不再执行（循环终止，按既有正文落 done）。
		if len(result.ToolCalls) > 0 && round <= chatToolRoundsMax {
			continuation = append(continuation, assistantToolCallsItem(result))
			for _, call := range result.ToolCalls {
				stream.writeEvent(sseEventFrame{Type: "tool_start", CallID: call.CallID, Name: call.ToolName, Args: call.ArgumentsJSON})
				resultText, ok := rt.executeFetchTool(ctx, call)
				summary := firstLineSummary(resultText, toolSummaryMaxRunes)
				stream.writeEvent(sseEventFrame{Type: "tool_end", CallID: call.CallID, Name: call.ToolName, OK: &ok, Summary: summary})
				continuation = append(continuation, map[string]any{
					"role":         "tool",
					"tool_call_id": call.CallID,
					// 工具结果标注来源 URL（设计 §7：来源 URL 由模型自身判断）。
					"content": "URL: " + call.SourceURL + "\n\n" + resultText,
				})
			}
			continue
		}
		break
	}
	if lastUsage != nil {
		total := lastUsage.TotalTokens
		if total == 0 {
			total = lastUsage.PromptTokens + lastUsage.CompletionTokens
		}
		stream.writeEvent(sseEventFrame{
			Type:             "usage",
			PromptTokens:     &lastUsage.PromptTokens,
			CompletionTokens: &lastUsage.CompletionTokens,
			TotalTokens:      &total,
		})
	}
	rounds := round
	stream.writeEvent(sseEventFrame{Type: "done", Rounds: &rounds})
}

// executeFetchTool 执行一次 fetch_url 工具调用（设计 §3.3：失败不产生 HTTP
// 错误，把「抓取失败:<原因>」作为工具结果回喂模型）。返回值是工具结果正文
// （来源 URL 标注由调用方统一拼装，避免双写前缀）。
func (rt *playgroundRoutes) executeFetchTool(ctx context.Context, call chatToolCall) (string, bool) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil || strings.TrimSpace(args.URL) == "" {
		return "抓取失败: 工具参数必须是 {\"url\": \"<http(s) URL>\"}", false
	}
	fetch := rt.deps.FetchURL
	if fetch == nil {
		fetch = defaultFetchURL
	}
	result, err := fetch(ctx, args.URL)
	if err != nil {
		return "抓取失败: " + err.Error(), false
	}
	if strings.TrimSpace(result.Text) == "" {
		return "(页面无可提取正文)", true
	}
	return result.Text, true
}

// buildChatCompletionsBody 构造出站 chat_completions 请求体（设计 §3.3：system
// 进首条 system 消息；function_calling 模型注入 fetch_url 工具定义；采样参数
// 显式传入才携带；maxTokens 走 max_completion_tokens——与 chat 面参数切片同
// 键；stream_options.include_usage 保证末 chunk 携带 usage）。
func buildChatCompletionsBody(req *chatStreamRequest, continuation []map[string]any, injectTools bool) []byte {
	messages := []map[string]any{}
	if system := strings.TrimSpace(req.System); system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	for _, message := range req.Messages {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	for _, item := range continuation {
		messages = append(messages, map[string]any(item))
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
	if injectTools {
		body["tools"] = []map[string]any{fetchURLToolDefinition()}
	}
	payload, _ := json.Marshal(body)
	return payload
}

// fetchURLToolDefinition 是 fetch_url 的 function 工具定义（设计 §3.3：参数
// {"url": "<http(s) URL>"}）。
func fetchURLToolDefinition() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        fetchURLToolName,
			"description": "抓取一个公网 http(s) 网页并返回正文文本，用于获取链接里的资料。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "要抓取的 http(s) URL"},
				},
				"required": []string{"url"},
			},
		},
	}
}

// assistantToolCallsItem 把本轮 assistant tool_calls 消息追加进续传上下文
// （chat CollectOpenAIChatSse 的 ContinuationItems 同构：assistant 角色 +
// tool_calls 数组，content 无正文时为 null）。
func assistantToolCallsItem(result *chatStreamResult) map[string]any {
	toolCalls := make([]map[string]any, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"id":   call.CallID,
			"type": "function",
			"function": map[string]any{
				"name":      call.ToolName,
				"arguments": call.ArgumentsJSON,
			},
		})
	}
	item := map[string]any{"role": "assistant", "tool_calls": toolCalls}
	if result.Content != "" {
		item["content"] = result.Content
	} else {
		item["content"] = nil
	}
	return item
}

// --- 上游 chat SSE 解析（精简版） ---

// chatToolCall 是聚合完成的单个工具调用（index 排序输出）。
type chatToolCall struct {
	CallID        string
	ToolName      string
	ArgumentsJSON string
	SourceURL     string // fetch_url 参数里的 url（tool 结果标注来源用）
}

// chatStreamResult 是单次上游流消费产物。
type chatStreamResult struct {
	Content   string
	ToolCalls []chatToolCall
	Usage     *chatUsageInfo
}

// consumeChatCompletionStream 逐行消费上游 chat_completions SSE：delta.content
// 增量回调、tool_calls 按 index 聚合、usage 取末次非空、[DONE] 收尾。缺
// [DONE] 视为上游异常（chat CollectOpenAIChatSse 同口径）。
func consumeChatCompletionStream(reader io.Reader, onDelta func(delta string)) (*chatStreamResult, error) {
	result := &chatStreamResult{ToolCalls: []chatToolCall{}}
	var content strings.Builder
	finishDone := false
	eventCount := 0
	toolParts := map[int]*chatToolCallPart{}
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
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function *struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
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
			for _, toolCall := range delta.ToolCalls {
				if toolCall.Index == nil || *toolCall.Index < 0 || *toolCall.Index > 255 {
					return nil, errors.New("Chat 工具调用 index 无效")
				}
				part, ok := toolParts[*toolCall.Index]
				if !ok {
					part = &chatToolCallPart{index: *toolCall.Index}
					toolParts[*toolCall.Index] = part
				}
				part.id = mergeStableToolField(part.id, toolCall.ID)
				if toolCall.Function != nil {
					part.name = mergeStableToolField(part.name, toolCall.Function.Name)
					part.arguments += toolCall.Function.Arguments
					if len(part.arguments) > toolArgsMaxBytes {
						return nil, errors.New("Chat 单个工具参数超过 64 KiB 上限")
					}
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
	indices := make([]int, 0, len(toolParts))
	for index := range toolParts {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		part := toolParts[index]
		if part.id == "" || part.name == "" || part.arguments == "" {
			return nil, errors.New("Chat 工具调用缺少 id、name 或 arguments")
		}
		call := chatToolCall{CallID: part.id, ToolName: part.name, ArgumentsJSON: part.arguments}
		if part.name == fetchURLToolName {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(part.arguments), &args); err == nil {
				call.SourceURL = args.URL
			}
		}
		result.ToolCalls = append(result.ToolCalls, call)
	}
	result.Content = content.String()
	result.Usage = usage
	return result, nil
}

// chatToolCallPart 是按 index 聚合中的工具调用分片。
type chatToolCallPart struct {
	index     int
	id        string
	name      string
	arguments string
}

// mergeStableToolField 聚合分片间的稳定字段（id/name 只在分片间出现一次，
// 重传/重复片段不重复拼接；chat gateway_sse.go 同名函数的精简复制）。
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

// firstLineSummary 取文本首行做 tool_end 摘要（按 rune 截断，保证不劈开
// 多字节字符）。
func firstLineSummary(text string, maxRunes int) string {
	trimmed := strings.TrimSpace(text)
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		trimmed = trimmed[:index]
	}
	runes := []rune(trimmed)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return trimmed
}
