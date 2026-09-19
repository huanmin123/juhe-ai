package main

// w1: chain_ports.go 端口适配器收割——纯函数诊断面（错误名/码、上游 URL
// 脱敏、响应头/体投影、attempt 重建）、进程内会话亲和（localSessionAffinity
// 全生命周期）、slog 可观测性适配与审计/用量适配器的缺席守卫。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bytes"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// w1CodedError 携带 ErrorCode() 的错误，覆盖 upstreamRequestErrorCode 的
// code 提取分支（Node objectStringProperty(error,'code')）。
type w1CodedError struct{ code string }

func (e w1CodedError) Error() string     { return "编码错误" }
func (e w1CodedError) ErrorCode() string { return e.code }

func TestW1UpstreamRequestErrorDiagnostics(t *testing.T) {
	// 错误名：类型名去掉包路径（Node error.name）。
	if got := upstreamRequestErrorName(nil); got != "" {
		t.Fatalf("nil name = %q", got)
	}
	if got := upstreamRequestErrorName(&gatewaydispatch.UpstreamRequestAbortedError{}); got != "UpstreamRequestAbortedError" {
		t.Fatalf("name = %q", got)
	}
	// 错误码：nil / 无 code 面 / 有 code 面。
	if got := upstreamRequestErrorCode(nil); got != "" {
		t.Fatalf("nil code = %q", got)
	}
	if got := upstreamRequestErrorCode(errors.New("普通错误")); got != "" {
		t.Fatalf("普通错误 code = %q", got)
	}
	if got := upstreamRequestErrorCode(w1CodedError{code: "insufficient_quota"}); got != "insufficient_quota" {
		t.Fatalf("code = %q", got)
	}
	// 上游 URL 脱敏：空值 → unknown。
	if got := chainSanitizedUpstreamURL("  "); got != "unknown" {
		t.Fatalf("sanitized = %q", got)
	}
	if got := chainSanitizedUpstreamURL("https://upstream.example/v1"); got != "https://upstream.example/v1" {
		t.Fatalf("sanitized = %q", got)
	}
	// 传输失败分类：超时优先、未证实响应起点为连接失败、已证实为读取不完整。
	if got := formatUpstreamRequestTransportFailureKind(errors.New("upstream request timeout after 30s"), nil); got != gatewaydispatch.TransportFailureKindTimeout {
		t.Fatalf("timeout kind = %q", got)
	}
	if got := formatUpstreamRequestTransportFailureKind(nil, nil); got != gatewaydispatch.TransportFailureKindConnection {
		t.Fatalf("connection kind = %q", got)
	}
	if got := formatUpstreamRequestTransportFailureKind(nil, &gatewaydispatch.UpstreamAttempt{HasStatus: true}); got != gatewaydispatch.TransportFailureKindReadIncomplete {
		t.Fatalf("read kind = %q", got)
	}
	if got := formatUpstreamRequestErrorMessage(nil); got != "请求失败" {
		t.Fatalf("message = %q", got)
	}
	if got := formatUpstreamRequestErrorMessage(errors.New(" 拨号失败 ")); got != "拨号失败" {
		t.Fatalf("message = %q", got)
	}
	// 请求方法与状态指针。
	if got := requestMethodOf(nil); got != "" {
		t.Fatalf("nil method = %q", got)
	}
	if statusPointer(false, 500) != nil {
		t.Fatal("无状态必须 nil")
	}
	if got := statusPointer(true, 502); got == nil || *got != 502 {
		t.Fatalf("status = %v", got)
	}
}

func TestW1ResponseProjectionHelpers(t *testing.T) {
	// 响应头投影：小写化 + 值合并；nil / 无 Header → nil。
	if got := responseHeadersOf(nil); got != nil {
		t.Fatalf("nil headers = %v", got)
	}
	empty := &gatewaydispatch.GatewayUpstreamResponse{}
	if got := responseHeadersOf(empty); got != nil {
		t.Fatalf("空 Header = %v", got)
	}
	if got := usageFailureHeadersOf(nil); got != nil {
		t.Fatalf("usage nil = %v", got)
	}
	response := &gatewaydispatch.GatewayUpstreamResponse{Header: http.Header{
		"Content-Type": {"application/json"},
		"X-Multi":      {"a", "b"},
		"X-Empty":      {},
	}}
	headers := responseHeadersOf(response)
	if headers["content-type"] != "application/json" || headers["x-multi"] != "a, b" {
		t.Fatalf("headers = %v", headers)
	}
	if _, exists := headers["X-Empty"]; exists {
		t.Fatal("空值头必须跳过")
	}
	if responseContentTypeOf(nil) != "" || responseContentTypeOf(empty) != "" {
		t.Fatal("content-type 缺席必须空")
	}
	if got := responseContentTypeOf(response); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	if responseHTTPHeaderOf(nil) != nil {
		t.Fatal("nil 必须返回 nil header")
	}
	// usage 投影：string → any 值域。
	projected := usageFailureHeadersOf(response)
	if projected["x-multi"] != "a, b" {
		t.Fatalf("projected = %v", projected)
	}
	// 解析失败体：合法 JSON 对象才投影。
	if got := parsedFailureBodyOf(nil, ""); got != nil {
		t.Fatalf("nil body = %v", got)
	}
	if got := parsedFailureBodyOf(response, ""); got != nil {
		t.Fatalf("空文本 = %v", got)
	}
	if got := parsedFailureBodyOf(response, "不是 JSON"); got != nil {
		t.Fatalf("非法 JSON = %v", got)
	}
	parsed := parsedFailureBodyOf(response, `{"error":{"code":"quota"}}`)
	if parsed == nil || parsed["error"] == nil {
		t.Fatalf("parsed = %v", parsed)
	}
	// 协议载荷投影：nil → 空 payload；有值 → 提取。
	emptyPayload := failureProtocolPayloadOf(nil)
	if emptyPayload.HasEvidence() {
		t.Fatal("nil 解析体必须空载荷")
	}
	payload := failureProtocolPayloadOf(parsed)
	if !payload.HasEvidence() {
		t.Fatalf("载荷必须携带证据 = %+v", payload)
	}
	if usageErrorPayloadOf(emptyPayload) != nil {
		t.Fatal("空载荷不投影 usage")
	}
	usagePayload := usageErrorPayloadOf(payload)
	asMap, ok := usagePayload.(map[string]any)
	if !ok || asMap["code"] != "quota" {
		t.Fatalf("usage payload = %v", usagePayload)
	}
	// 失败体有界读取：截断与 nil 守卫。
	if text, truncated, err := readUpstreamFailureBody(context.Background(), nil, 8); text != "" || truncated || err != nil {
		t.Fatalf("nil response = %q, %v, %v", text, truncated, err)
	}
	truncatedResponse := &gatewaydispatch.GatewayUpstreamResponse{Body: io.NopCloser(strings.NewReader("0123456789"))}
	text, truncated, err := readUpstreamFailureBody(context.Background(), truncatedResponse, 4)
	if err != nil || !truncated || text != "0123" {
		t.Fatalf("truncated = %q, %v, %v", text, truncated, err)
	}
	fullResponse := &gatewaydispatch.GatewayUpstreamResponse{Body: io.NopCloser(strings.NewReader("短"))}
	text, truncated, err = readUpstreamFailureBody(context.Background(), fullResponse, 8)
	if err != nil || truncated || text != "短" {
		t.Fatalf("full = %q, %v, %v", text, truncated, err)
	}
}

func TestW1AttemptRebuildHelpers(t *testing.T) {
	candidate := gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一", ProviderCode: "openai", ProtocolCode: "responses",
	}
	upstream := gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusTooManyRequests, http.Header{}, io.NopCloser(strings.NewReader("")))
	// 失败响应重建：携带上一尝试事实 + 覆盖账户与响应字段。
	withPrevious := failedResponseAttemptOf(gatewaydispatch.FailedUpstreamResponseInput{
		Account: candidate, UpstreamURL: "https://u/v1", Response: upstream,
		LastAttempt: &gatewaydispatch.UpstreamAttempt{Message: "上次消息"},
	}, "失败体", map[string]any{"k": "v"})
	if withPrevious.AccountID != "acc_1" || withPrevious.UpstreamURL != "https://u/v1" || withPrevious.Status != http.StatusTooManyRequests || !withPrevious.HasStatus {
		t.Fatalf("attempt = %+v", withPrevious)
	}
	if withPrevious.Message != "上次消息" || withPrevious.ResponseBodyText != "失败体" || withPrevious.ParsedResponseBody["k"] != "v" {
		t.Fatalf("body = %+v", withPrevious)
	}
	// 无响应：HasStatus 保持 false。
	withoutResponse := failedResponseAttemptOf(gatewaydispatch.FailedUpstreamResponseInput{
		Account: candidate, UpstreamURL: "https://u/v1",
	}, "", nil)
	if withoutResponse.HasStatus || withoutResponse.Status != 0 {
		t.Fatalf("attempt = %+v", withoutResponse)
	}
	// 下游关闭重建：同名同 URL 才继承上一尝试状态。
	downstream := downstreamClosedAttemptOf(gatewaydispatch.UpstreamRequestErrorInput{
		Account: candidate, UpstreamURL: "https://u/v1",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{AccountID: "acc_1", UpstreamURL: "https://u/v1", Status: 429, HasStatus: true},
	})
	if downstream.Status != 429 || !downstream.HasStatus {
		t.Fatalf("downstream = %+v", downstream)
	}
	mismatched := downstreamClosedAttemptOf(gatewaydispatch.UpstreamRequestErrorInput{
		Account: candidate, UpstreamURL: "https://u/v1",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{AccountID: "other", Status: 429, HasStatus: true},
	})
	if mismatched.HasStatus {
		t.Fatal("不匹配的上一尝试不得继承状态")
	}
	// 传输失败重建：携带上一尝试事实 + 消息与失败类别。
	transport := transportFailureAttemptOf(gatewaydispatch.UpstreamRequestErrorInput{
		Account: candidate, UpstreamURL: "https://u/v1",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{ErrorCode: "ECONNRESET"},
	}, "拨号失败", gatewaydispatch.TransportFailureKindConnection)
	if transport.ErrorCode != "ECONNRESET" || transport.Message != "拨号失败" || transport.TransportFailureKind != gatewaydispatch.TransportFailureKindConnection {
		t.Fatalf("transport = %+v", transport)
	}
}

func TestW1LocalSessionAffinityLifecycle(t *testing.T) {
	affinity := newLocalSessionAffinity()
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{{ID: "a"}, {ID: "b"}}
	// 空 key：顺序原样返回。
	ordered, err := affinity.OrderAsync(ctx, accounts, "", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || len(ordered) != 2 {
		t.Fatalf("empty key = %+v, %v", ordered, err)
	}
	// 记住 b 后排序置前。
	affinity.RememberAsync(ctx, "sess_1", "b", gatewaydispatch.AffinityScope{GroupID: "grp"})
	ordered, err = affinity.OrderAsync(ctx, accounts, "sess_1", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || ordered[0].ID != "b" {
		t.Fatalf("ordered = %+v, %v", ordered, err)
	}
	// Claim：记住的账户获胜。
	claimed, ok := affinity.ClaimAsync(ctx, "sess_1", "a", gatewaydispatch.AffinityScope{})
	if !ok || claimed != "b" {
		t.Fatalf("claim = %q, %v", claimed, ok)
	}
	// 新会话：认领提议账户并写入 TTL。
	claimed, ok = affinity.ClaimAsync(ctx, "sess_2", "a", gatewaydispatch.AffinityScope{GroupID: "grp"})
	if !ok || claimed != "a" {
		t.Fatalf("fresh claim = %q, %v", claimed, ok)
	}
	// 空 key / 空账户的守卫。
	// 空 key：返回提议账户但不占用（ok=false）。
	claimed, ok = affinity.ClaimAsync(ctx, "", "a", gatewaydispatch.AffinityScope{})
	if ok || claimed != "a" {
		t.Fatalf("empty key claim = %q, %v", claimed, ok)
	}
	claimed, ok = affinity.ClaimAsync(ctx, "sess_3", "", gatewaydispatch.AffinityScope{})
	if ok || claimed != "" {
		t.Fatalf("empty account claim = %q, %v", claimed, ok)
	}
	// 过期条目：排序回落原序、认领走重新认领分支。
	affinity.mu.Lock()
	affinity.ttls["sess_1"] = time.Now().Add(-time.Hour)
	affinity.ttls["sess_2"] = time.Now().Add(-time.Hour)
	affinity.mu.Unlock()
	ordered, _ = affinity.OrderAsync(ctx, accounts, "sess_1", gatewaydispatch.AffinityOrderingOptions{})
	if ordered[0].ID != "a" {
		t.Fatalf("expired order = %+v", ordered)
	}
	claimed, ok = affinity.ClaimAsync(ctx, "sess_2", "c", gatewaydispatch.AffinityScope{})
	if !ok || claimed != "c" {
		t.Fatalf("expired claim = %q, %v", claimed, ok)
	}
	// 遗忘后过期。
	if err := affinity.ForgetAsync(ctx, "sess_2", "c"); err != nil {
		t.Fatalf("forget = %v", err)
	}
	affinity.mu.Lock()
	expired := affinity.expired("sess_2")
	affinity.mu.Unlock()
	if !expired {
		t.Fatal("遗忘后必须过期")
	}
	if err := affinity.ForgetAsync(ctx, "", "c"); err != nil {
		t.Fatalf("empty forget = %v", err)
	}
	// 高并发账户：零值 localSessionAffinity（nil 并发事实源）保持显式降级，
	// 恒不忙（注入 store 后的真实谓词见
	// TestW1PLocalSessionAffinityHighConcurrencyBusy）。
	affinity.concurrency = nil
	busy, err := affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, gatewaydispatch.HighConcurrencyBusyOptions{})
	if err != nil || busy {
		t.Fatalf("busy = %v, %v", busy, err)
	}
}

// w1ObservationPortStub 是 chainAPIKeyObservationPort 的可控 stub。
type w1ObservationPortStub struct {
	epoch *int64
}

func (s w1ObservationPortStub) CaptureFailureObservation(gatewayruntimecache.OpenAIAccountSecret) *int64 {
	return s.epoch
}

func TestW1ObservationEpochAndHeaderAccount(t *testing.T) {
	candidate := gatewaydispatch.AccountCandidate{ID: "acc_1", APIKey: "sk-x", Type: "api_key", ProviderCode: "openai"}
	// 观察纪元：缺席端口 / 空纪元 / 有纪元（十进制字符串）。
	if got := chainObservationEpochOf(nil, candidate); got != "" {
		t.Fatalf("nil port = %q", got)
	}
	if got := chainObservationEpochOf(w1ObservationPortStub{}, candidate); got != "" {
		t.Fatalf("nil epoch = %q", got)
	}
	epoch := int64(42)
	if got := chainObservationEpochOf(w1ObservationPortStub{epoch: &epoch}, candidate); got != "42" {
		t.Fatalf("epoch = %q", got)
	}
	// 头部账户投影：派发候选字段逐一映射。
	headerAccount := chainUpstreamHeaderAccountOf(candidate)
	if headerAccount.ID != "acc_1" || headerAccount.APIKey != "sk-x" || headerAccount.Type != "api_key" || headerAccount.ProviderCode != "openai" {
		t.Fatalf("header account = %+v", headerAccount)
	}
}

func TestW1SlogObservabilityAdapter(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// nil 依赖降级：默认 logger + 系统 clock。
	nilObservability := newSlogObservability(nil, nil)
	if nilObservability.logger == nil || nilObservability.clock == nil {
		t.Fatal("nil 依赖必须降级")
	}
	observability := newSlogObservability(logger, gatewaypreauth.SystemClock{})
	if observability.TraceID() != "" {
		t.Fatal("无请求上下文 trace id 必须为空")
	}
	if !strings.HasPrefix(observability.CreateTraceID(), "trace_") {
		t.Fatalf("trace id = %q", observability.CreateTraceID())
	}
	if observability.SanitizeURLForLog("https://u/v1") != "https://u/v1" {
		t.Fatal("日志 URL 不改写")
	}
	// 阶段日志级别策略：unexpected→error / expected→warn / aborted→warn /
	// 慢阶段→info / 常规→debug。
	observability.LogRequestStage("preflight", nil, "unexpected_failure", time.Now().Add(-time.Second))
	observability.LogRequestStage("preflight", map[string]any{"traceId": "t1"}, "expected_failure", time.Now())
	observability.LogRequestStage("dispatch", nil, "aborted", time.Now())
	observability.LogRequestStage("slow_stage", nil, "success", time.Now().Add(-30*time.Second))
	observability.LogRequestStage("fast_stage", nil, "success", time.Now())
	logs := buffer.String()
	for _, want := range []string{"请求阶段未预期失败：preflight", "请求阶段预期失败：preflight", "请求阶段中断：dispatch", "请求慢阶段完成：slow_stage", "fast_stage"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("日志缺少 %q：\n%s", want, logs)
		}
	}
	// 级别策略纯函数：1s 阈值分界。
	if got := gatewayRequestStageLogLevel("unexpected_failure", 0); got != "error" {
		t.Fatalf("level = %q", got)
	}
	if got := gatewayRequestStageLogLevel("expected_failure", 0); got != "warn" {
		t.Fatalf("level = %q", got)
	}
	if got := gatewayRequestStageLogLevel("success", gatewaySlowStageThresholdMs); got != "info" {
		t.Fatalf("level = %q", got)
	}
	if got := gatewayRequestStageLogLevel("success", gatewaySlowStageThresholdMs-1); got != "debug" {
		t.Fatalf("level = %q", got)
	}
	// fmtInt64：0 / 负数 / 正数。
	if got := fmtInt64(0); got != "0" {
		t.Fatalf("zero = %q", got)
	}
	if got := fmtInt64(-1728000000000); got != "-1728000000000" {
		t.Fatalf("negative = %q", got)
	}
	if got := fmtInt64(1728000000000); got != "1728000000000" {
		t.Fatalf("positive = %q", got)
	}
}

func TestW1AdapterGuardsAndIdentity(t *testing.T) {
	// usage 适配器：recorder / service 缺席直接返回。
	usageDispatchAdapter{}.DispatchUsageRecord(gatewayresponse.ModelsUsageDispatchInput{})
	usageDispatchAdapter{}.RecordGatewayFailure(gatewayresponse.FailureUsageRecordInput{})
	// 用量失败上下文投影：字段逐一映射。
	source := gatewaypreauth.GatewayFailureUsageContext{TraceID: "t1", TrafficSource: "gateway", SystemAccountID: "sys_1", GroupID: "grp_1", ProviderCode: "openai"}
	projected := usageFailureContextOf(source)
	if projected.TraceID != "t1" || projected.ProviderCode != "openai" || projected.SystemAccountID != "sys_1" {
		t.Fatalf("projected = %+v", projected)
	}
	// 尝试状态码：HasStatusCode 门。
	if attemptStatusCodeOf(gatewaydispatch.FailedAttemptRecord{}) != nil {
		t.Fatal("无状态必须 nil")
	}
	status := 502
	statusPointerValue := attemptStatusCodeOf(gatewaydispatch.FailedAttemptRecord{HasStatusCode: true, StatusCode: status})
	if statusPointerValue == nil || *statusPointerValue != 502 {
		t.Fatalf("status = %v", statusPointerValue)
	}
	// 会话身份适配器：nil 请求 / 无身份服务时回退请求头。
	nilIdentity := sessionIdentityAdapter{}.ResolveGatewaySessionIdentity(nil, gatewaypreauth.SessionIdentityInput{})
	if nilIdentity.SessionID != "" || nilIdentity.ConversationKey != "" {
		t.Fatalf("nil req identity = %+v", nilIdentity)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	request.Header.Set("X-Session-Id", "  sess-9  ")
	request.Header.Set("X-Conversation-Key", " conv-1 ")
	identity := sessionIdentityAdapter{}.ResolveGatewaySessionIdentity(gatewaypreauth.NewGatewayRequest(request), gatewaypreauth.SessionIdentityInput{})
	if identity.SessionID != "sess-9" || identity.ConversationKey != "conv-1" {
		t.Fatalf("identity = %+v", identity)
	}
	// 亲和键适配器：服务缺席 / 空 conversation key → false。
	_, sourceOK := (sessionAffinityAdapter{}).ResolveKeyFromClientSource(nil, gatewaypreauth.SessionAffinityScope{})
	if sourceOK {
		t.Fatal("缺席服务必须 false")
	}
	_, keyOK := (sessionAffinityAdapter{}).ResolveKey(gatewaypreauth.SessionIdentity{}, gatewaypreauth.SessionAffinityScope{})
	if keyOK {
		t.Fatal("空 conversation key 必须 false")
	}
	// scope 投影：字段逐一映射。
	scope := gatewaySessionAffinityScopeOf(gatewaypreauth.SessionAffinityScope{SystemAccountID: "sys_1", APIKeyID: "key_1", RouteStrategyID: "rs_1", GroupID: "grp_1"}, "secret")
	if scope.HMACSecret != "secret" || scope.RouteStrategyID != "rs_1" || scope.GroupID != "grp_1" {
		t.Fatalf("scope = %+v", scope)
	}
	// codex 桥 preflight 保持非 nil。
	if chainCodexBridgePreflight(nil) == nil {
		t.Fatal("必须回退到适配器")
	}
	// 审计适配器：producer 缺席静默丢弃。
	auditDispatchAdapter{}.Dispatch(gatewaypreauth.DispatchedAuditLogInput{})
	auditUsageDispatcher{}.DispatchAuditLog(nil, gatewayusage.AuditLogInput{})
	// auditSettingsSource：nil enabled 函数 → 关闭；零值构造采样字段保持 0。
	settings := (auditSettingsSourceAdapter{}).ReadAuditLogSettings()
	if settings.Enabled {
		t.Fatal("nil enabled 必须关闭")
	}
	if settings.SuccessSampleRate != 0 || settings.SuccessHotRetentionHours != 0 {
		t.Fatalf("零值构造采样字段应保持 0：%+v", settings)
	}
	// E2E-FINDING #10：采样字段构造透传。
	wired := (auditSettingsSourceAdapter{successSampleRate: 0.1, successHotRetentionHours: 1}).ReadAuditLogSettings()
	if wired.SuccessSampleRate != 0.1 || wired.SuccessHotRetentionHours != 1 {
		t.Fatalf("采样字段透传断言失败：%+v", wired)
	}
	// 协议探测助手：可调用且不 panic（原生判定依赖各自头，断言仅要求稳定）。
	anthropicNative := gatewayanthropicIsNative(httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	geminiNative := gatewaygeminiIsNative(httptest.NewRequest(http.MethodPost, "/v1x-goog", nil))
	if anthropicNative && geminiNative {
		t.Fatal("无原生头的请求不应同时判为原生")
	}
}
