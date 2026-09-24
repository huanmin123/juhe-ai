package gatewaypreauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func whNewRequest(method, target string) *GatewayRequest {
	req := httptest.NewRequest(method, target, nil)
	return &GatewayRequest{HTTP: req, ClientIP: "203.0.113.9", RemoteAddr: "203.0.113.9:443"}
}

// 已知错误处理器的 agent guidance 分支按四种协议 × JSON/SSE 渲染 200 响应体。
func TestWhKnownErrorGuidanceProtocols(t *testing.T) {
	notScoped := whBoolPtr(false)
	tests := []struct {
		name     string
		protocol GatewayAgentGuidanceProtocol
		stream   bool
		needles  []string
	}{
		{name: "messages JSON", protocol: AgentGuidanceProtocolMessages, needles: []string{`"type":"message"`, `"stop_reason":"end_turn"`, `"input_tokens":0`}},
		{name: "messages SSE", protocol: AgentGuidanceProtocolMessages, stream: true, needles: []string{"event: message_start", "event: content_block_delta", "event: message_stop"}},
		{name: "gemini JSON", protocol: AgentGuidanceProtocolGemini, needles: []string{`"finishReason":"STOP"`, `"promptTokenCount":0`, "usageMetadata"}},
		{name: "gemini SSE", protocol: AgentGuidanceProtocolGemini, stream: true, needles: []string{"candidatesTokenCount", "modelVersion"}},
		{name: "responses JSON", protocol: AgentGuidanceProtocolResponses, needles: []string{`"output_text"`, `"status":"completed"`, "gateway_guidance"}},
		{name: "responses SSE", protocol: AgentGuidanceProtocolResponses, stream: true, needles: []string{"event: response.created", "event: response.output_text.delta", "event: response.completed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _, _ := newTestService(t, nil)
			_, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
			guidance := NewGatewayAgentGuidanceResponse(GatewayAgentGuidanceResponse{
				Message: "提示文本", Code: "guidance_code", AccountScoped: notScoped,
				Protocol: tt.protocol, Stream: tt.stream, Model: "gpt-4o",
			})
			req := whNewRequest("POST", "/v1/chat/completions")
			handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
				Req: req, Res: writer, Err: guidance, AuditCapture: &fakeAuditCapture{},
			})
			if !handled {
				t.Fatal("guidance 错误必须被处理")
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("状态码 = %d", recorder.Code)
			}
			assertContains(t, recorder.Body.String(), tt.needles...)
			assertContains(t, recorder.Body.String(), "提示文本")
			if tt.stream {
				if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
					t.Fatalf("SSE Content-Type = %q", got)
				}
			}
		})
	}
}

// 模型列表前置处理：限流拦截、限流服务不可用与成功发送三条路径。
func TestWhModelsBeforeRequiredAuth(t *testing.T) {
	row := validRuntimeRow()
	buildService := func(decision AuthenticatedModelsRateLimitDecision, limit *fakeModelsRateLimit) (*Service, *fakeResponseSink) {
		service, _, sink := newTestService(t, func(s *Service) {
			s.APIKeyValidator = &fakeAPIKeyValidator{row: row}
			if limit != nil {
				s.ModelsRateLimit = limit
			} else {
				s.ModelsRateLimit = &fakeModelsRateLimit{decision: decision}
			}
		})
		return service, sink
	}

	// 1. 正常放行：发送认证模型列表响应。
	service, sink := buildService(AuthenticatedModelsRateLimitDecision{Allowed: true}, nil)
	req := whAuthorizedModelsRequest()
	_, recorder, writer := newTestRequest("GET", "/v1/models")
	completed, err := service.handleGatewayModelsRequestBeforeRequiredAuth(context.Background(), modelsBeforeAuthInput{
		req: req, res: writer, protocol: ResponseProtocolOpenAI, auditCapture: &fakeAuditCapture{},
		clientIP: "1.2.3.4", traceID: "t", endpoint: "GET /v1/models",
	})
	if err != nil || !completed {
		t.Fatalf("completed = %v err=%v", completed, err)
	}
	if len(sink.modelSends) != 1 || sink.modelSends[0].Protocol != "openai" {
		t.Fatalf("模型响应 = %+v", sink.modelSends)
	}
	if got := sink.modelSends[0].ProviderCodes; len(got) != 1 || got[0] != "openai" {
		t.Fatalf("provider codes = %v", got)
	}
	_ = recorder

	// 2. 限流拦截：429 + Retry-After 头。
	blocked := int64(9)
	service, sink = buildService(AuthenticatedModelsRateLimitDecision{Allowed: false, Scope: "api_key", RetryAfterSeconds: &blocked}, nil)
	_, recorder, writer = newTestRequest("GET", "/v1/models")
	req = whAuthorizedModelsRequest()
	completed, err = service.handleGatewayModelsRequestBeforeRequiredAuth(context.Background(), modelsBeforeAuthInput{
		req: req, res: writer, protocol: ResponseProtocolOpenAI, auditCapture: &fakeAuditCapture{},
	})
	if err != nil || !completed {
		t.Fatalf("拦截 completed = %v err=%v", completed, err)
	}
	if len(sink.failureInputs) != 1 || sink.failureInputs[0].StatusCode != 429 {
		t.Fatalf("429 失败 = %+v", sink.failureInputs)
	}
	if sink.failureInputs[0].Audit.ErrorCode != "authenticated_models_rate_limited" {
		t.Fatalf("错误码 = %q", sink.failureInputs[0].Audit.ErrorCode)
	}

	// 3. 限流服务不可用：503 + fail-closed 文案。
	unavailable := int64(5)
	service, sink = buildService(AuthenticatedModelsRateLimitDecision{Allowed: false, Unavailable: true, RetryAfterSeconds: &unavailable}, nil)
	_, recorder, writer = newTestRequest("GET", "/v1/models")
	req = whAuthorizedModelsRequest()
	completed, err = service.handleGatewayModelsRequestBeforeRequiredAuth(context.Background(), modelsBeforeAuthInput{
		req: req, res: writer, protocol: ResponseProtocolOpenAI, auditCapture: &fakeAuditCapture{},
	})
	if err != nil || !completed {
		t.Fatalf("不可用 completed = %v err=%v", completed, err)
	}
	failure := sink.failureInputs[0]
	if failure.StatusCode != 503 || failure.Audit.ErrorPhase != "security" || failure.Audit.ErrorCode != "authenticated_models_rate_limit_unavailable" {
		t.Fatalf("503 失败 = %+v", failure)
	}

	// 4. 未认证：finalize 认证失败审计并就地结束。
	service, sink = buildService(AuthenticatedModelsRateLimitDecision{Allowed: true}, nil)
	req = whNewRequest("GET", "/v1/models")
	_, recorder, writer = newTestRequest("GET", "/v1/models")
	completed, err = service.handleGatewayModelsRequestBeforeRequiredAuth(context.Background(), modelsBeforeAuthInput{
		req: req, res: writer, protocol: ResponseProtocolOpenAI, auditCapture: &fakeAuditCapture{},
	})
	if err != nil || !completed {
		t.Fatalf("未认证 completed = %v err=%v", completed, err)
	}
	if sink.authFailures != 1 || len(sink.failureInputs) != 0 {
		t.Fatalf("认证失败审计 = %d", sink.authFailures)
	}
}

// 混合路由等校验错误的 known-error 渲染（validation error 分支已覆盖一部分）。
func TestWhKnownErrorValidationPropagation(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	_, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	err := NewGatewayRequestValidationError("请求无效",
		WithValidationErrorCode("invalid_model"),
		WithValidationErrorStatusCode(422),
		WithValidationErrorType("invalid_request_error"),
		WithValidationErrorAccountScoped(),
	)
	if err.StatusCode != 422 || err.Type != "invalid_request_error" || !err.AccountScoped {
		t.Fatalf("校验错误选项 = %+v", err)
	}
	handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: whNewRequest("POST", "/v1/chat/completions"), Res: writer, Err: err, AuditCapture: &fakeAuditCapture{},
	})
	if !handled || recorder.Code != 422 {
		t.Fatalf("handled=%v code=%d", handled, recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "请求无效" {
		t.Fatalf("message = %v", errObject["message"])
	}
	// 本地协议响应分支：SSE content type 附带 Cache-Control。
	_, recorder, writer = newTestRequest("POST", "/v1/messages", )
	local := NewGatewayLocalProtocolResponse(GatewayLocalProtocolResponse{
		Message: "本地响应", Code: "local", Body: "event: done\ndata: {}\n\n",
		ContentType: "text/event-stream",
	})
	handled = service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: whNewRequest("POST", "/v1/messages"), Res: writer, Err: local, AuditCapture: &fakeAuditCapture{},
	})
	if !handled {
		t.Fatal("本地协议响应必须被处理")
	}
	if recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("SSE 缓存头 = %q", recorder.Header().Get("Cache-Control"))
	}
}

// 流式失败事件与响应写包装器的前向语义。
func TestWhStreamFailureEventsAndTrackingWriter(t *testing.T) {
	// 各协议的事件装配。
	openAI := WriteGatewayStreamFailureEvent("流失败", "code-1", GatewayErrorProtocolOpenAI, DownstreamProtocolResponsesSSE)
	if openAI == nil || !strings.Contains(string(openAI), "response.failed") || !strings.Contains(string(openAI), "code-1") {
		t.Fatalf("responses 事件 = %s", openAI)
	}
	if WriteGatewayStreamFailureEvent("m", "", GatewayErrorProtocolOpenAI, "") != nil {
		t.Fatal("无 downstream 协议的 openai 事件必须为 nil")
	}
	anthropicEvent := WriteGatewayStreamFailureEvent("流失败", "c2", GatewayErrorProtocolAnthropic, "")
	if !strings.Contains(string(anthropicEvent), "event: error") || !strings.Contains(string(anthropicEvent), "overloaded_error") {
		t.Fatalf("anthropic 事件 = %s", anthropicEvent)
	}
	geminiEvent := WriteGatewayStreamFailureEvent("流失败", "c3", GatewayErrorProtocolGemini, "")
	if !strings.Contains(string(geminiEvent), "UNAVAILABLE") {
		t.Fatalf("gemini 事件 = %s", geminiEvent)
	}
	if GatewayStreamFailureCode("any") != "upstream_stream_interrupted" {
		t.Fatal("流失败码常量错误")
	}
	// SSE 响应中写入流失败事件并结束。
	_, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(200)
	SendGatewayErrorResponse(writer, 503, GatewayErrorPayloadOf("消息", "service_unavailable", "code-9"), SendGatewayErrorResponseOptions{
		Protocol: GatewayErrorProtocolOpenAI, DownstreamProtocol: DownstreamProtocolResponsesSSE,
	})
	if !strings.Contains(recorder.Body.String(), "response.failed") || !writer.WritableEnded() {
		t.Fatalf("SSE 失败事件 = %s", recorder.Body.String())
	}
	// 已结束的响应跳过发送。
	before := recorder.Body.Len()
	SendGatewayErrorResponse(writer, 500, GatewayErrorPayloadOf("再次", "x", "y"), SendGatewayErrorResponseOptions{})
	if recorder.Body.Len() != before {
		t.Fatal("已结束响应不得再次写入")
	}
	// MarkUpstreamError / Flush 前向。
	writer.MarkUpstreamError()
	writer.Flush()
	// 非流式 content type：结束但不写事件。
	_, recorder2, writer2 := newTestRequest("POST", "/v1/chat/completions")
	writer2.Header().Set("Content-Type", "application/json")
	writer2.WriteHeader(200)
	SendGatewayErrorResponse(writer2, 500, GatewayErrorPayloadOf("JSON 后失败", "x", "y"), SendGatewayErrorResponseOptions{
		Protocol: GatewayErrorProtocolOpenAI, DownstreamProtocol: DownstreamProtocolResponsesSSE,
	})
	if recorder2.Body.Len() != 0 || !writer2.WritableEnded() {
		t.Fatal("非流式路径只结束不写事件")
	}
	if !IsOpenAIStreamContentType("Text/Event-Stream; charset=utf-8") || IsOpenAIStreamContentType("application/json") {
		t.Fatal("content type 判定语义错误")
	}
}

// 审计捕获取消的可选接口发现。
func TestWhCancelAuditCapture(t *testing.T) {
	plain := &fakeAuditCapture{}
	CancelAuditCapture(plain) // 无 cancel 接口 → 安全通过
	cancelling := &whCancellableCapture{}
	CancelAuditCapture(cancelling)
	if !cancelling.cancelled {
		t.Fatal("可取消捕获必须收到 Cancel")
	}
}

type whCancellableCapture struct{ cancelled bool }

func (c *whCancellableCapture) Cancel() { c.cancelled = true }
func (c *whCancellableCapture) BindContext(AuditGatewayContext) {}
func (c *whCancellableCapture) AddGatewayMetadata(string, map[string]any) {}
func (c *whCancellableCapture) Finalize(AuditFinalizeInput) {}

// 协议视图：驱动协议判定与原生请求识别。
func TestWhProtocolView(t *testing.T) {
	openaiReq := whNewRequest("POST", "/v1/chat/completions")
	if protocol, err := GatewayProtocolClientErrorProtocolForRequest(openaiReq); err != nil || protocol != GatewayErrorProtocolOpenAI {
		t.Fatalf("openai 协议 = %v err=%v", protocol, err)
	}
	anthropicReq := whNewRequest("POST", "/v1/messages")
	if protocol, err := GatewayProtocolClientErrorProtocolForRequest(anthropicReq); err != nil || protocol != GatewayErrorProtocolAnthropic {
		t.Fatalf("anthropic 协议 = %v err=%v", protocol, err)
	}
	geminiReq := whNewRequest("POST", "/v1beta/models/gemini-pro:generateContent")
	if protocol, err := GatewayProtocolClientErrorProtocolForRequest(geminiReq); err != nil || protocol != GatewayErrorProtocolGemini {
		t.Fatalf("gemini 协议 = %v err=%v", protocol, err)
	}
	unknown := whNewRequest("POST", "/not-registered")
	if _, err := GatewayProtocolClientErrorProtocolForRequest(unknown); err == nil || !strings.Contains(err.Error(), "未配置网关协议驱动") {
		t.Fatalf("未知协议错误 = %v", err)
	}
	if !IsGatewayProtocolNativeRequest(openaiReq, ProtocolCodeOpenAI) {
		t.Fatal("openai 路径只认 openai 协议")
	}
	if IsGatewayProtocolNativeRequest(openaiReq, ProtocolCodeGemini) {
		t.Fatal("openai 路径不认 gemini 协议")
	}
	if !IsGatewayProtocolNativeRequest(anthropicReq, "anthropic") {
		t.Fatal("anthropic 原生路径必须命中")
	}
	if !IsGatewayProtocolNativeRequest(geminiReq, ProtocolCodeGemini) {
		t.Fatal("gemini 原生路径必须命中")
	}
	if !IsGatewayModelsRequest(whNewRequest("GET", "/v1/models")) ||
		!IsGatewayModelsRequest(whNewRequest("GET", "/v1beta/models")) ||
		IsGatewayModelsRequest(whNewRequest("GET", "/v1/chat/completions")) {
		t.Fatal("模型列表请求判定语义错误")
	}
	if rawHTTPRequest(nil) != nil || rawHTTPRequest(openaiReq) != openaiReq.HTTP {
		t.Fatal("rawHTTPRequest nil 安全语义错误")
	}
	if IsOpenAIProtocolRequestPath("/v1/embeddings") != true || IsOpenAIProtocolRequestPath("/v1/audio/speech") != true {
		t.Fatal("openai 路径面覆盖 embeddings/audio")
	}
}

// 请求元数据视图：请求模型、流判定、端点、查询参数与 IP 归一化。
func TestWhRequestMetadata(t *testing.T) {
	// Gemini 路径模型优先。
	geminiReq := whNewRequest("GET", "/v1beta/models/gemini-2.0:countTokens?alt=sse")
	if model, ok := RequestModel(geminiReq); !ok || model != "gemini-2.0" {
		t.Fatalf("gemini 模型 = %q ok=%v", model, ok)
	}
	// body state 模型次之，parsed body 再次。
	stateModel := "state-model"
	withState := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{Model: &stateModel})}}
	if model, ok := RequestModel(withState); !ok || model != "state-model" {
		t.Fatalf("state 模型 = %q ok=%v", model, ok)
	}
	withBody := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{"model": "body-model"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "body-model"}})}}
	if model, _ := RequestModel(withBody); model != "body-model" {
		t.Fatalf("body 模型 = %q", model)
	}
	// 流判定：state → body → gemini 查询。
	streamTrue := true
	streaming := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{Stream: &streamTrue})}}
	if !RequestStream(streaming) {
		t.Fatal("state 流标志必须生效")
	}
	bodyStream := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{"stream": true}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"stream": true}})}}
	if !RequestStream(bodyStream) {
		t.Fatal("body stream 必须生效")
	}
	geminiInteraction := whNewRequest("GET", "/v1beta/interactions/i1?stream=true")
	if !RequestStream(geminiInteraction) {
		t.Fatal("gemini interactions 查询流必须生效")
	}
	if RequestStream(whNewRequest("GET", "/v1beta/interactions/i1")) {
		t.Fatal("无 stream 查询不得开启流")
	}
	// 端点与路径视图。
	if got := RequestEndpoint(whNewRequest("GET", "/v1/models?x=1")); got != "GET /v1/models" {
		t.Fatalf("端点 = %q", got)
	}
	// RequestURI 优先于解析 URL。
	rawReq := httptest.NewRequest("GET", "/v1/models", nil)
	rawReq.RequestURI = "/v1/models?alt=json"
	if got := (&GatewayRequest{HTTP: rawReq}).PathAndQuery(); got != "/v1/models?alt=json" {
		t.Fatalf("RequestURI = %q", got)
	}
	emptyReq := httptest.NewRequest("GET", "http://example.com", nil)
	emptyReq.RequestURI = ""
	if got := gatewayRequestPathAndQuery(emptyReq); got != "/" {
		t.Fatalf("空路径 = %q", got)
	}
	if path, query := splitPathAndQuery("/a/b?x=1&y=2"); path != "/a/b" || query != "x=1&y=2" {
		t.Fatalf("路径拆分 = %q %q", path, query)
	}
	if path, query := splitPathAndQueryTwo("/a?b?c"); path != "/a" || query != "b?c" {
		t.Fatalf("两段拆分 = %q %q", path, query)
	}
	// 查询参数首值 + 表单解码。
	if value, ok := queryParamFirstValue("a=1&b=%20x&b=2", "b"); !ok || value != " x" {
		t.Fatalf("查询首值 = %q ok=%v", value, ok)
	}
	if value, ok := queryParamFirstValue("?key=abc", "key"); !ok || value != "abc" {
		t.Fatalf("前导问号 = %q ok=%v", value, ok)
	}
	if _, ok := queryParamFirstValue("a=1", "z"); ok {
		t.Fatal("缺失键必须失败")
	}
	// 客户端 IP 归一化链。
	// 2026-09-25 起 IPv6 保留（修复生产 CF 链路 IPv6 客户端归空缺陷）。
	ipReq := &GatewayRequest{ClientIP: "[2001:db8::1]:443", RemoteAddr: "192.168.1.7:5000"}
	if ip, ok := ExtractClientIP(ipReq); !ok || ip != "2001:db8::1" {
		t.Fatalf("IPv6 保留 = %q ok=%v", ip, ok)
	}
	ipv6Only := &GatewayRequest{ClientIP: "[2001:db8::1]:443", RemoteAddr: "[2001:db8::2]:1"}
	if ip, ok := ExtractClientIP(ipv6Only); !ok || ip != "2001:db8::1" {
		t.Fatalf("全 IPv6 保留 = %q ok=%v", ip, ok)
	}
	v4Req := &GatewayRequest{RemoteAddr: "192.168.1.7:5000"}
	if ip, ok := ExtractClientIP(v4Req); !ok || ip != "192.168.1.7" {
		t.Fatalf("远端回退 = %q ok=%v", ip, ok)
	}
	mapped := &GatewayRequest{ClientIP: "::ffff:203.0.113.5"}
	if ip, ok := ExtractClientIP(mapped); !ok || ip != "203.0.113.5" {
		t.Fatalf("映射地址 = %q ok=%v", ip, ok)
	}
	if _, ok := ExtractBearerToken(""); ok {
		t.Fatal("空 Authorization 必须失败")
	}
	if token, ok := ExtractBearerToken("Bearer   sk-abc  "); !ok || token != "sk-abc" {
		t.Fatalf("Bearer 解析 = %q ok=%v", token, ok)
	}
	if _, ok := ExtractBearerToken("Basic abc"); ok {
		t.Fatal("非 Bearer 必须失败")
	}
}

// 端点模式：请求形态到账户支持模式的映射（候选过滤的输入）。
func TestWhRequestSupportedEndpointMode(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   map[string]any
		want   string
	}{
		{name: "chat json", method: "POST", target: "/v1/chat/completions", body: map[string]any{"stream": false}, want: EndpointModeChatJSON},
		{name: "chat sse", method: "POST", target: "/v1/chat/completions", body: map[string]any{"stream": true}, want: EndpointModeChatSSE},
		{name: "responses json", method: "POST", target: "/v1/responses", want: EndpointModeResponsesJSON},
		{name: "responses sse", method: "POST", target: "/v1/responses", body: map[string]any{"stream": true}, want: EndpointModeResponsesSSE},
		{name: "images", method: "POST", target: "/v1/images/generations", want: EndpointModeImagesJSON},
		{name: "messages json", method: "POST", target: "/v1/messages", want: EndpointModeMessagesJSON},
		{name: "count tokens", method: "POST", target: "/v1/messages/count_tokens", want: EndpointModeMessageTokenCounting},
		{name: "generate content json", method: "POST", target: "/v1beta/models/m:generateContent", want: EndpointModeGenerateContentJSON},
		{name: "stream generate content", method: "POST", target: "/v1beta/models/m:streamGenerateContent", want: EndpointModeGenerateContentSSE},
		{name: "countTokens", method: "POST", target: "/v1beta/models/m:countTokens", want: EndpointModeCountTokens},
		{name: "embed content", method: "POST", target: "/v1beta/models/m:embedContent", want: EndpointModeEmbedContent},
		{name: "interactions", method: "POST", target: "/v1beta/interactions/i1", want: EndpointModeInteractionsJSON},
		{name: "未匹配", method: "GET", target: "/v1/models", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &GatewayRequest{HTTP: httptest.NewRequest(tt.method, tt.target, nil)}
			if tt.body != nil {
				req.Body = &gatewaybody.Request{Body: tt.body, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: tt.body})}
			}
			if got := RequestSupportedEndpointMode(req); got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
	if RequestSupportedEndpointMode(nil) != "" {
		t.Fatal("nil 请求必须返回空模式")
	}
	if got := normalizedV1StrippedPath("/v1beta"); got != "/" {
		t.Fatalf("裸 v1beta = %q", got)
	}
}

// 编排小助手的行为面。
func TestWhOrchestrationHelpers(t *testing.T) {
	if got := defaultTrafficSource(""); got != TrafficSourceGateway {
		t.Fatalf("默认流量来源 = %q", got)
	}
	if got := defaultTrafficSource("custom"); got != "custom" {
		t.Fatalf("自定义来源 = %q", got)
	}
	row := validRuntimeRow()
	if firstNonNilRecord(row, nil) != row {
		t.Fatal("primary 优先")
	}
	if firstNonNilRecord(nil, row) != row {
		t.Fatal("fallback 兜底")
	}
	if asRecordAlias(nil) != nil {
		t.Fatal("nil 别名")
	}
	if alias := asRecordAlias(row); alias == row {
		t.Fatal("别名必须复制")
	}
	if orEmptyPolicies(nil) == nil || len(orEmptyPolicies(nil)) != 0 {
		t.Fatal("nil 策略归一化为空切片")
	}
	if got := probeModelOverride(&PreflightOptions{ForwardModelsRequestToUpstream: true, AccountProbeModel: "probe"}); got != "probe" {
		t.Fatalf("探针模型 = %q", got)
	}
	if got := probeModelOverride(&PreflightOptions{}); got != "" {
		t.Fatalf("默认探针模型 = %q", got)
	}
	if got := compactionTimeoutsDisabledTimeoutPolicy(true); got != "codex_compaction_unbounded" {
		t.Fatalf("compaction 策略 = %q", got)
	}
	if got := compactionTimeoutsDisabledTimeoutPolicy(false); got != "" {
		t.Fatalf("默认 compaction 策略 = %q", got)
	}
	if gatewayAccountRuntimeKey(gatewayruntimecache.OpenAIAccountSecret{ID: "acc"}) != "acc" {
		t.Fatal("运行键 = 账户 ID")
	}
	// onceSettle：只允许首次结算生效。
	calls := 0
	settle := onceSettle(func(string) error { calls++; return errors.New("首次错误") })
	if err := settle("a"); err == nil || calls != 1 {
		t.Fatalf("首次结算 err=%v calls=%d", err, calls)
	}
	if err := settle("b"); err != nil || calls != 1 {
		t.Fatalf("重复结算必须短路 err=%v calls=%d", err, calls)
	}
	// 记录助手。
	if recordBindingCount(nil) != 0 || recordBindingCount(row) != 1 {
		t.Fatal("绑定计数语义错误")
	}
	if recordRouteStrategyID(nil) != "" {
		t.Fatal("nil 记录策略 ID 为空")
	}
	if !apiKeyHasActiveBindingForGroup(*row, "group_1") {
		t.Fatal("激活绑定必须命中")
	}
	inactive := validRuntimeRow()
	inactive.GroupBindings[0].Status = "disabled"
	if apiKeyHasActiveBindingForGroup(*inactive, "group_1") {
		t.Fatal("非激活绑定不得命中")
	}
	if got := groupAccessProviderCode(nil); got != "" {
		t.Fatal("nil 分组访问 provider 为空")
	}
	if fields := groupUsageFieldsOr(nil, nil); fields != nil {
		t.Fatal("双 nil 字段为 nil")
	}
	if fields := groupUsageFieldsOr(nil, validRuntime().GroupAccess); fields == nil || fields.ProviderCode != "openai" {
		t.Fatalf("runtime 分组字段 = %+v", fields)
	}
	// 会话身份解析链：strategy 自带 ClientSource 优先。
	service, _, _ := newTestService(t, nil)
	strategy := ClientStrategyContext{ClientProfile: "generic"}
	if identity := resolveSessionIdentityFromStrategy(service, nil, strategy, "sys", "key"); identity.SessionID != "session_1" {
		t.Fatalf("回退身份 = %+v", identity)
	}
	carried := SessionIdentity{SessionID: "carried"}
	strategy.ClientSource = &ClientSource{SessionIdentity: &carried}
	if identity := resolveSessionIdentityFromStrategy(service, nil, strategy, "sys", "key"); identity.SessionID != "carried" {
		t.Fatalf("自带身份 = %+v", identity)
	}
	// requestContext 非nil。
	if service.requestContext() == nil {
		t.Fatal("requestContext 必须可用")
	}
}

// 模型映射投影与 anthropic 桥接延迟判定。
func TestWhModelMappingProjection(t *testing.T) {
	// 跨协议转换是 hybrid 供应商账户能力：ProviderCode 必须是 hybrid。
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc1", ProviderCode: "hybrid", ProtocolCode: "anthropic", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-4o", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: "claude-3-5-sonnet", UpstreamEndpointFamily: EndpointFamilyMessages,
			Enabled: true,
		}},
	}
	mapping := resolveOpenAIAccountModelMapping(account, "gpt-4o", EndpointFamilyChatCompletions)
	if mapping == nil || mapping.UpstreamModel != "claude-3-5-sonnet" || mapping.UpstreamEndpointFamily != EndpointFamilyMessages {
		t.Fatalf("映射 = %+v", mapping)
	}
	if mapping := resolveOpenAIAccountModelMapping(account, "", EndpointFamilyChatCompletions); mapping != nil {
		t.Fatal("空模型不得映射")
	}
	if mapping := resolveOpenAIAccountModelMapping(account, "unknown", EndpointFamilyChatCompletions); mapping != nil {
		t.Fatal("未匹配模型不得映射")
	}
	service, _, _ := newTestService(t, nil)
	// 全部候选都映射到 anthropic messages → 延迟图像工具权限到桥接。
	// 请求必须带模型提示以驱动映射解析。
	req := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{"model": "gpt-4o"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "gpt-4o"}})}}
	if !service.shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(req, []gatewayruntimecache.OpenAIAccountSecret{account}, "") {
		t.Fatal("全 anthropic 候选必须延迟")
	}
	// 非 anthropic 账户 → 不延迟。
	openaiAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "acc2", ProviderCode: "hybrid", ProtocolCode: "openai", ProtocolVersion: "v1"}
	if service.shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(req, []gatewayruntimecache.OpenAIAccountSecret{openaiAccount}, "") {
		t.Fatal("openai 候选不得延迟")
	}
	if service.shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(req, nil, "") {
		t.Fatal("空候选不得延迟")
	}
	// GET /models 请求族不参与延迟。
	if service.shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(whNewRequest("GET", "/v1/models"), []gatewayruntimecache.OpenAIAccountSecret{account}, "") {
		t.Fatal("models 请求族不得延迟")
	}
}

// 账户模型目标图像判定：模型名短路、目录协议与价格信号。
func TestWhAccountModelsTargetImage(t *testing.T) {
	catalog := []gatewayruntimecache.ProviderModelCatalogItem{{
		ProviderCode: "openai", Model: "catalog-model",
		SupportedAPIProtocols: []string{"images"},
	}}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{catalog: catalog}
	})
	req := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{"model": "gpt-image-1"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "gpt-image-1"}})}}
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "a", ProviderCode: "openai"}}
	if isImage, err := service.accountModelsTargetImage(context.Background(), req, accounts, "sys"); err != nil || !isImage {
		t.Fatalf("图像模型 = %v err=%v", isImage, err)
	}
	// 目录命中：输出模态判定。
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{catalog: []gatewayruntimecache.ProviderModelCatalogItem{{
			ProviderCode: "openai", Model: "catalog-model",
			OutputModalities: []string{"image"},
		}}}
	})
	plainReq := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{"model": "catalog-model"}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{"model": "catalog-model"}})}}
	if isImage, err := service2.accountModelsTargetImage(context.Background(), plainReq, accounts, "sys"); err != nil || !isImage {
		t.Fatalf("目录图像模态 = %v err=%v", isImage, err)
	}
	// 目录未命中 → 非图像。
	service3, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{catalog: []gatewayruntimecache.ProviderModelCatalogItem{{
			ProviderCode: "openai", Model: "catalog-model",
			OutputModalities: []string{"text"},
		}}}
	})
	if isImage, err := service3.accountModelsTargetImage(context.Background(), plainReq, accounts, "sys"); err != nil || isImage {
		t.Fatalf("目录未命中 = %v err=%v", isImage, err)
	}
	// 无模型 → false。
	emptyReq := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil), Body: &gatewaybody.Request{Body: map[string]any{}, State: gatewaybody.CreateBodyState(gatewaybody.BodyStateInput{JSONParseStatus: gatewaybody.JSONParseStatusParsed, ParsedBody: map[string]any{}})}}
	if isImage, err := service.accountModelsTargetImage(context.Background(), emptyReq, accounts, "sys"); err != nil || isImage {
		t.Fatalf("空模型 = %v err=%v", isImage, err)
	}
	if isImage, err := service.accountModelsTargetImage(context.Background(), emptyReq, nil, "sys"); err != nil || isImage {
		t.Fatalf("空账户 = %v err=%v", isImage, err)
	}
}

// whAuthorizedModelsRequest 构造携带 Bearer 的模型列表请求。
func whAuthorizedModelsRequest() *GatewayRequest {
	req := whNewRequest("GET", "/v1/models")
	req.HTTP.Header.Set("Authorization", "Bearer sk-good")
	return req
}
