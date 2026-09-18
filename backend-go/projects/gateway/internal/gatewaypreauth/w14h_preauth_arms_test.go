package gatewaypreauth

// w14h 覆盖波次：metadata/endpointmode/protocol 纯函数分支、responses 写出臂、
// retry budget 观察者臂、Service 审计旁路辅助与 ResolveGatewayRuntime(Async)
// 的认证失败臂。全部为进程内 fake，无真实网络。
//
// 已知不可达语句（登记备查）：
//   - responses.go BuildChatCompletions/BuildGateway/BuildAnthropic/
//     BuildGeminiGatewayStreamFailureEvent 的 json.Marshal 错误臂（398-400、
//     427-429、437-439、447-449）：入参均为 map/payload 结构体，恒可序列化。
//   - errorresponse.go writeGuidanceJSON/mustJSON 的 json.Marshal 错误臂
//     （282-284、589-591）：入参均为 map[string]any，恒可序列化。
//   - service.go newAuditID 的 rand.Read 错误臂（121-123）：crypto/rand 在
//     常规进程内不失败。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

var w14hBoomErr = errors.New("w14h boom")

func TestW14HAbortAndTimeHelperArms(t *testing.T) {
	if GatewayDiagnosticAbortSourceFromSignal("", nil) != AbortSourceServerDiagnosticCancel {
		t.Fatal("空信号必须归类为 cancel")
	}
	if !isTimeoutLikeAbortReason("context deadline exceeded", nil) {
		t.Fatal("deadline 文案必须命中")
	}
	if isTimeoutLikeAbortReason("", nil) {
		t.Fatal("nil err 必须返回 false")
	}
	if !isTimeoutLikeAbortReason("", context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded 错误必须命中")
	}
	if !isTimeoutLikeAbortReason("", &w14hTimeoutError{}) {
		t.Fatal("Timeout() 错误必须命中")
	}
	// gatewaybodyDecodeJSON / RFC3339 辅助。
	if _, err := gatewaybodyDecodeJSON([]byte("w14h-not-json")); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if _, ok := gatewayruntimecacheRFC3339Millis("w14h-not-a-time"); ok {
		t.Fatal("非法时间必须失败")
	}
	if _, ok := gatewayruntimecacheRFC3339Millis("2026-09-01T08:00:00.250Z"); !ok {
		t.Fatal("合法时间必须解析")
	}
	if _, err := timeParseRFC3339Instant("2026-13-45T99:99:99Z"); err == nil {
		t.Fatal("日历非法时间必须报错")
	}
	if _, err := timeParseRFC3339Instant("2026-09-01 08:00:00"); err == nil {
		t.Fatal("裸日期必须报错")
	}
}

type w14hTimeoutError struct{}

func (*w14hTimeoutError) Error() string { return "w14h timeout" }
func (*w14hTimeoutError) Timeout() bool { return true }

func TestW14HMetadataArms(t *testing.T) {
	// normalizeClientIP：空值。
	if _, ok := normalizeClientIP(""); ok {
		t.Fatal("空 IP 必须失败")
	}
	// requestModelFromGeminiPath：空路径回退 / 无匹配 / 非法转义。
	if _, ok := requestModelFromGeminiPath("?a=1", "/v1beta/models/gem"); ok {
		t.Fatal("回退路径无模型必须失败")
	}
	if _, ok := requestModelFromGeminiPath("/v1beta/models", ""); ok {
		t.Fatal("无模型段必须失败")
	}
	model, ok := requestModelFromGeminiPath("/v1beta/models/gem-2.5:generateContent?x=1", "")
	if !ok || model != "gem-2.5" {
		t.Fatalf("模型解析=%q/%v", model, ok)
	}
	raw, ok := requestModelFromGeminiPath("/v1beta/models/%zz:generateContent", "")
	if !ok || raw != "%zz" {
		t.Fatalf("非法转义模型=%q/%v", raw, ok)
	}
	// RequestStream：body state 优先，其次解析体，再 Gemini 查询。
	stream := true
	withState := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat", nil), Body: &gatewaybody.Request{State: &gatewaybody.BodyState{Stream: &stream}}}
	if !RequestStream(withState) {
		t.Fatal("body state stream=true 必须命中")
	}
	plainGET := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat", nil))
	if RequestStream(plainGET) {
		t.Fatal("普通 POST 不应命中流式")
	}
	geminiGET := NewGatewayRequest(httptest.NewRequest("GET", "/v1beta/interactions/abc?stream=true", nil))
	if !RequestStream(geminiGET) {
		t.Fatal("Gemini interactions stream 查询必须命中")
	}
	nonGET := NewGatewayRequest(httptest.NewRequest("POST", "/v1beta/interactions/abc?stream=true", nil))
	if requestGeminiInteractionStreamQuery(nonGET) {
		t.Fatal("非 GET 必须失败")
	}
	wrongPath := NewGatewayRequest(httptest.NewRequest("GET", "/v1/chat?stream=true", nil))
	if requestGeminiInteractionStreamQuery(wrongPath) {
		t.Fatal("非 interactions 路径必须失败")
	}
	noParam := NewGatewayRequest(httptest.NewRequest("GET", "/v1beta/interactions/abc", nil))
	if requestGeminiInteractionStreamQuery(noParam) {
		t.Fatal("缺 stream 参数必须失败")
	}
	// RequestEndpoint：PathAndQuery 为空时回退 Path。
	empty := &GatewayRequest{HTTP: nil}
	if got := RequestEndpoint(empty); got != " /" {
		t.Fatalf("nil 请求端点=%q", got)
	}
	// queryParamFirstValue：非法转义回退原文 / + 号转空格 / 缺键。
	if value, ok := queryParamFirstValue("a=%zz", "a"); !ok || value != "%zz" {
		t.Fatalf("非法转义值=%q/%v", value, ok)
	}
	if value, ok := queryParamFirstValue("%zz=1", "b"); ok {
		t.Fatalf("非法转义键不应命中=%q", value)
	}
	if value, ok := queryParamFirstValue("a=hello+world", "a"); !ok || value != "hello world" {
		t.Fatalf("+ 号=%q", value)
	}
	if _, ok := queryParamFirstValue("a=1", "b"); ok {
		t.Fatal("缺键必须失败")
	}
	// requestPathOnly。
	if got := requestPathOnly(NewGatewayRequest(httptest.NewRequest("GET", "/x/y?z=1", nil))); got != "/x/y" {
		t.Fatalf("requestPathOnly=%q", got)
	}
}

func TestW14HEndpointModeArms(t *testing.T) {
	modeRequest := func(method, target string, stream bool) *GatewayRequest {
		request := httptest.NewRequest(method, target, nil)
		body := &gatewaybody.Request{}
		if stream {
			flag := true
			body.State = &gatewaybody.BodyState{Stream: &flag}
		}
		return &GatewayRequest{HTTP: request, Body: body}
	}
	cases := []struct {
		method, target string
		stream         bool
		want           string
	}{
		{"POST", "/v1/messages", true, EndpointModeMessagesSSE},
		{"POST", "/v1/messages", false, EndpointModeMessagesJSON},
		{"POST", "/v1/messages/count_tokens", false, EndpointModeMessageTokenCounting},
		{"POST", "/v1beta/models/gem:generateContent", true, EndpointModeGenerateContentSSE},
		{"POST", "/v1beta/models/gem:generateContent", false, EndpointModeGenerateContentJSON},
		{"POST", "/v1beta/models/gem:streamGenerateContent", false, EndpointModeGenerateContentSSE},
		{"GET", "/v1beta/models/gem:countTokens", false, EndpointModeCountTokens},
		{"POST", "/v1beta/models/gem:embedContent", false, EndpointModeEmbedContent},
		{"POST", "/v1beta/interactions/abc", true, EndpointModeInteractionsSSE},
		{"POST", "/v1beta/interactions/abc", false, EndpointModeInteractionsJSON},
		{"POST", "/v1/chat/completions", false, EndpointModeChatJSON},
		{"POST", "/v1/responses", true, EndpointModeResponsesSSE},
		{"POST", "/v1/images/generations", false, EndpointModeImagesJSON},
		{"GET", "/v1/models", false, ""},
	}
	for _, tc := range cases {
		if got := RequestSupportedEndpointMode(modeRequest(tc.method, tc.target, tc.stream)); got != tc.want {
			t.Fatalf("%s %s stream=%v = %q（期望 %q）", tc.method, tc.target, tc.stream, got, tc.want)
		}
	}
	// RequestPathWithoutQuery 分支。
	if got := RequestPathWithoutQuery(&GatewayRequest{HTTP: nil}); got != "/" {
		t.Fatalf("nil 请求=%q", got)
	}
	relative := httptest.NewRequest("GET", "/x", nil)
	relative.RequestURI = "relative/path?a=1"
	if got := RequestPathWithoutQuery(&GatewayRequest{HTTP: relative}); got != "/relative/path" {
		t.Fatalf("相对路径=%q", got)
	}
}

func TestW14HProtocolAndImageArms(t *testing.T) {
	// isExplicitAnthropicModelsClient。
	anthropicClient := NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	anthropicClient.HTTP.Header.Set(GatewayClientProfileHeader, "claude_code")
	if !isExplicitAnthropicModelsClient(anthropicClient) {
		t.Fatal("claude_code profile 必须命中")
	}
	versionClient := NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	versionClient.HTTP.Header.Set("anthropic-version", "2023-06-01")
	if !isExplicitAnthropicModelsClient(versionClient) {
		t.Fatal("anthropic-version 必须命中")
	}
	plainClient := NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	if isExplicitAnthropicModelsClient(plainClient) {
		t.Fatal("普通客户端不应命中")
	}
	// IsGatewayProtocolNativeRequest。
	openaiPath := NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	if !IsGatewayProtocolNativeRequest(openaiPath, ProtocolCodeOpenAI) {
		t.Fatal("openai 路径 + openai 协议必须命中")
	}
	if IsGatewayProtocolNativeRequest(openaiPath, "w14h-other") {
		t.Fatal("openai 路径 + 其他协议不应命中")
	}
	// imagepermission：model 提示（body 优先 / 状态回退 / 均缺省）。
	parsedBody := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat", nil), Body: &gatewaybody.Request{Body: map[string]any{"model": "gpt-image-1"}}}
	if got := requestModelHint(parsedBody); got != "gpt-image-1" {
		t.Fatalf("解析体模型提示=%q", got)
	}
	stateModel := "dall-e-3"
	stateBody := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat", nil), Body: &gatewaybody.Request{State: &gatewaybody.BodyState{Model: &stateModel}}}
	if got := requestModelHint(stateBody); got != "dall-e-3" {
		t.Fatalf("状态模型提示=%q", got)
	}
	if got := requestModelHint(NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat", nil))); got != "" {
		t.Fatalf("无 body 模型提示=%q", got)
	}
	imagesPath := NewGatewayRequest(httptest.NewRequest("POST", "/v1/images/generations", nil))
	if !IsOpenAIGatewayImageEndpointOrModelRequest(imagesPath) {
		t.Fatal("images 路径必须命中")
	}
	chatPath := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if IsOpenAIGatewayImageEndpointOrModelRequest(chatPath) {
		t.Fatal("chat 路径不应命中")
	}
}

func TestW14HResponseWriterAndPayloadArms(t *testing.T) {
	// containsFoldASCII：空 needle 恒真。
	if !containsFoldASCII("anything", "") {
		t.Fatal("空 needle 必须为真")
	}
	// TrackingWriter.Write：未写头时状态按 200 处理。
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	if _, err := writer.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	if writer.StatusCode() != http.StatusOK || !writer.HeadersSent() {
		t.Fatalf("默认状态=%d headersSent=%v", writer.StatusCode(), writer.HeadersSent())
	}
	// MarkUpstreamError：非 marker writer 安全。
	writer.MarkUpstreamError()
	// Flush：非 flusher 安全。
	writer.Flush()
	// LocalizedGatewayErrorPayload：消息已本地化时原样返回。
	payload := GatewayErrorPayloadOf("请求失败", "rate_limit_error")
	same := LocalizedGatewayErrorPayload(payload, http.StatusTooManyRequests)
	if same.Error.Message != payload.Error.Message {
		t.Fatalf("本地化消息=%q", same.Error.Message)
	}
	// MarshalJSON：code 与 extra 序列化。
	withCode := GatewayErrorPayloadOf("msg", "type", "code_x")
	withCode.Extra = map[string]any{"trace": "t1"}
	encoded, err := json.Marshal(withCode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "code_x") || !strings.Contains(string(encoded), "trace") {
		t.Fatalf("payload=%s", encoded)
	}
	// GatewayErrorPayloadForProtocol：anthropic/gemini 形状。
	anthropic := GatewayErrorPayloadForProtocol(withCode, GatewayErrorProtocolAnthropic)
	if anthropicMap, ok := anthropic.(map[string]any); !ok || anthropicMap["type"] != "error" {
		t.Fatalf("anthropic 形状=%v", anthropic)
	}
	gemini := GatewayErrorPayloadForProtocol(withCode, GatewayErrorProtocolGemini)
	if geminiMap, ok := gemini.(map[string]any); !ok || geminiMap["type"] != nil && geminiMap["error"] != nil {
		t.Fatalf("gemini 形状=%v", gemini)
	}
	// SendGatewayJSONError：保留上游消息分支。
	errRecorder := httptest.NewRecorder()
	errWriter := NewTrackingWriter(errRecorder)
	SendGatewayJSONError(errWriter, http.StatusBadGateway, GatewayErrorPayloadOf("upstream boom", "upstream_error"), SendGatewayErrorOptions{PreserveUpstreamErrorMessage: true})
	if errRecorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码=%d", errRecorder.Code)
	}
	// 流失败事件：带 code 与默认 code。
	if event := BuildGatewayStreamFailureEvent("boom", "custom_code"); !strings.Contains(string(event), "custom_code") {
		t.Fatalf("自定义 code 事件=%s", event)
	}
	if event := BuildGatewayStreamFailureEvent("boom"); !strings.Contains(string(event), "upstream_stream_interrupted") {
		t.Fatalf("默认 code 事件=%s", event)
	}
	if event := BuildChatCompletionsGatewayStreamFailureEvent(withCode); !strings.HasPrefix(string(event), "data: ") {
		t.Fatalf("chat 流失败事件=%s", event)
	}
	if event := BuildAnthropicGatewayStreamFailureEvent(withCode); !strings.HasPrefix(string(event), "event: error") {
		t.Fatalf("anthropic 流失败事件=%s", event)
	}
	if event := BuildGeminiGatewayStreamFailureEvent(withCode); !strings.HasPrefix(string(event), "event: error") {
		t.Fatalf("gemini 流失败事件=%s", event)
	}
	// asValidationOrCodexAdapterError：nil / 校验错误 / 普通错误。
	if _, ok := asValidationOrCodexAdapterError(nil); ok {
		t.Fatal("nil 必须失败")
	}
	if _, ok := asValidationOrCodexAdapterError(errors.New("plain")); ok {
		t.Fatal("普通错误必须失败")
	}
	validation := &GatewayRequestValidationError{Message: "m", Code: "c", StatusCode: 400, Type: "t"}
	resolved, ok := asValidationOrCodexAdapterError(validation)
	if !ok || resolved.Message != "m" {
		t.Fatalf("校验错误=%v/%v", resolved, ok)
	}
	// responsesGuidanceOutputTexts：非 map / 无 content / 非 string text。
	texts := responsesGuidanceOutputTexts([]any{
		"not-a-map",
		map[string]any{"content": "not-array"},
		map[string]any{"content": []any{"not-map", map[string]any{"text": "hit"}, map[string]any{"other": 1}}},
	})
	if len(texts) != 2 || texts[0] != "hit" || texts[1] != "" {
		t.Fatalf("guidance texts=%v", texts)
	}
}

func TestW14HRetryBudgetArms(t *testing.T) {
	// clock 缺省回退 + 负预算钳制。
	budget := NewServerRetryBudget(-5, nil)
	if budget.WaitBudgetMs != 1 {
		t.Fatalf("预算=%d", budget.WaitBudgetMs)
	}
	// normalizedTimestamp 负值钳制。
	if got := normalizedTimestamp(-3); got != 0 {
		t.Fatalf("负时间戳=%d", got)
	}
	// 同一观察者重复设置直接返回。
	started, paused := 0, 0
	observer := &ServerRetryBudgetWaitObserver{OnWaitPaused: func() { paused++ }}
	budget.SetWaitObserver(observer)
	budget.SetWaitObserver(observer)
	// 等待进行中替换观察者：旧观察者暂停、新观察者启动。
	second := &ServerRetryBudgetWaitObserver{
		OnWaitStarted: func() { started++ },
	}
	budget.BeginNoAvailableWait(nil)
	budget.SetWaitObserver(second)
	if paused != 1 || started != 1 {
		t.Fatalf("观察者切换 paused=%d started=%d", paused, started)
	}
	budget.PauseNoAvailableWait(nil)
}

func TestW14HServiceAuditHelperArms(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	// newAuditID 形状。
	auditID := service.newAuditID()
	if !strings.HasPrefix(auditID, "audit_") {
		t.Fatalf("audit id=%q", auditID)
	}
	// dispatchDroppedAuditCapture：settings 缺失/关闭直接返回。
	nilSettings := &Service{AuditDispatch: &fakeAuditDispatcher{}}
	nilSettings.dispatchDroppedAuditCapture(droppedCaptureInput{})
	dispatcher := &fakeAuditDispatcher{}
	disabled, _, _ := newTestService(t, func(s *Service) {
		s.AuditSettings = &fakeAuditSettings{enabled: false}
		s.AuditDispatch = dispatcher
	})
	disabled.dispatchDroppedAuditCapture(droppedCaptureInput{})
	if len(dispatcher.dispatched) != 0 {
		t.Fatal("关闭审计不应派发")
	}
	// 正常派发：空 path/method 回退。
	enabledDispatcher := &fakeAuditDispatcher{}
	enabled, _, _ := newTestService(t, func(s *Service) {
		s.AuditDispatch = enabledDispatcher
	})
	enabled.dispatchDroppedAuditCapture(droppedCaptureInput{
		method:      "get",
		path:        " ",
		queryString: "a=1",
		statusCode:  502,
	})
	if len(enabledDispatcher.dispatched) != 1 {
		t.Fatalf("派发条数=%d", len(enabledDispatcher.dispatched))
	}
	dispatched := enabledDispatcher.dispatched[0]
	if dispatched.Path != "unknown" || dispatched.Method != "GET" || dispatched.QueryString != "a=1" {
		t.Fatalf("派发内容=%+v", dispatched)
	}
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	// recordEarlyGatewayAuthFailure：带 bearer 与不带 bearer 的两条消息臂。
	writer.WriteHeader(http.StatusUnauthorized)
	noBearer := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat", nil))
	service.recordEarlyGatewayAuthFailure(writer, noBearer)
	withBearer := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat", nil))
	withBearer.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	service.recordEarlyGatewayAuthFailure(writer, withBearer)
	withMessage := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat", nil))
	withMessage.AuthFailureErrorMessage = "自定义失败"
	withMessage.AuthFailureErrorCode = "custom_code"
	service.recordEarlyGatewayAuthFailure(writer, withMessage)
}

func TestW14HObservedContextArms(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	// 通过 RequestContextMiddleware 附加 kernel 上下文，覆盖 context 优先臂。
	var observedIP, observedTrace string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayRequest := &GatewayRequest{HTTP: r}
		observedIP = service.observedClientIP(gatewayRequest)
		observedTrace = service.observedTraceID(gatewayRequest)
	})
	middleware := kernel.RequestContextMiddleware(0)
	request := httptest.NewRequest("GET", "/v1/x", nil)
	request.RemoteAddr = "203.0.113.9:44556"
	middleware(handler).ServeHTTP(httptest.NewRecorder(), request)
	if observedIP == "" {
		t.Fatal("kernel 上下文必须提供 client ip")
	}
	if observedTrace == "" {
		t.Fatal("kernel 上下文必须提供 trace id")
	}
	// Observability trace 为空时回退 CreateTraceID。
	emptyTrace := &w14hEmptyTraceObservability{obs: &fakeObservability{}}
	fallback := &Service{Observability: emptyTrace}
	if got := fallback.observedTraceID(NewGatewayRequest(httptest.NewRequest("GET", "/x", nil))); got == "" {
		t.Fatal("CreateTraceID 回退必须非空")
	}
	// Observability 缺省时返回空。
	if got := (&Service{}).observedTraceID(&GatewayRequest{}); got != "" {
		t.Fatalf("nil observability=%q", got)
	}
	if got := service.observedClientIP(&GatewayRequest{}); got != "" {
		t.Fatalf("nil HTTP client ip=%q", got)
	}
}

type w14hEmptyTraceObservability struct{ obs *fakeObservability }

func (e *w14hEmptyTraceObservability) Logger() Logger { return e.obs.Logger() }
func (e *w14hEmptyTraceObservability) TraceID() string { return "" }
func (e *w14hEmptyTraceObservability) CreateTraceID() string { return "trace_w14h" }
func (e *w14hEmptyTraceObservability) SanitizeURLForLog(value string) string { return value }
func (e *w14hEmptyTraceObservability) LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time) {
	e.obs.LogRequestStage(stage, fields, outcome, startedAt)
}

func TestW14HRecordClientIPErrorSampleArms(t *testing.T) {
	ctx := context.Background()
	// 未熔断 → 只记录样本。
	notBlocked, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{}
	})
	notBlocked.RecordClientIPRequestErrorSample(ctx, w14hCapture{onMetadata: func(string, map[string]any) {}}, ClientIPErrorSampleInput{})
	// 熔断打开 → 发出诊断与审计元数据。
	blockedDecision := CircuitDecision{Blocked: true, Reason: "w14h"}
	var metadata map[string]any
	blocked, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{sampleDecision: blockedDecision}
	})
	blocked.RecordClientIPRequestErrorSample(ctx, w14hCapture{onMetadata: func(label string, m map[string]any) {
		metadata = m
	}}, ClientIPErrorSampleInput{Reason: ClientIPErrorCircuitInvalidJSON})
	if metadata == nil || metadata["opened"] != true {
		t.Fatalf("熔断元数据=%v", metadata)
	}
}

type w14hCapture struct {
	onMetadata func(label string, metadata map[string]any)
}

func (c w14hCapture) BindContext(AuditGatewayContext)            {}
func (c w14hCapture) AddGatewayMetadata(label string, m map[string]any) { c.onMetadata(label, m) }
func (c w14hCapture) Finalize(AuditFinalizeInput)                {}

func TestW14HResolveRuntimeAsyncArms(t *testing.T) {
	ctx := context.Background()
	// 已解析 runtime 直接复用。
	cached, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	cached.Runtime = &gatewayruntimecache.GatewayRuntime{APIKey: &gatewayruntimecache.GatewayAPIKeyRow{ID: "w14h-key"}}
	service, _, _ := newTestService(t, nil)
	runtime, err := service.ResolveGatewayRuntimeAsync(ctx, writer, cached, ResolveGatewayRuntimeOptions{})
	if err != nil || runtime == nil || runtime.APIKey == nil {
		t.Fatalf("缓存复用=%v/%v", runtime, err)
	}
	// 缺少 bearer → 401。
	missing, recorder2, writer2 := newTestRequest("POST", "/v1/chat/completions")
	if _, err := service.ResolveGatewayRuntimeAsync(ctx, writer2, missing, ResolveGatewayRuntimeOptions{CloseConnectionOnAuthFailure: true}); err != nil {
		t.Fatal(err)
	}
	if recorder2.Code != http.StatusUnauthorized {
		t.Fatalf("缺失令牌状态=%d", recorder2.Code)
	}
	// runtime cache 读取失败 → 透传错误。
	readErrService, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{readErr: w14hBoomErr}
	})
	noKey, _, writer3 := newTestRequest("POST", "/v1/chat/completions")
	noKey.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := readErrService.ResolveGatewayRuntimeAsync(ctx, writer3, noKey, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("缓存读取失败必须透传")
	}
	// runtime cache 未命中 → 401。
	unresolvedService, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &fakeRuntimeCache{}
	})
	unresolved, recorder4, writer4 := newTestRequest("POST", "/v1/chat/completions")
	unresolved.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := unresolvedService.ResolveGatewayRuntimeAsync(ctx, writer4, unresolved, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder4.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌状态=%d", recorder4.Code)
	}
	_ = recorder
}

func TestW14HResolveModelsAPIKeyArms(t *testing.T) {
	ctx := context.Background()
	// 缺少 bearer → rejectMissingOrInvalidGatewayCredential 记录 401。
	service, _, _ := newTestService(t, nil)
	missing, recorder, writer := newTestRequest("GET", "/v1/models")
	if _, err := service.ResolveGatewayAPIKeyForModelsAsync(ctx, writer, missing, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("缺失令牌状态=%d", recorder.Code)
	}
	// validator 未命中 → 401。
	noRow, _, _ := newTestService(t, func(s *Service) {
		s.APIKeyValidator = &fakeAPIKeyValidator{}
	})
	noKey, recorder2, writer2 := newTestRequest("GET", "/v1/models")
	noKey.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := noRow.ResolveGatewayAPIKeyForModelsAsync(ctx, writer2, noKey, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder2.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌状态=%d", recorder2.Code)
	}
	// validator 命中 → 返回 API Key。
	valid, _, _ := newTestService(t, nil)
	okKey, recorder3, writer3 := newTestRequest("GET", "/v1/models")
	okKey.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	apiKey, err := valid.ResolveGatewayAPIKeyForModelsAsync(ctx, writer3, okKey, ResolveGatewayRuntimeOptions{})
	if err != nil || apiKey == nil {
		t.Fatalf("有效令牌=%v/%v", apiKey, err)
	}
	if recorder3.Code != http.StatusOK {
		t.Fatalf("成功路径不应写响应=%d", recorder3.Code)
	}
}

// ---------------------------------------------------------------------------
// ResolveGatewayRuntime/ForModels 的熔断、封禁与记录错误臂
// ---------------------------------------------------------------------------

// w14hCircuitsOverride 在 fakeCircuits 上叠加 inspect/record 错误注入。
type w14hCircuitsOverride struct {
	fakeCircuits
	inspectErr error
}

func (c *w14hCircuitsOverride) InspectPreAuthCircuit(ctx context.Context, input PreAuthCircuitInput) (CircuitDecision, error) {
	if c.inspectErr != nil {
		return CircuitDecision{}, c.inspectErr
	}
	return c.fakeCircuits.InspectPreAuthCircuit(ctx, input)
}

// w14hIPPolicyOverride 在 fakeIPPolicy 上叠加错误注入。
type w14hIPPolicyOverride struct {
	fakeIPPolicy
	err error
}

func (p *w14hIPPolicyOverride) InspectClientIPPolicy(ctx context.Context, ip string, cacheOnly bool) (ClientIPPolicyDecision, error) {
	if p.err != nil {
		return ClientIPPolicyDecision{}, p.err
	}
	return p.fakeIPPolicy.InspectClientIPPolicy(ctx, ip, cacheOnly)
}

func TestW14HResolveRuntimeBlockedArms(t *testing.T) {
	ctx := context.Background()
	blockedPolicy := ClientIPPolicyDecision{
		Blocked:         true,
		BlacklistPolicy: &BlacklistPolicy{ID: "w14h-policy", IPHash: "hash", Reason: "abuse", ClientIP: "203.0.113.9", AggregateIPKey: "agg-w14h"},
		NormalizedIP:    &NormalizedClientIP{ClientIP: "203.0.113.9", AggregateIPKey: "agg-w14h"},
	}
	// IP 黑名单缓存命中 → 403 封禁响应（runtime 与 models 两条路径）。
	for _, name := range []string{"runtime", "models"} {
		t.Run(name, func(t *testing.T) {
			service, _, _ := newTestService(t, func(s *Service) {
				s.IPPolicy = &fakeIPPolicy{decision: blockedPolicy}
			})
			request, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
			request.HTTP.Header.Set("authorization", "Bearer sk-w14h")
			var err error
			if name == "runtime" {
				_, err = service.ResolveGatewayRuntimeAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{})
			} else {
				_, err = service.ResolveGatewayAPIKeyForModelsAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("封禁状态=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	// Inspect 熔断错误 → 透传。
	inspectErr, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &w14hCircuitsOverride{inspectErr: w14hBoomErr}
	})
	request, _, writer := newTestRequest("POST", "/v1/chat/completions")
	if _, err := inspectErr.ResolveGatewayRuntimeAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("熔断检查错误必须透传")
	}
	// 熔断已打开 → 403 短路（runtime 与 models 两条路径）。
	openDecision := CircuitDecision{Blocked: true, Reason: "w14h-open"}
	for _, name := range []string{"runtime", "models"} {
		t.Run(name, func(t *testing.T) {
			service, _, _ := newTestService(t, func(s *Service) {
				s.Circuits = &fakeCircuits{inspectDecision: openDecision}
			})
			request, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
			request.HTTP.Header.Set("authorization", "Bearer sk-w14h")
			var err error
			if name == "runtime" {
				_, err = service.ResolveGatewayRuntimeAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{})
			} else {
				_, err = service.ResolveGatewayAPIKeyForModelsAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("熔断状态=%d", recorder.Code)
			}
		})
	}
	// 记录失败计数出错 → 透传（无 bearer / 带 bearer 两条路径）。
	recordErrService, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{recordErr: w14hBoomErr}
	})
	noKey, _, writer := newTestRequest("POST", "/v1/chat/completions")
	if _, err := recordErrService.ResolveGatewayRuntimeAsync(ctx, writer, noKey, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("记录失败错误（缺 bearer）必须透传")
	}
	withKey, _, writer := newTestRequest("POST", "/v1/chat/completions")
	withKey.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := recordErrService.ResolveGatewayRuntimeAsync(ctx, writer, withKey, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("记录失败错误（无效令牌）必须透传")
	}
	// 失败计数触发熔断 → 静默短路。
	blockedRecord, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{recordDecision: openDecision}
	})
	silent, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	if _, err := blockedRecord.ResolveGatewayRuntimeAsync(ctx, writer, silent, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("熔断短路响应=%d", recorder.Code)
	}
	withKey2, recorder2, writer2 := newTestRequest("POST", "/v1/chat/completions")
	withKey2.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := blockedRecord.ResolveGatewayRuntimeAsync(ctx, writer2, withKey2, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder2.Code != http.StatusTooManyRequests {
		t.Fatalf("熔断短路响应=%d", recorder2.Code)
	}
	// models 路径：rejectMissingOrInvalidGatewayCredential 错误透传与熔断短路。
	modelsRecordErr, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{recordErr: w14hBoomErr}
	})
	modelsNoKey, _, writer3 := newTestRequest("GET", "/v1/models")
	if _, err := modelsRecordErr.ResolveGatewayAPIKeyForModelsAsync(ctx, writer3, modelsNoKey, ResolveGatewayRuntimeOptions{}); err == nil {
		t.Fatal("models 记录失败错误必须透传")
	}
	modelsBlocked, recorder4, writer4 := newTestRequest("GET", "/v1/models")
	modelsBlockedService, _, _ := newTestService(t, func(s *Service) {
		s.Circuits = &fakeCircuits{recordDecision: openDecision}
	})
	if _, err := modelsBlockedService.ResolveGatewayAPIKeyForModelsAsync(ctx, writer4, modelsBlocked, ResolveGatewayRuntimeOptions{}); err != nil {
		t.Fatal(err)
	}
	if recorder4.Code != http.StatusTooManyRequests {
		t.Fatalf("models 熔断短路响应=%d", recorder4.Code)
	}
	// IP 策略检查出错 → rejectCachedClientIPBlacklist 视为未命中。
	policyErr, _, _ := newTestService(t, func(s *Service) {
		s.IPPolicy = &w14hIPPolicyOverride{err: w14hBoomErr}
	})
	policyErrRequest, _, writer5 := newTestRequest("POST", "/v1/chat/completions")
	if _, err := policyErr.ResolveGatewayRuntimeAsync(ctx, writer5, policyErrRequest, ResolveGatewayRuntimeOptions{InspectClientIPPolicyAfterRuntime: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
}

// w14hMarkerWriter 实现 kernel.UpstreamMarker 供 TrackingWriter 转发。
type w14hMarkerWriter struct {
	httptest.ResponseRecorder
	marked bool
}

func (w *w14hMarkerWriter) MarkUpstream()      { w.marked = true }
func (w *w14hMarkerWriter) MarkedUpstream() bool { return w.marked }

func TestW14HResponsePayloadExtraArms(t *testing.T) {
	// Error.Extra 序列化 + 本地化未变更原样返回。
	payload := GatewayErrorPayloadOf("请求失败", "rate_limit_error", "code_x")
	payload.Error.Extra = map[string]any{"retry": 3}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "retry") {
		t.Fatalf("payload=%s", encoded)
	}
	same := LocalizedGatewayErrorPayload(payload, http.StatusTooManyRequests)
	if same.Error.Message != "请求失败" {
		t.Fatalf("中文消息应原样保留=%q", same.Error.Message)
	}
	// MarkUpstreamError 转发到实现 marker 的底层 writer。
	marker := &w14hMarkerWriter{}
	wrapper := NewTrackingWriter(marker)
	wrapper.MarkUpstreamError()
	if !marker.marked {
		t.Fatal("上游标记必须转发")
	}
	// dispatchDroppedAuditCapture：空 path 与空 method 回退臂。
	dispatcher := &fakeAuditDispatcher{}
	service, _, _ := newTestService(t, func(s *Service) {
		s.AuditDispatch = dispatcher
	})
	service.dispatchDroppedAuditCapture(droppedCaptureInput{path: "?a=1"})
	if len(dispatcher.dispatched) != 1 {
		t.Fatalf("派发条数=%d", len(dispatcher.dispatched))
	}
	sent := dispatcher.dispatched[0]
	if sent.Path != "unknown" || sent.Method != "UNKNOWN" || sent.QueryString != "a=1" {
		t.Fatalf("空值回退=%+v", sent)
	}
}

// ---------------------------------------------------------------------------
// preflighthelpers：路由配置、可恢复账户等待与回退准备错误臂
// ---------------------------------------------------------------------------
