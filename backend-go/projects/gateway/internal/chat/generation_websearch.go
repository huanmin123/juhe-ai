package chat

// web_search 模型工具的子代理执行器（AI问答工具体系与主子模型设计 §6.1/§9）：
// 用会话绑定的「账户 + 模型」发起一次非流式 Responses 子调用（hosted
// web_search 工具），固定派发到绑定账户（经进程内 /v1 链的调度覆盖端口，
// 与主对话执行器同构）；结果裁剪为 tool result 回喂主模型。子调用计入主轮次
// 取消链路（runCtx.Context），整体超时默认 120s；失败/超时返回错误 tool
// result（不中断主轮次——orchestrator 的 correctableFailures 机制回喂失败
// JSON）。子调用经 /v1 链天然产生独立 trace 与用量记录。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// chatWebSearchTimeoutMs 是子调用整体超时（契约 §6.1 默认 120s；env
// JUHE_AI_CHAT_WEB_SEARCH_TIMEOUT_MS 可覆盖，毫秒）。chatWebSearchResultMaxBytes
// 是回喂主模型的 tool result 字节上限（契约 §13.3 默认 4KB；env
// JUHE_AI_CHAT_WEB_SEARCH_RESULT_BYTES 可覆盖）。env 初始化求值一次。
var (
	chatWebSearchTimeoutMs      = chatWebSearchTimeoutConfig()
	chatWebSearchResultMaxBytes = chatWebSearchResultBytesConfig()
	// chatWebSearchMaxResponseBytes 是子调用响应体读取上限。
	chatWebSearchMaxResponseBytes = 2 * 1024 * 1024
	// chatWebSearchInstructions 引导子模型「执行搜索并汇总结果与来源 URL」。
	chatWebSearchInstructions = "你是搜索子代理。针对用户给出的搜索词执行联网搜索，用简明段落汇总关键结果（优先时效性信息），并在末尾以「来源:」列表逐行列出引用的 URL。只依据真实搜索结果作答，不得编造。"
)

func chatWebSearchTimeoutConfig() int64 {
	return int64(chatEnvIntOrDefault("JUHE_AI_CHAT_WEB_SEARCH_TIMEOUT_MS", 120*1000, 1000, 600*1000))
}

func chatWebSearchResultBytesConfig() int {
	return chatEnvIntOrDefault("JUHE_AI_CHAT_WEB_SEARCH_RESULT_BYTES", 4*1024, 512, 64*1024)
}

// executeChatWebSearch 执行 web_search 子调用并组装 tool result：
//   - executor 为绑定账户的固定派发执行器视图（stream_route 解析）；
//   - 子调用协议固定为 Responses + hosted web_search（候选过滤已保证绑定
//     「账户支持 responses_sse × 模型目录矩阵声明 web_search」，契约 §6.3）；
//   - 结果 = 子调用回答文本 + 来源 URL 清单，裁剪到字节上限；
//   - context 继承主轮次（用户停止时同步取消），整体超时 chatWebSearchTimeoutMs。
func executeChatWebSearch(runCtx *ChatGenerationExecutionContext, executor GenerationExecutor, apiKeySecret, traceID, model, query string) (chatToolExecutionResult, error) {
	if executor == nil {
		return chatToolExecutionResult{}, errors.New("搜索工具执行器未接线")
	}
	var parent context.Context = context.Background()
	if runCtx != nil && runCtx.Context != nil {
		parent = runCtx.Context
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(chatWebSearchTimeoutMs)*time.Millisecond)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":        model,
		"instructions": chatWebSearchInstructions,
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": query}},
		}},
		"tools": []any{map[string]any{"type": HostedToolSearchKey}},
	})
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	headers := map[string]string{
		"authorization":     "Bearer " + apiKeySecret,
		"content-type":      "application/json",
		"x-juhe-ai-purpose": "chat_web_search",
	}
	if traceID != "" {
		headers["x-trace-id"] = traceID
	}
	response, err := executor.Dispatch(ctx, GenerationDispatchRequest{
		Path: "/v1/responses", Method: "POST", Headers: headers, Body: body,
	})
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	if response == nil {
		return chatToolExecutionResult{}, errors.New("搜索子调用无响应")
	}
	defer func() { _ = response.Body.Close() }()
	payloadBytes, readErr := io.ReadAll(io.LimitReader(response.Body, int64(chatWebSearchMaxResponseBytes)+1))
	if readErr != nil {
		return chatToolExecutionResult{}, readErr
	}
	if len(payloadBytes) > chatWebSearchMaxResponseBytes {
		return chatToolExecutionResult{}, errors.New("搜索子调用响应超过大小上限")
	}
	if response.Status < 200 || response.Status >= 300 {
		return chatToolExecutionResult{}, fmt.Errorf("搜索子调用失败（HTTP %d）: %s", response.Status, upstreamMessagePayload(string(payloadBytes), "上游搜索请求失败"))
	}
	var parsed map[string]any
	if err := json.Unmarshal(payloadBytes, &parsed); err != nil {
		return chatToolExecutionResult{}, errors.New("搜索子调用响应解析失败")
	}
	text, sources := extractWebSearchResponse(parsed)
	result := formatWebSearchToolResult(text, sources, query)
	publicResult := map[string]any{"query": query, "sourceCount": len(sources)}
	if len(sources) > 0 {
		publicResult["sources"] = sources
	}
	return chatToolExecutionResult{ModelOutput: result, PublicResult: publicResult}, nil
}

// extractWebSearchResponse 从非流式 Responses 输出提取回答文本与来源 URL：
// 优先 output_text 汇总字段；否则聚合 output[].content[] 的 output_text 块；
// 来源取 content 块 annotations 里的 url_citation.url（提取不到时空列表，
// 由调用方回退正文）。
func extractWebSearchResponse(payload map[string]any) (string, []string) {
	if text, ok := payload["output_text"].(string); ok && strings.TrimSpace(text) != "" {
		return text, collectWebSearchCitations(payload)
	}
	output, _ := payload["output"].([]any)
	texts := []string{}
	for _, item := range output {
		entry, _ := item.(map[string]any)
		if entry == nil {
			continue
		}
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
	}
	return strings.Join(texts, "\n"), collectWebSearchCitations(payload)
}

// collectWebSearchCitations 收集 output[].content[].annotations[].url_citation
// 的 url 字段（去重、按首次出现顺序）。
func collectWebSearchCitations(payload map[string]any) []string {
	seen := map[string]bool{}
	sources := []string{}
	output, _ := payload["output"].([]any)
	for _, item := range output {
		entry, _ := item.(map[string]any)
		if entry == nil {
			continue
		}
		content, _ := entry["content"].([]any)
		for _, contentItem := range content {
			block, _ := contentItem.(map[string]any)
			if block == nil {
				continue
			}
			annotations, _ := block["annotations"].([]any)
			for _, annotationItem := range annotations {
				annotation, _ := annotationItem.(map[string]any)
				if annotation == nil {
					continue
				}
				if annotationType, _ := annotation["type"].(string); annotationType != "url_citation" {
					continue
				}
				url, _ := annotation["url"].(string)
				if url == "" || seen[url] {
					continue
				}
				seen[url] = true
				sources = append(sources, url)
			}
		}
	}
	return sources
}

// formatWebSearchToolResult 组装并裁剪回喂主模型的 tool result：正文在前、
// 来源清单在后；超限时先裁正文再裁来源，总量不超过 chatWebSearchResultMaxBytes。
func formatWebSearchToolResult(text string, sources []string, query string) string {
	if strings.TrimSpace(text) == "" && len(sources) == 0 {
		text = "搜索未返回可用结果（查询: " + query + "）"
	}
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(text))
	if len(sources) > 0 {
		builder.WriteString("\n\n来源:\n")
		for _, source := range sources {
			builder.WriteString("- " + source + "\n")
		}
	}
	result := builder.String()
	if len(result) <= chatWebSearchResultMaxBytes {
		return result
	}
	// 超限：保来源清单，裁正文。
	list := "\n\n来源:\n"
	for _, source := range sources {
		list += "- " + source + "\n"
	}
	if len(list) >= chatWebSearchResultMaxBytes {
		list = list[:chatWebSearchResultMaxBytes]
		return list
	}
	bodyBudget := chatWebSearchResultMaxBytes - len(list)
	if bodyBudget > 0 && len(text) > bodyBudget {
		text = truncateUTF8Bytes(text, bodyBudget)
	} else if bodyBudget <= 0 {
		text = ""
	}
	return text + list
}

// truncateUTF8Bytes 按字节上限裁剪且不撕裂 UTF-8 序列。
func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	runes := []rune(value)
	for len(runes) > 0 {
		candidate := string(runes)
		if len(candidate) <= maxBytes {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return ""
}
