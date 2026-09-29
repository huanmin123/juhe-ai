package chat

// web_search 模型工具的子代理执行器（AI问答工具体系与主子模型设计 §6.1/§9）：
// 用会话绑定的「账户 + 模型」发起一次流式 Responses 子调用（hosted web_search
// 工具 + reasoning summary），固定派发到绑定账户（经进程内 /v1 链的调度覆盖
// 端口，与主对话执行器同构）；流式增量（思考摘要/搜索动作/回答摘要）经
// progress 回调渐进下发（内容块投影通道 → 前端子代理过程区，契约 §10.3）；
// 最终文本与来源以 response.completed 内的完整响应为权威（复用非流式提取），
// 裁剪为 tool result 回喂主模型。子调用计入主轮次取消链路（runCtx.Context），
// 整体超时默认 120s；失败/超时返回错误 tool result（不中断主轮次——
// orchestrator 的 correctableFailures 机制回喂失败 JSON）。子调用经 /v1 链
// 天然产生独立 trace 与用量记录。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
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
	// chatWebSearchMaxResponseBytes 是子调用单块/终态响应的读取上限。
	chatWebSearchMaxResponseBytes = 2 * 1024 * 1024
	// chatWebSearchProgressIntervalMs 是过程增量下发节流窗口（阶段切换即时）。
	chatWebSearchProgressIntervalMs = 400
	// chatWebSearchInstructions 引导子模型「执行搜索并汇总结果与来源 URL」。
	chatWebSearchInstructions = "你是搜索子代理。针对用户给出的搜索词执行联网搜索，用简明段落汇总关键结果（优先时效性信息），并在末尾以「来源:」列表逐行列出引用的 URL。只依据真实搜索结果作答，不得编造。"
)

func chatWebSearchTimeoutConfig() int64 {
	return int64(chatEnvIntOrDefault("JUHE_AI_CHAT_WEB_SEARCH_TIMEOUT_MS", 120*1000, 1000, 600*1000))
}

func chatWebSearchResultBytesConfig() int {
	return chatEnvIntOrDefault("JUHE_AI_CHAT_WEB_SEARCH_RESULT_BYTES", 4*1024, 512, 64*1024)
}

// chatWebSearchProgress 是子代理过程增量（契约 §6.1/§6.2/§10.3）：stage 推进
// reasoning → searching → answering；actions 为已发生的搜索动作可读描述；
// reasoning/answer 为累积文本（progress 限长，完整内容仅在回喂 result 内）；
// stageTimings 为各已完成阶段的耗时毫秒映射（在阶段切换点结算，进行中阶段
// 不计入），随过程快照下发并随终态最后一次快照落库。
type chatWebSearchProgress struct {
	Stage        string           `json:"stage"`
	Reasoning    string           `json:"reasoning,omitempty"`
	Actions      []string         `json:"actions,omitempty"`
	Answer       string           `json:"answer,omitempty"`
	StageTimings map[string]int64 `json:"stageTimings,omitempty"`
}

// executeChatWebSearch 执行 web_search 子调用并组装 tool result：
//   - executor 为绑定账户的固定派发执行器视图（stream_route 解析）；
//   - 子调用协议固定为流式 Responses + hosted web_search（候选过滤已保证绑定
//     「账户支持 responses_sse × 模型目录矩阵声明 web_search」，契约 §6.3）；
//   - progress 回调以节流窗口渐进下发过程增量（可为 nil，测试/降级场景）；
//   - 最终结果 = completed 响应的回答文本 + 来源 URL 清单，裁剪到字节上限；
//   - context 继承主轮次（用户停止时同步取消），整体超时 chatWebSearchTimeoutMs。
func executeChatWebSearch(runCtx *ChatGenerationExecutionContext, executor GenerationExecutor, apiKeySecret, traceID, model, query string, progress func(chatWebSearchProgress)) (chatToolExecutionResult, error) {
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
		// 流式子调用：增量驱动子代理过程区；思考摘要供前端展示（§10.3）。
		"stream":    true,
		"reasoning": map[string]any{"summary": "auto"},
	})
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	headers := map[string]string{
		"authorization":     "Bearer " + apiKeySecret,
		"content-type":      "application/json",
		"accept":            "text/event-stream",
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
	if response.Status < 200 || response.Status >= 300 {
		payloadBytes, readErr := io.ReadAll(io.LimitReader(response.Body, int64(chatWebSearchMaxResponseBytes)+1))
		if readErr != nil {
			return chatToolExecutionResult{}, readErr
		}
		return chatToolExecutionResult{}, fmt.Errorf("搜索子调用失败（HTTP %d）: %s", response.Status, upstreamMessagePayload(string(payloadBytes), "上游搜索请求失败"))
	}
	completed, err := consumeWebSearchStream(response.Body, progress)
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	text, sources := extractWebSearchResponse(completed)
	result := formatWebSearchToolResult(text, sources, query)
	publicResult := map[string]any{"query": query, "sourceCount": len(sources)}
	if len(sources) > 0 {
		publicResult["sources"] = sources
	}
	return chatToolExecutionResult{ModelOutput: result, PublicResult: publicResult}, nil
}

// consumeWebSearchStream 消费子调用的 SSE 流：把 reasoning/搜索动作/回答增量
// 聚合并经 progress 节流下发；返回 response.completed 事件内的完整响应对象
// （提取不到 completed 响应时返回 nil，由调用方走空结果回退）。
func consumeWebSearchStream(reader io.Reader, progress func(chatWebSearchProgress)) (map[string]any, error) {
	state := chatWebSearchProgress{Stage: "reasoning"}
	emittedStage := ""
	lastEmit := time.Time{}
	// 阶段耗时计时（契约 §6.2）：首个阶段起点为子调用流开始；progress 快照按
	// 值下发，累积 map 维护在此局部状态，emit 时深拷贝携带。
	activeStage := state.Stage
	stageStart := time.Now()
	stageTimings := map[string]int64{}
	settleStageTiming := func(next string) {
		if next == activeStage {
			return
		}
		// 多轮搜索会重复进入同一阶段（如 searching），耗时按阶段名累积。
		stageTimings[activeStage] += time.Since(stageStart).Milliseconds()
		stageStart = time.Now()
		activeStage = next
	}
	emit := func(force bool) {
		if progress == nil {
			return
		}
		if !force && time.Since(lastEmit) < time.Duration(chatWebSearchProgressIntervalMs)*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		snapshot := chatWebSearchProgress{
			Stage:     state.Stage,
			Reasoning: truncateUTF8Bytes(state.Reasoning, 2048),
			Answer:    truncateUTF8Bytes(state.Answer, 1024),
		}
		snapshot.Actions = append([]string{}, state.Actions...)
		snapshot.StageTimings = copyWebSearchStageTimings(stageTimings)
		emittedStage = state.Stage
		progress(snapshot)
	}
	var completed map[string]any
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), chatWebSearchMaxResponseBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event struct {
			Type     string         `json:"type"`
			Delta    string         `json:"delta"`
			Item     map[string]any `json:"item"`
			Response map[string]any `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch {
		case event.Type == "response.reasoning_summary_part.added" || event.Type == "response.reasoning_text_part.added":
			// 上游多段思考摘要在各段自带强调标记（如 **…**），段间不加分隔会
			// 连成「**a****b**」——新段开始前补空行分隔。
			if strings.TrimSpace(state.Reasoning) != "" && !strings.HasSuffix(state.Reasoning, "\n") {
				state.Reasoning += "\n\n"
			}
		case event.Type == "response.reasoning_summary_text.delta" || event.Type == "response.reasoning_text.delta":
			state.Reasoning += event.Delta
			state.Stage = "reasoning"
			emit(false)
		case (event.Type == "response.output_item.added" || event.Type == "response.output_item.done") && event.Item != nil && fmt.Sprint(event.Item["type"]) == "web_search_call":
			// 官方流在 added 带 action；部分中转上游（实测 shenwenai）仅在
			// done 携带完整 action（query/queries）——两个时机都提取并去重。
			state.Stage = "searching"
			if action, ok := event.Item["action"].(map[string]any); ok {
				appendWebSearchAction(&state, action)
			}
			emit(false)
		case event.Type == "response.web_search_call.in_progress" || event.Type == "response.web_search_call.searching":
			if len(state.Actions) == 0 {
				state.Actions = append(state.Actions, "联网搜索中")
			}
			state.Stage = "searching"
			emit(false)
		case event.Type == "response.output_text.delta":
			state.Answer += event.Delta
			state.Stage = "answering"
			emit(false)
		case event.Type == "response.completed":
			completed = event.Response
		case event.Type == "response.failed" || event.Type == "error":
			if completed == nil {
				completed = map[string]any{}
			}
		}
		// 阶段推进结算上一阶段耗时（先结算再下发，切换首发快照即携带）。
		settleStageTiming(state.Stage)
		// 阶段切换即时下发（不等节流窗口）。
		if state.Stage != emittedStage && progress != nil {
			emit(true)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("搜索子调用流读取失败: %w", err)
	}
	// 流结束：工具调用整体完成，进行中的阶段（通常 answering）此刻已是已完成
	// 阶段，终态快照结算其耗时（契约 §6.2/§10.3「回答 Ns」可达）。
	settleStageTiming("")
	emit(true)
	return completed, nil
}

// copyWebSearchStageTimings 深拷贝阶段耗时映射：progress 快照按值下发，
// 防止后续累积修改穿透已下发/已落库的快照。
func copyWebSearchStageTimings(timings map[string]int64) map[string]int64 {
	if timings == nil {
		return nil
	}
	copied := make(map[string]int64, len(timings))
	maps.Copy(copied, timings)
	return copied
}

// appendWebSearchAction 从 web_search_call 的 action 提取可读动作并去重追加：
// 优先 query，其次 queries 数组（官方形状），最后按 action 类型给通用标签。
func appendWebSearchAction(state *chatWebSearchProgress, action map[string]any) {
	labels := []string{}
	if q, ok := action["query"].(string); ok && strings.TrimSpace(q) != "" {
		labels = append(labels, "搜索「"+q+"」")
	} else if queries, ok := action["queries"].([]any); ok && len(queries) > 0 {
		for _, entry := range queries {
			if q, ok := entry.(string); ok && strings.TrimSpace(q) != "" {
				labels = append(labels, "搜索「"+q+"」")
			}
		}
	} else if t, ok := action["type"].(string); ok && t != "" && t != "search" {
		labels = append(labels, webSearchActionLabel(t))
	}
	for _, label := range labels {
		duplicate := false
		for _, existing := range state.Actions {
			if existing == label {
				duplicate = true
				break
			}
		}
		if !duplicate {
			state.Actions = append(state.Actions, label)
		}
	}
}

// webSearchActionLabel 把 hosted web_search 动作类型转成可读描述（非纯枚举值）。
func webSearchActionLabel(actionType string) string {
	switch strings.TrimSpace(actionType) {
	case "open_page", "open", "click":
		return "打开网页"
	case "find":
		return "页内查找"
	default:
		return "联网搜索"
	}
}

// extractWebSearchResponse 从 Responses 完整响应（非流式响应体或流式
// response.completed 内的 response 对象）提取回答文本与来源 URL：优先
// output_text 汇总字段；否则聚合 output[].content[] 的 output_text 块；
// 来源取 content 块 annotations 里的 url_citation.url（提取不到时空列表，
// 由调用方回退正文）。
func extractWebSearchResponse(payload map[string]any) (string, []string) {
	if payload == nil {
		return "", []string{}
	}
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
// 的 url 字段（去重、按首次出现顺序；命中来源黑名单的搜索页/跳转链 URL 不进
// 清单，契约 §6.2）。
func collectWebSearchCitations(payload map[string]any) []string {
	seen := map[string]bool{}
	sources := []string{}
	if payload == nil {
		return sources
	}
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
				if url == "" || seen[url] || isWebSearchBlockedSource(url) {
					continue
				}
				seen[url] = true
				sources = append(sources, url)
			}
		}
	}
	return sources
}

// webSearchSourceBlocklist 来源提取保守黑名单（契约 §6.2）：搜索引擎自身
// 搜索页与已知跳转包装（host+路径模式命中）。这类 URL 不是内容来源，进清单
// 只制造噪音；过滤只作用于来源清单（sources/sourceCount），不影响回喂主
// 模型的原始结果。
var webSearchSourceBlocklist = []struct{ host, pathPrefix string }{
	{"baidu.com", "/link"},
	{"google.com", "/search"},
	{"google.com", "/url"},
	{"bing.com", "/search"},
	{"bing.com", "/ck"},
	{"duckduckgo.com", "/l"},
}

// isWebSearchBlockedSource 判断 URL 是否命中来源黑名单：解析后的 host 剥离
// www./m. 等子域前缀并忽略 scheme/端口，host 等于域名或以其为后缀的子域
//（域名边界匹配，避免 bing.com 子串误伤 rubbing.com/webbing.com 类域名），
// 且路径按段前缀命中（path 等于前缀或以前缀加 / 开头，避免 /link 误伤
// /linkedin 类路径）。
func isWebSearchBlockedSource(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, prefix := range []string{"www.", "m."} {
		for strings.HasPrefix(host, prefix) {
			host = strings.TrimPrefix(host, prefix)
		}
	}
	path := parsed.Path
	for _, rule := range webSearchSourceBlocklist {
		if host != rule.host && !strings.HasSuffix(host, "."+rule.host) {
			continue
		}
		if path == rule.pathPrefix || strings.HasPrefix(path, rule.pathPrefix+"/") {
			return true
		}
	}
	return false
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
