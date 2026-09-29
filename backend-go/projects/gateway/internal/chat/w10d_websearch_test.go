package chat

// web_search 模型工具链路（工具体系设计 §6.1/§8.4/§11）：主对话恒
// chat_completions；目录矩阵声明 web_search 不再驱动协议偏好；未绑定触发
// tool.binding_required 引导 + 明确 tool result；已绑定经 /v1/responses 子调用
// 固定派发绑定「账户+模型」并回喂来源。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// w10dWebSearchCatalog 在 mockModelCatalog 基础上为 gpt-5 声明 responses 协议
// 的 web_search（二维矩阵）与提示缓存。
type w10dWebSearchCatalog struct{}

func (w10dWebSearchCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	return (mockModelCatalog{}).ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily)
}

func (w10dWebSearchCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	items := (mockModelCatalog{}).ListProviderCatalog(providerCode, systemAccountID)
	for i := range items {
		if items[i].Model == "gpt-5" {
			items[i].SupportedToolsByProtocol = map[string][]string{
				"chat_completions": {"function_calling"},
				"responses":        {"function_calling", "web_search"},
			}
			promptCaching := true
			items[i].SupportsPromptCaching = &promptCaching
		}
	}
	return items
}

// TestW10DStreamChatOnlyProtocol 验证主对话恒 chat_completions：目录矩阵声明
// web_search 不再把协议偏好切到 responses。
func TestW10DStreamChatOnlyProtocol(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = w10dWebSearchCatalog{}
	env.fixture.createConversation("conv_w10d_chat", routeTestOwner)
	bindStreamConversation(t, env, "conv_w10d_chat")
	env.executor.steps = []scriptStep{{
		match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("搜索完成", false))
		},
	}}
	response := env.streamPost("conv_w10d_chat", routeTestOwner, streamPayload("ws-chat-1", "查一下天气", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("恒 chat 流式 = %d %s", response.status, response.rawString())
	}
	if !strings.Contains(response.rawString(), "搜索完成") {
		t.Fatalf("缺少回答: %s", response.rawString())
	}
	for _, call := range env.executor.calls {
		if call.Path != "/v1/chat/completions" {
			t.Fatalf("主对话派发必须恒 chat_completions: %s", call.Path)
		}
	}
}

// TestW10DWebSearchBindingRequired 驱动未绑定引导：主模型调用 web_search →
// SSE tool.binding_required（含 toolId）+ 引导 tool result 回喂主模型，轮次正常完成。
func TestW10DWebSearchBindingRequired(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = w10dWebSearchCatalog{}
	env.fixture.createConversation("conv_w10d_unbound", routeTestOwner)
	bindStreamConversation(t, env, "conv_w10d_unbound")
	round := 0
	env.executor.steps = []scriptStep{
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				round++
				if round == 1 {
					return sseResponse(chatCompletionsToolSSE("call_ws_1", "web_search", `{"query":"北京天气"}`))
				}
				return sseResponse(chatCompletionsSSE("搜索工具未配置，请先在会话设置中绑定搜索模型。", false))
			},
		},
	}
	response := env.streamPost("conv_w10d_unbound", routeTestOwner, streamPayload("ws-unbound-1", "查一下天气", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("未绑定引导流式 = %d %s", response.status, response.rawString())
	}
	raw := response.rawString()
	if !strings.Contains(raw, "event: tool.binding_required") {
		t.Fatalf("缺少 tool.binding_required 事件: %s", raw)
	}
	if !strings.Contains(raw, `"toolId":"web_search"`) {
		t.Fatalf("binding_required 事件缺少 toolId: %s", raw)
	}
	// 引导 tool result 必须回喂主模型（后续轮请求体含 tool 消息与未配置文案）。
	fedBack := false
	for _, call := range env.executor.calls {
		if call.Path != "/v1/chat/completions" || !strings.Contains(call.Body, `"role":"tool"`) {
			continue
		}
		if strings.Contains(call.Body, "tool_binding_required") && strings.Contains(call.Body, "搜索工具未配置") {
			fedBack = true
		}
	}
	if !fedBack {
		t.Fatalf("引导 tool result 未回喂主模型: %+v", env.executor.calls)
	}
	if round != 2 {
		t.Fatalf("主模型轮次 = %d, want 2", round)
	}
}

// TestW10DWebSearchSubagentBound 驱动已绑定链路：主模型调用 web_search →
// 子代理经 /v1/responses 流式派发（stream + reasoning summary + hosted
// web_search）→ 过程增量经 content_block.updated 下发（item.progress）→
// response.completed 内完整响应提取结果与来源回喂主模型。
func TestW10DWebSearchSubagentBound(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = w10dWebSearchCatalog{}
	env.fixture.createConversation("conv_w10d_bound", routeTestOwner)
	bindStreamConversation(t, env, "conv_w10d_bound")
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET search_account_id = 'account-1', search_model_id = 'gpt-5' WHERE id = ?`, "conv_w10d_bound"); err != nil {
		t.Fatal(err)
	}
	subagentSSE := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"先查一下北京今天的天气。"}`,
		`data: {"type":"response.output_item.added","item":{"type":"web_search_call","id":"ws_1","action":{"type":"search","query":"北京天气"}}}`,
		`data: {"type":"response.web_search_call.in_progress","item_id":"ws_1"}`,
		`data: {"type":"response.output_text.delta","delta":"北京今日晴，26 度。"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"北京今日晴，26 度。","annotations":[{"type":"url_citation","url":"https://weather.example.com/bj"},{"type":"url_citation","url":"https://news.example.com/weather"}]}]}]}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	round := 0
	env.executor.steps = []scriptStep{
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/responses" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				var body map[string]any
				if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
					t.Fatalf("子调用请求体非法: %v", err)
				}
				if body["model"] != "gpt-5" {
					t.Fatalf("子调用 model = %v, want gpt-5", body["model"])
				}
				tools, _ := body["tools"].([]any)
				if len(tools) != 1 {
					t.Fatalf("子调用 tools = %v", body["tools"])
				}
				if tool, _ := tools[0].(map[string]any); tool["type"] != "web_search" {
					t.Fatalf("子调用 tool type = %v", tools[0])
				}
				if stream, _ := body["stream"].(bool); !stream {
					t.Fatalf("子调用必须流式（契约 §6.1）: %v", body["stream"])
				}
				if reasoning, _ := body["reasoning"].(map[string]any); reasoning["summary"] != "auto" {
					t.Fatalf("子调用 reasoning = %v, want summary=auto", body["reasoning"])
				}
				if call.Headers["x-juhe-ai-purpose"] != "chat_web_search" {
					t.Fatalf("子调用缺少 purpose 标识: %v", call.Headers)
				}
				return sseResponse(subagentSSE)
			},
		},
		{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				round++
				if round == 1 {
					return sseResponse(chatCompletionsToolSSE("call_ws_2", "web_search", `{"query":"北京天气"}`))
				}
				if !strings.Contains(call.Body, "https://weather.example.com/bj") {
					t.Fatalf("搜索结果未回喂主模型: %s", call.Body)
				}
				return sseResponse(chatCompletionsSSE("北京今日晴（来源已核对）。", false))
			},
		},
	}
	response := env.streamPost("conv_w10d_bound", routeTestOwner, streamPayload("ws-bound-1", "查一下天气", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("已绑定搜索流式 = %d %s", response.status, response.rawString())
	}
	raw := response.rawString()
	if !strings.Contains(raw, "北京今日晴") {
		t.Fatalf("缺少回答: %s", raw)
	}
	// 子代理过程增量（契约 §10.3）：思考摘要/搜索动作/阶段推进渐进下发。
	for _, fragment := range []string{
		`"stage":"searching"`, `"stage":"answering"`,
		`搜索「北京天气」`, `先查一下北京今天的天气`,
	} {
		if !strings.Contains(raw, fragment) {
			t.Fatalf("缺少子代理过程增量 %s: %s", fragment, raw)
		}
	}
	paths := map[string]bool{}
	for _, call := range env.executor.calls {
		paths[call.Path] = true
	}
	if !paths["/v1/responses"] || !paths["/v1/chat/completions"] {
		t.Fatalf("派发路径 = %v（主对话 chat + 子调用 responses 均须出现）", paths)
	}
	if strings.Contains(raw, "event: tool.binding_required") {
		t.Fatalf("已绑定时不得出现 binding_required: %s", raw)
	}
	// 终态落库保留 progress 快照（契约 §10.3）：completed 事件携带最后一次
	// 快照并入 tool_call item，terminalize 不剥离，历史回看可重建过程区。
	var persisted string
	if err := env.fixture.db.QueryRow(`SELECT content_blocks_json FROM chat_messages WHERE conversation_id = ? AND role = 'assistant' AND content_blocks_json LIKE '%web_search%'`, "conv_w10d_bound").Scan(&persisted); err != nil {
		t.Fatalf("读取落库内容块失败: %v", err)
	}
	if !strings.Contains(persisted, `"progress"`) || !strings.Contains(persisted, `"stage"`) {
		t.Fatalf("落库 tool_call item 必须携带 progress 快照: %s", persisted)
	}
	if !strings.Contains(persisted, `北京天气`) {
		t.Fatalf("落库必须保留搜索动作快照: %s", persisted)
	}
}

// TestW10DWebSearchResultExtraction 覆盖子调用响应解析与裁剪：output_text
// 汇总、annotations 来源收集、字节上限裁剪保来源清单。
func TestW10DWebSearchResultExtraction(t *testing.T) {
	payload := map[string]any{
		"output": []any{map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text", "text": "汇总正文",
				"annotations": []any{
					map[string]any{"type": "url_citation", "url": "https://a.example.com"},
					map[string]any{"type": "url_citation", "url": "https://b.example.com"},
					map[string]any{"type": "url_citation", "url": "https://a.example.com"},
				},
			}},
		}},
	}
	text, sources := extractWebSearchResponse(payload)
	if text != "汇总正文" || len(sources) != 2 || sources[0] != "https://a.example.com" {
		t.Fatalf("extract = %q %v", text, sources)
	}
	result := formatWebSearchToolResult(text, sources, "query")
	if !strings.Contains(result, "汇总正文") || !strings.Contains(result, "- https://a.example.com") {
		t.Fatalf("result = %q", result)
	}
	if len(result) > chatWebSearchResultMaxBytes {
		t.Fatalf("result 超限: %d > %d", len(result), chatWebSearchResultMaxBytes)
	}
	// 超限正文裁剪：来源清单保留、正文截断。
	long := strings.Repeat("长", 4096)
	clipped := formatWebSearchToolResult(long, []string{"https://c.example.com"}, "query")
	if len(clipped) > chatWebSearchResultMaxBytes {
		t.Fatalf("clipped 超限: %d", len(clipped))
	}
	if !strings.Contains(clipped, "https://c.example.com") {
		t.Fatalf("clipped 丢失来源: %q", clipped)
	}
	// 空结果回退文案。
	empty := formatWebSearchToolResult("", nil, "q1")
	if !strings.Contains(empty, "搜索未返回可用结果") {
		t.Fatalf("empty = %q", empty)
	}
}

// TestW10DConsumeWebSearchStream 覆盖子调用 SSE 解析器：思考摘要/搜索动作/
// 回答增量聚合、阶段推进序列、completed 权威提取与失败事件回退。
func TestW10DConsumeWebSearchStream(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_part.added","item":{"type":"reasoning"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"**想一下**"}`,
		`data: {"type":"response.reasoning_summary_part.added","item":{"type":"reasoning"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"**再确认**"}`,
		`data: {"type":"response.output_item.added","item":{"type":"web_search_call","action":{"type":"search","query":"q1"}}}`,
		`data: {"type":"response.web_search_call.searching","item_id":"ws_1"}`,
		`data: {"type":"response.output_item.added","item":{"type":"web_search_call","action":{"type":"open_page","url":"https://a.example.com"}}}`,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"q1","queries":["q1"]}}}`,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","queries":["q2","q3"]}}}`,
		`data: {"type":"response.output_text.delta","delta":"答"}`,
		`data: {"type":"response.completed","response":{"output_text":"答案全文"}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	var stages []string
	var last chatWebSearchProgress
	completed, err := consumeWebSearchStream(strings.NewReader(sse), func(p chatWebSearchProgress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
		last = p
	})
	if err != nil {
		t.Fatalf("consume err = %v", err)
	}
	wantStages := []string{"reasoning", "searching", "answering"}
	if strings.Join(stages, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("阶段序列 = %v, want %v", stages, wantStages)
	}
	if last.Reasoning != "**想一下**\n\n**再确认**" || last.Answer != "答" {
		t.Fatalf("最终快照 = %+v", last)
	}
	// 官方 added 带 query、done 复述同一 query 必须去重；中转上游仅 done 带
	// queries 数组的场景按数组逐条提取；非 search 动作给可读标签。
	wantActions := []string{"搜索「q1」", "打开网页", "搜索「q2」", "搜索「q3」"}
	if strings.Join(last.Actions, "|") != strings.Join(wantActions, "|") {
		t.Fatalf("actions = %v, want %v", last.Actions, wantActions)
	}
	if text, _ := completed["output_text"].(string); text != "答案全文" {
		t.Fatalf("completed = %v", completed)
	}
	// 失败事件（无 completed 响应）→ 空对象回退，由提取器走空结果文案。
	fallback, err := consumeWebSearchStream(strings.NewReader("data: {\"type\":\"response.failed\"}\n\n"), nil)
	if err != nil || fallback == nil {
		t.Fatalf("failed 回退 = %v err=%v", fallback, err)
	}
	if text, sources := extractWebSearchResponse(fallback); text != "" || len(sources) != 0 {
		t.Fatalf("failed 提取 = %q %v", text, sources)
	}
}

// TestW10DTerminalizeKeepsProgressSnapshot 验证过程增量随终态持久化（契约
// §10.3）：落库时保留 item.progress 最后一次快照，历史回看据此重建子代理过程区。
func TestW10DTerminalizeKeepsProgressSnapshot(t *testing.T) {
	raw := terminalizeAssistantBlocks([]*assistantBlock{{
		Type: "tool_call", BlockID: "b1", CallID: "c1", ToolType: "web_search", Status: asstStarted,
		Item: map[string]any{"query": "q", "progress": map[string]any{"stage": "searching"}},
	}}, asstCanceled)
	if !strings.Contains(string(raw), `"progress":{"stage":"searching"}`) {
		t.Fatalf("持久化块必须保留 progress 快照: %s", raw)
	}
	if !strings.Contains(string(raw), `"query":"q"`) {
		t.Fatalf("持久化块应保留终态字段: %s", raw)
	}
}

// TestW10DToolBindingsEndpointShape 验证 GET tool-bindings 的响应形状（契约
// §8.1）：model 工具携带 bound/binding/valid/candidates，code 工具仅列出。
func TestW10DToolBindingsEndpointShape(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = w10dWebSearchCatalog{}
	if env.deps.AccountOptionsLookup == nil {
		env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	}
	env.fixture.createConversation("conv_w10d_bindings", routeTestOwner)
	bindStreamConversation(t, env, "conv_w10d_bindings")
	response := env.do("GET", "/__aisys__/api/my-chat/conversations/conv_w10d_bindings/tool-bindings", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("tool-bindings = %d %s", response.status, response.rawString())
	}
	body := response.rawString()
	for _, fragment := range []string{
		`"id":"web_search"`, `"kind":"model"`, `"bound":false`,
		`"id":"generate_image"`, `"candidates":`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("tool-bindings 缺少 %s: %s", fragment, body)
		}
	}
	// 已绑定搜索后 bound=true 且携带 binding 二元组。
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET search_account_id='account-1', search_model_id='gpt-5' WHERE id='conv_w10d_bindings'`); err != nil {
		t.Fatal(err)
	}
	bound := env.do("GET", "/__aisys__/api/my-chat/conversations/conv_w10d_bindings/tool-bindings", routeTestOwner, "")
	if !strings.Contains(bound.rawString(), `"bound":true`) {
		t.Fatalf("绑定后 bound 应为 true: %s", bound.rawString())
	}
}
