package gatewaypreauth

// w14h 覆盖波次（第四部分）：协议判定回退臂、请求视图 nil 守卫、payload
// 本地化变更臂、Codex 适配器错误识别、DeadlineExceeded 错误臂、黑名单响应
// 回退与解析后封禁路径。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// w14hDeadlineError 只实现 DeadlineExceeded，用于命中独立于 Timeout 的分支。
type w14hDeadlineError struct{}

func (*w14hDeadlineError) Error() string           { return "w14h deadline" }
func (*w14hDeadlineError) DeadlineExceeded() bool  { return true }

// w14hCodexAdapterError 实现 CodexAdapterErrorMarker。
type w14hCodexAdapterError struct{ payload *CodexAdapterValidationError }

func (e *w14hCodexAdapterError) Error() string                        { return "w14h codex adapter" }
func (e *w14hCodexAdapterError) CodexAdapterError() *CodexAdapterValidationError {
	return e.payload
}

func TestW14HProtocolViewAndRequestGuards(t *testing.T) {
	// 非 openai 路径 + 未知协议码 → false。
	chatPath := NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if IsGatewayProtocolNativeRequest(chatPath, "w14h-unknown") {
		t.Fatal("未知协议码不应命中")
	}
	// nil 请求视图守卫。
	var nilRequest *GatewayRequest
	if got := nilRequest.PathAndQuery(); got != "/" {
		t.Fatalf("nil PathAndQuery=%q", got)
	}
	if got := nilRequest.Path(); got != "/" {
		t.Fatalf("nil Path=%q", got)
	}
	if got := gatewayRequestPathAndQuery(nil); got != "/" {
		t.Fatalf("nil 原始请求=%q", got)
	}
	// whitespace-only client ip。
	if _, ok := normalizeClientIP("   "); ok {
		t.Fatal("空白 IP 必须失败")
	}
	// 本地化变更臂：英文消息按状态码替换。
	translated := LocalizedGatewayErrorPayload(GatewayErrorPayloadOf("plain message", "type"), http.StatusInternalServerError)
	if translated.Error.Message == "plain message" {
		t.Fatalf("英文消息应被本地化=%q", translated.Error.Message)
	}
}

func TestW14HAbortAndErrorClassifyArms(t *testing.T) {
	// 仅实现 DeadlineExceeded 的错误。
	if !isTimeoutLikeAbortReason("", &w14hDeadlineError{}) {
		t.Fatal("DeadlineExceeded 错误必须命中")
	}
	// Codex 适配器错误识别。
	payload := &CodexAdapterValidationError{Message: "m", Code: "c", StatusCode: 400, Type: "t"}
	resolved, ok := asValidationOrCodexAdapterError(&w14hCodexAdapterError{payload: payload})
	if !ok || resolved != payload {
		t.Fatalf("适配器错误=%v/%v", resolved, ok)
	}
	// 实现 marker 但 payload 为 nil → 落回校验错误分支。
	none, ok := asValidationOrCodexAdapterError(&w14hCodexAdapterError{})
	if ok || none != nil {
		t.Fatalf("空 payload=%v/%v", none, ok)
	}
	// marshalClientPayload：不可序列化 → 空串。
	if got := marshalClientPayload(make(chan int)); got != "" {
		t.Fatalf("不可序列化=%q", got)
	}
	// wrapped adapter 错误仍可识别。
	wrapped := errors.Join(errors.New("context"), &w14hCodexAdapterError{payload: payload})
	if _, ok := asValidationOrCodexAdapterError(wrapped); !ok {
		t.Fatal("包装的适配器错误必须可识别")
	}
}

func TestW14HRecordEarlyFailureBeforeHeaders(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	request, _, writer := newTestRequest("POST", "/v1/chat/completions")
	// 头未发出 → 直接返回，不派发审计。
	service.recordEarlyGatewayAuthFailure(writer, request)
}

func TestW14HAfterRuntimeBlacklistArms(t *testing.T) {
	ctx := context.Background()
	blockedPolicy := ClientIPPolicyDecision{
		Blocked:         true,
		BlacklistPolicy: &BlacklistPolicy{ID: "w14h-policy", Reason: "abuse", ClientIP: "203.0.113.9", AggregateIPKey: "agg-w14h"},
		NormalizedIP:    &NormalizedClientIP{ClientIP: "203.0.113.9", AggregateIPKey: "agg-w14h"},
	}
	runtimeHit := &fakeRuntimeCache{runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{
		"sk-w14h": {APIKey: validRuntimeRow()},
	}}
	// runtime 解析成功后复查黑名单 → 403。
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = runtimeHit
		s.IPPolicy = &fakeIPPolicy{decision: blockedPolicy}
	})
	request, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	request.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	if _, err := service.ResolveGatewayRuntimeAsync(ctx, writer, request, ResolveGatewayRuntimeOptions{InspectClientIPPolicyAfterRuntime: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("runtime 解析后封禁=%d", recorder.Code)
	}
	// models 路径同样复查。
	modelsService, _, _ := newTestService(t, func(s *Service) {
		s.IPPolicy = &fakeIPPolicy{decision: blockedPolicy}
	})
	modelsRequest, recorder2, writer2 := newTestRequest("GET", "/v1/models")
	modelsRequest.HTTP.Header.Set("authorization", "Bearer sk-w14h")
	apiKey, err := modelsService.ResolveGatewayAPIKeyForModelsAsync(ctx, writer2, modelsRequest, ResolveGatewayRuntimeOptions{InspectClientIPPolicyAfterRuntime: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	_ = apiKey
	if recorder2.Code != http.StatusForbidden {
		t.Fatalf("models 解析后封禁=%d", recorder2.Code)
	}
}

func TestW14HRoutePlanWeightedBindingArms(t *testing.T) {
	service := &Service{}
	weighted := &gatewayruntimecache.GatewayAPIKeyRow{
		RouteStrategyMode: gatewayruntimecache.RouteStrategyModeWeighted,
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "w14h-binding", GroupID: "g2", Status: "active"},
		},
	}
	snapshot, err := service.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
		traceID:      "w14h",
		startedAt:    1,
		groupId:      "g2",
		apiKeyRecord: weighted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WeightedDecisionToken != "w14h-binding" {
		t.Fatalf("加权 token=%q", snapshot.WeightedDecisionToken)
	}
	// anthropic messages 端点族：相对路径补斜杠与 /v1 剥离为空。
	relative := httptest.NewRequest("POST", "/x", nil)
	relative.RequestURI = "messages"
	if got := anthropicMessagesRequestEndpointFamily(&GatewayRequest{HTTP: relative}); got != EndpointFamilyMessages {
		t.Fatalf("相对路径端点族=%q", got)
	}
	v1Only := httptest.NewRequest("POST", "/x", nil)
	v1Only.RequestURI = "/v1"
	if got := anthropicMessagesRequestEndpointFamily(&GatewayRequest{HTTP: v1Only}); got != "" {
		t.Fatalf("仅 v1 前缀=%q", got)
	}
	// kernel 标记写出（SendGatewayErrorResponse 的保留分支底层依赖）。
	marker := &w14hMarkerWriter{}
	NewTrackingWriter(marker).MarkUpstreamError()
	if !marker.marked {
		t.Fatal("上游标记必须转发")
	}
	_ = kernel.UpstreamMarker(nil)
}
