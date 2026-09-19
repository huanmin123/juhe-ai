package gatewayopenai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
)

// Driver 身份与协议面（G02 编排层契约）。
func TestWAOpenAIDriverIdentity(t *testing.T) {
	driver := NewDriver()
	if driver.ID() != "openai-v1" {
		t.Fatalf("ID = %q", driver.ID())
	}
	if driver.ProtocolCode() != "openai" || driver.ProtocolVersion() != "v1" {
		t.Fatalf("协议 = %q/%q", driver.ProtocolCode(), driver.ProtocolVersion())
	}
	if driver.ResponseProtocol() != "openai_v1" {
		t.Fatalf("ResponseProtocol = %q", driver.ResponseProtocol())
	}
	if driver.ClientErrorProtocol() != "openai" || driver.DefaultClientProfile() != "generic_openai" {
		t.Fatalf("错误协议/默认画像 = %q/%q", driver.ClientErrorProtocol(), driver.DefaultClientProfile())
	}
	profile := gatewayproto.ProtocolProfile{ProtocolCode: " OpenAI ", ProtocolVersion: "V1"}
	if !driver.SupportsProfile(profile) {
		t.Fatal("归一化后应支持 openai/v1 档位")
	}
	profile.ProtocolVersion = "v2"
	if driver.SupportsProfile(profile) {
		t.Fatal("版本不符不应支持")
	}
	if !driver.MatchPath(gatewayproto.RequestShape{OriginalPathAndQuery: "/v1/chat/completions?x=1"}) {
		t.Fatal("chat completions 应命中")
	}
	if !driver.MatchPath(gatewayproto.RequestShape{Path: "/v1/models"}) {
		t.Fatal("OriginalPathAndQuery 缺省回退 Path")
	}
	if driver.MatchPath(gatewayproto.RequestShape{Path: "/other"}) {
		t.Fatal("非协议路径不命中")
	}
	if driver.NewStreamInspector() == nil {
		t.Fatal("NewStreamInspector 不应为 nil")
	}
	registry := DefaultRegistry()
	if registry == nil {
		t.Fatal("DefaultRegistry 不应为 nil")
	}
}

// Driver usage / 错误负载门面。
func TestWAOpenAIDriverUsageAndErrors(t *testing.T) {
	driver := NewDriver()
	body := []byte(`{"service_tier":"flex","usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	waOAssertToken(t, driver.ExtractUsageFromJSONBuffer(body).InputTokens, 2, "buffer")
	waOAssertToken(t, driver.ExtractUsageFromJSONValue(mustParseJSON(t, string(body))).OutputTokens, 1, "value")
	waOAssertToken(t, driver.ExtractUsageFromJSONTextFragment(string(body)).InputTokens, 2, "fragment")
	payload := driver.ParseErrorPayload(`{"error":{"code":"c","message":"m"}}`, nil)
	if payload.Code != "c" || payload.Message != "m" {
		t.Fatalf("错误负载 = %+v", payload)
	}
}

// BuildUpstreamRequest：无映射直通、同族映射改写模型、跨协议桥、错误路径。
func TestWAOpenAIDriverBuildUpstreamRequest(t *testing.T) {
	t.Run("无映射直通", func(t *testing.T) {
		driver := NewDriver()
		input := gatewayproto.BuildUpstreamRequestInput{
			Method:             "",
			ClientPathAndQuery: "/v1/chat/completions?k=1",
			Body:               []byte(`{"model":"gpt-x","stream":true,"messages":[]}`),
			UpstreamBaseURL:    "https://up.example",
			Header:             http.Header{"X-Test": {"1"}},
		}
		result, err := driver.BuildUpstreamRequest(input)
		if err != nil {
			t.Fatalf("BuildUpstreamRequest error: %v", err)
		}
		if result.Method != http.MethodPost {
			t.Fatalf("缺省方法 = %q", result.Method)
		}
		if result.URL != "https://up.example/v1/chat/completions?k=1" {
			t.Fatalf("URL = %q", result.URL)
		}
		if result.Stream != true || result.EndpointMode != gatewayproto.EndpointModeChatSSE {
			t.Fatalf("流式 = %v/%q", result.Stream, result.EndpointMode)
		}
		if result.Lane != gatewayproto.LaneText {
			t.Fatalf("通道 = %q", result.Lane)
		}
		if result.UpstreamModel != "gpt-x" {
			t.Fatalf("上游模型 = %q", result.UpstreamModel)
		}
		if result.Header.Get("X-Test") != "1" {
			t.Fatalf("header 未复制: %v", result.Header)
		}
	})
	// 同族映射见 TestWAOpenAIDriverBuildUpstreamRequestMappingSameFamily，
	// 跨协议桥见 TestWAOpenAIDriverBuildUpstreamRequestBridge。
}

func TestWAOpenAIDriverBuildUpstreamRequestMappingSameFamily(t *testing.T) {
	driver := NewDriver()
	input := gatewayproto.BuildUpstreamRequestInput{
		ClientPathAndQuery: "/v1/chat/completions",
		Body:               []byte(`{"model":"client-model","messages":[{"role":"user","content":"hi"}]}`),
		UpstreamBaseURL:    "https://up.example",
		ModelMapping: &gatewayproto.ResolvedModelMapping{
			SourceModel:            "client-model",
			SourceEndpointFamily:   FamilyChatCompletions,
			UpstreamModel:          "upstream-model",
			UpstreamEndpointFamily: FamilyChatCompletions,
		},
	}
	result, err := driver.BuildUpstreamRequest(input)
	if err != nil {
		t.Fatalf("BuildUpstreamRequest error: %v", err)
	}
	if result.UpstreamModel != "upstream-model" {
		t.Fatalf("UpstreamModel = %q", result.UpstreamModel)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("转换后 body 非法: %v", err)
	}
	if body["model"] != "upstream-model" {
		t.Fatalf("body model = %v，期望覆写", body["model"])
	}
	if _, has := body["messages"]; !has {
		t.Fatal("其余字段保留")
	}
	if result.PathAndQuery != "/v1/chat/completions" {
		t.Fatalf("同族映射不改路径: %q", result.PathAndQuery)
	}
}

// 跨协议桥：chat → anthropic messages（混合账户）。
func TestWAOpenAIDriverBuildUpstreamRequestBridge(t *testing.T) {
	driver := NewDriver()
	input := gatewayproto.BuildUpstreamRequestInput{
		ClientPathAndQuery: "/v1/chat/completions?beta=1",
		Body:               []byte(`{"model":"client-model","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`),
		UpstreamBaseURL:    "https://up.example",
		ModelMapping: &gatewayproto.ResolvedModelMapping{
			SourceModel:            "client-model",
			SourceEndpointFamily:   FamilyChatCompletions,
			UpstreamModel:          "claude-up",
			UpstreamEndpointFamily: FamilyAnthropicMessages,
		},
		ParsedBodyAvailable: false,
	}
	result, err := driver.BuildUpstreamRequest(input)
	if err != nil {
		t.Fatalf("BuildUpstreamRequest error: %v", err)
	}
	if result.PathAndQuery != "/messages?beta=1" {
		t.Fatalf("桥目标路径 = %q", result.PathAndQuery)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("桥 body 非法: %v", err)
	}
	if body["model"] != "claude-up" {
		t.Fatalf("桥 model = %v", body["model"])
	}
	if body["max_tokens"] == nil {
		t.Fatal("max_tokens 应由桥推导")
	}
	if result.EndpointMode != gatewayproto.EndpointModeChatJSON {
		t.Fatalf("端点模式 = %q", result.EndpointMode)
	}
}

// 桥错误语义：guidance 原样透传、BridgeRequestError 归一转换、未知错误。
func TestWAOpenAIBridgeBuildError(t *testing.T) {
	guidance := &openaicompatbridge.BridgeGuidanceError{Message: "guidance", Code: "agent_guidance"}
	if got := bridgeBuildError(guidance); got != error(guidance) {
		t.Fatalf("guidance 错误应原样透传: %v", got)
	}
	bridgeErr := bridgeBuildError(&openaicompatbridge.BridgeRequestError{Message: "负载无效", Code: "bad"})
	var buildErr *gatewayproto.BuildUpstreamError
	if !errors.As(bridgeErr, &buildErr) {
		t.Fatalf("BridgeRequestError 应转为 BuildUpstreamError: %v", bridgeErr)
	}
	if buildErr.Code != gatewayproto.ErrCodeUnsupportedModelMappingConversion || buildErr.Message != "负载无效" {
		t.Fatalf("转换错误 = %+v", buildErr)
	}
	unknown := bridgeBuildError(errors.New("boom"))
	if !errors.As(unknown, &buildErr) || buildErr.Message == "boom" {
		t.Fatalf("未知错误应转为固定文案: %+v", buildErr)
	}
	// errBridgeBodyNotObject 错误值文本。
	if got := errBridgeBodyNotObject.Error(); got != "bridge body must be a JSON object" {
		t.Fatalf("桥 body 错误文本 = %q", got)
	}
	if _, err := unmarshalJSONObject([]byte(`[1]`)); err == nil {
		t.Fatal("非对象桥 body 应报错")
	}
	if _, err := unmarshalJSONObject([]byte(`{bad`)); err == nil {
		t.Fatal("非法 JSON 桥 body 应报错")
	}
	obj, err := unmarshalJSONObject([]byte(`{"a":1}`))
	if err != nil || obj["a"] == nil {
		t.Fatalf("合法对象应解析: %+v/%v", obj, err)
	}
}

// 模型映射请求体约束：必须为 JSON 对象。
func TestWABuildModelMappedJSONBody(t *testing.T) {
	root := mustParseJSON(t, `{"model":"a","x":1}`)
	encoded, err := buildModelMappedJSONBody(root, nil, "b")
	if err != nil {
		t.Fatalf("对象 body 报错: %v", err)
	}
	var next map[string]any
	if err := json.Unmarshal(encoded, &next); err != nil || next["model"] != "b" {
		t.Fatalf("覆写后 = %s/%v", encoded, err)
	}
	cases := []struct {
		name    string
		rawBody []byte
	}{
		{"空 body", []byte("")},
		{"空白 body", []byte("   ")},
		{"数组 body", []byte(`[1,2]`)},
		{"非法 JSON", []byte(`{bad`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildModelMappedJSONBody(nil, tc.rawBody, "b"); err == nil {
				t.Fatal("非对象 body 应报错")
			}
		})
	}
	// jsonValid 直接验证。
	if !jsonValid(`{"a":1}`) || jsonValid(`{bad`) {
		t.Fatal("jsonValid 语义错误")
	}
	// parseJSONBodyBytes：event: 前缀与空 body 不解析。
	if parseJSONBodyBytes([]byte("event: x")) != nil || parseJSONBodyBytes([]byte("  ")) != nil {
		t.Fatal("event/空 body 应返回 nil")
	}
	if parseJSONBodyBytes([]byte(`{"a":1}`)) == nil {
		t.Fatal("合法 JSON 应解析")
	}
	// ParsedBody 未提供时从 Body 解析。
	driver := NewDriver()
	input := gatewayproto.BuildUpstreamRequestInput{
		ClientPathAndQuery: "/v1/chat/completions",
		Body:               []byte(`{"model":"m"}`),
	}
	result, err := driver.BuildUpstreamRequest(input)
	if err != nil {
		t.Fatalf("BuildUpstreamRequest error: %v", err)
	}
	if result.UpstreamModel != "m" {
		t.Fatalf("从 body 提取模型 = %q", result.UpstreamModel)
	}
}

// InspectResponse：缓冲响应的协议完成证据与语义成功判定。
func TestWAOpenAIDriverInspectResponse(t *testing.T) {
	driver := NewDriver()
	t.Run("chat 成功", func(t *testing.T) {
		inspection := driver.InspectResponse(gatewayproto.InspectResponseInput{
			Body:         []byte(`{"choices":[{"message":{"content":"答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
			RequestShape: gatewayproto.RequestShape{Path: "/v1/chat/completions"},
		})
		if !inspection.ProtocolComplete || !inspection.SemanticSuccess {
			t.Fatalf("chat 巡检 = %+v", inspection)
		}
		if inspection.FinishReason != "stop" || inspection.Status != "stop" {
			t.Fatalf("完成帧 = %+v", inspection)
		}
		if !inspection.OutputReceived {
			t.Fatal("应有可见输出")
		}
		waOAssertToken(t, inspection.Usage.OutputTokens, 2, "output")
	})
	t.Run("responses failed 状态", func(t *testing.T) {
		inspection := driver.InspectResponse(gatewayproto.InspectResponseInput{
			Body:         []byte(`{"status":"failed","error":{"code":"e","message":"m"}}`),
			RequestShape: gatewayproto.RequestShape{Path: "/v1/responses"},
		})
		if inspection.ProtocolComplete != true || inspection.SemanticSuccess {
			t.Fatalf("failed 巡检 = %+v", inspection)
		}
		if !inspection.Failed || inspection.ErrorCode != "e" {
			t.Fatalf("failed 巡检错误 = %+v", inspection)
		}
	})
	t.Run("incomplete 状态语义失败", func(t *testing.T) {
		inspection := driver.InspectResponse(gatewayproto.InspectResponseInput{
			Body:         []byte(`{"status":"incomplete","output":[]}`),
			RequestShape: gatewayproto.RequestShape{Path: "/v1/responses"},
		})
		if inspection.SemanticSuccess || !inspection.Failed {
			t.Fatalf("incomplete 巡检 = %+v", inspection)
		}
	})
	t.Run("错误负载", func(t *testing.T) {
		inspection := driver.InspectResponse(gatewayproto.InspectResponseInput{
			Body:         []byte(`{"error":{"code":"quota","message":"额度不足"}}`),
			RequestShape: gatewayproto.RequestShape{Path: "/v1/chat/completions"},
		})
		if !inspection.Failed || inspection.ErrorCode != "quota" || inspection.ErrorMessage != "额度不足" {
			t.Fatalf("错误巡检 = %+v", inspection)
		}
	})
	t.Run("非 JSON body", func(t *testing.T) {
		inspection := driver.InspectResponse(gatewayproto.InspectResponseInput{
			Body:         []byte("plain"),
			RequestShape: gatewayproto.RequestShape{Path: "/v1/chat/completions"},
		})
		if inspection.ProtocolComplete || inspection.SemanticSuccess || inspection.Failed {
			t.Fatalf("非 JSON 巡检 = %+v", inspection)
		}
	})
}

// InspectStream 的接口等价性（gatewayproto.StreamInspector 核心方法面）。
func TestWAOpenAIDriverStreamInspectorInterface(t *testing.T) {
	var inspector gatewayproto.StreamInspector = NewStreamInspector()
	inspector.PushChunk([]byte("data: [DONE]\n\n"))
	snapshot := inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("接口路径 [DONE] = %+v", snapshot)
	}
	if !inspector.DrainEventSummariesCanEndStream() {
		t.Fatal("接口 summary 应可结束流")
	}
	if inspector.DrainEventSummariesCanEndStream() {
		t.Fatal("drain 后应为 false")
	}
}

// httptest 上游 SSE 集成：分块读取 + 巡检缓冲逐事件转发与拦截。
func TestWAOpenAIBufferWithUpstreamSSE(t *testing.T) {
	const upstreamBody = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"id\":\"c2\",\"choices\":[{\"delta\":{\"content\":\"答\"}}]}\n\n" +
		"data: {\"id\":\"c3\",\"error\":{\"code\":\"blocked\",\"message\":\"拦截\"}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("请求上游失败: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	var policyCalls int
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, frames []gatewayproto.SemanticFrame) *InspectionDecision {
			policyCalls++
			if event.Data != nil {
				if _, hasError := event.Data["error"]; hasError {
					return &InspectionDecision{Action: DecisionIntercept, ErrorCode: "policy_blocked", Message: "策略拦截"}
				}
			}
			return nil
		}},
	})

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取上游失败: %v", err)
	}
	// 按事件边界拆分喂入（确定性拆分：以 \n\n 为界）。
	var forwarded []byte
	for _, event := range strings.SplitAfter(string(raw), "\n\n") {
		if event == "" {
			continue
		}
		result := buffer.PushChunk([]byte(event))
		for _, chunk := range result.Chunks {
			forwarded = append(forwarded, chunk...)
		}
		if result.Intercepted != nil {
			break
		}
	}
	if policyCalls < 2 {
		t.Fatalf("策略调用次数 = %d", policyCalls)
	}
	if !strings.Contains(string(forwarded), "c1") || !strings.Contains(string(forwarded), "c2") {
		t.Fatalf("前两个事件应转发: %q", forwarded)
	}
	if strings.Contains(string(forwarded), "c3") {
		t.Fatalf("错误事件应被拦截丢弃: %q", forwarded)
	}
	if !strings.Contains(string(forwarded), "response.failed") || !strings.Contains(string(forwarded), "policy_blocked") {
		t.Fatalf("应注入失败事件: %q", forwarded)
	}
}
