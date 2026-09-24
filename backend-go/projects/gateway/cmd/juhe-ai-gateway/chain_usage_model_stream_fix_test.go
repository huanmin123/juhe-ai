package main

// 失败尝试 usage 行 Model/Stream 补齐 + 完成尝试 Usage token 全量映射的回归：
//
//   - 失败路（failed_response / transport / downstream_closed 三个分支与
//     engine 侧 adapter）构造 RecordFailedUpstreamAttemptInput 时必须携带
//     请求 model 与 stream 口径（与成功行同为
//     gatewaypreauth.IsOpenAIStreamRequest）；此前恒为空/false。
//   - chainFinalizationUsage.RecordCompletedUpstreamAttempt 必须把
//     CompletedAttemptInput.Usage 的全部 token 字段与响应模型名透传到
//     UsageRecordInput；此前完全未消费，流式成功行 tokens 落 NULL。
//
// 断言经真实 gatewayusage.Service（FinalizationDispatch → capturing
// recorder）往返，观察点为归一化前的 EnqueueUsageRecord 输入。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// newUsageCapturingService 装配真实 gatewayusage.Service：FinalizationDispatch
// 直连 capturingUsageRecorder，测试在 WaitForIdle 后断言入队记录。
func newUsageCapturingService() (*gatewayusage.Service, *gatewayusage.FinalizationDispatch, *capturingUsageRecorder) {
	recorder := &capturingUsageRecorder{}
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, nil, 64, 1)
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{})
	return service, dispatch, recorder
}

func waitForUsageDispatch(t *testing.T, dispatch *gatewayusage.FinalizationDispatch) {
	t.Helper()
	if !dispatch.WaitForIdle(5000) {
		t.Fatal("usage dispatch 5s 内未排空")
	}
}

func modelStreamRequestBody(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	// 预检捕获后请求体才可被 model/stream 读取（与 w1CodexRequest 同形）。
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

// TestChainFailureDispatchFailedResponseUsageCarriesModelAndStream：上游非 2xx
// 分支的失败 usage 行必须携带请求体 model 与 stream 口径。
func TestChainFailureDispatchFailedResponseUsageCarriesModelAndStream(t *testing.T) {
	service, dispatch, recorder := newUsageCapturingService()
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{usage: service, affinity: &failureDispatchAffinity{}}
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":{"message":"rate limited"}}`)
	input := gatewayFailedResponseInput(response, sink, gatewayTrafficSource)
	input.Req = modelStreamRequestBody(t, `{"model":"gpt-test","stream":true}`)

	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	waitForUsageDispatch(t, dispatch)
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "gpt-test" {
		t.Errorf("Model = %q, want gpt-test", record.Model)
	}
	if record.Stream == nil || !*record.Stream {
		t.Errorf("Stream = %v, want true", record.Stream)
	}
}

// TestChainFailureDispatchTransportFailureUsageCarriesModelAndStream：传输失败
// 分支同口径。
func TestChainFailureDispatchTransportFailureUsageCarriesModelAndStream(t *testing.T) {
	service, dispatch, recorder := newUsageCapturingService()
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{usage: service}
	input := upstreamRequestErrorInput(errors.New("connection refused"), sink, nil)
	input.Req = modelStreamRequestBody(t, `{"model":"gpt-test","stream":true}`)

	result, err := dispatcher.HandleUpstreamRequestError(context.Background(), input)
	if err != nil {
		t.Fatalf("handle upstream request error: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	waitForUsageDispatch(t, dispatch)
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "gpt-test" {
		t.Errorf("Model = %q, want gpt-test", record.Model)
	}
	if record.Stream == nil || !*record.Stream {
		t.Errorf("Stream = %v, want true", record.Stream)
	}
}

// TestChainFailureDispatchDownstreamClosedUsageCarriesModelAndStream：下游关闭
// 分支同口径（非流式请求 stream=false 显式落记录）。
func TestChainFailureDispatchDownstreamClosedUsageCarriesModelAndStream(t *testing.T) {
	service, dispatch, recorder := newUsageCapturingService()
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{usage: service}
	input := upstreamRequestErrorInput(
		&gatewaydispatch.UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true},
		sink, nil)
	input.Req = modelStreamRequestBody(t, `{"model":"claude-test"}`)

	result, err := dispatcher.HandleUpstreamRequestError(context.Background(), input)
	if err != nil {
		t.Fatalf("handle upstream request error: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	waitForUsageDispatch(t, dispatch)
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "claude-test" {
		t.Errorf("Model = %q, want claude-test", record.Model)
	}
	if record.Stream == nil || *record.Stream {
		t.Errorf("Stream = %v, want false", record.Stream)
	}
	if record.FailureAttribution != gatewayusage.FailureAttributionDownstreamClosed {
		t.Errorf("FailureAttribution = %q, want downstream_closed", record.FailureAttribution)
	}
}

// TestUsageAttemptRecorderAdapterNilRequestDoesNotPanic：engine 侧 adapter 在
// 并发上限路径（upstreamdispatch.go 传 nil req）不得 panic；nil req 的 model
// 提示为空、stream 按现有 bool 契约落 false。
func TestUsageAttemptRecorderAdapterNilRequestDoesNotPanic(t *testing.T) {
	service, dispatch, recorder := newUsageCapturingService()
	adapter := usageAttemptRecorderAdapter{service: service}

	err := adapter.RecordFailedUpstreamAttempt(context.Background(), nil,
		gatewaypreauth.GatewayFailureUsageContext{TrafficSource: gatewayTrafficSource},
		gatewaydispatch.AccountCandidate{ID: "acc_1", ProviderCode: "openai"},
		gatewaydispatch.FailedAttemptRecord{
			UpstreamURL:        "concurrency:limit",
			StartedAt:          1728000000000,
			ErrorMessage:       "并发上限",
			FailureAttribution: "gateway_capacity",
		})
	if err != nil {
		t.Fatalf("record = %v", err)
	}
	waitForUsageDispatch(t, dispatch)
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Model != "" {
		t.Errorf("Model = %q, want empty for nil request", record.Model)
	}
	if record.Stream == nil || *record.Stream {
		t.Errorf("Stream = %v, want false for nil request", record.Stream)
	}
}

// TestChainFinalizationUsageCompletedAttemptMapsUsageTokens：完成尝试必须全量
// 透传 ParsedUsage 的 token 字段与响应模型名（值透传 + 指针非 nil）。
func TestChainFinalizationUsageCompletedAttemptMapsUsageTokens(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	inputTokens := 12
	outputTokens := 34
	cacheReadTokens := 5
	cacheWriteTokens := 6
	cacheWrite1hTokens := 7
	thinkingTokens := 8
	inputImageTokens := 9
	outputImageTokens := 10
	inputAudioTokens := 11
	outputAudioTokens := 13
	outputImageCount := 2
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext:   gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_done_1", TrafficSource: "gateway"},
		Account:        gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc_done_1"}},
		StatusCode:     200,
		Success:        true,
		Stream:         true,
		RequestedModel: "gpt-test",
		Usage: gatewayproto.ParsedUsage{
			UpstreamResponseModel: "gpt-4.1-2025-04-14",
			ServiceTier:           "priority",
			InputTokens:           &inputTokens,
			OutputTokens:          &outputTokens,
			CacheReadTokens:       &cacheReadTokens,
			CacheWriteTokens:      &cacheWriteTokens,
			CacheWrite1hTokens:    &cacheWrite1hTokens,
			ThinkingTokens:        &thinkingTokens,
			InputImageTokens:      &inputImageTokens,
			OutputImageTokens:     &outputImageTokens,
			InputAudioTokens:      &inputAudioTokens,
			OutputAudioTokens:     &outputAudioTokens,
			OutputImageCount:      &outputImageCount,
		},
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	tokenCases := []struct {
		name string
		got  *int
		want int
	}{
		{"InputTokens", record.InputTokens, inputTokens},
		{"OutputTokens", record.OutputTokens, outputTokens},
		{"CacheReadTokens", record.CacheReadTokens, cacheReadTokens},
		{"CacheWriteTokens", record.CacheWriteTokens, cacheWriteTokens},
		{"CacheWrite1hTokens", record.CacheWrite1hTokens, cacheWrite1hTokens},
		{"ThinkingTokens", record.ThinkingTokens, thinkingTokens},
		{"InputImageTokens", record.InputImageTokens, inputImageTokens},
		{"OutputImageTokens", record.OutputImageTokens, outputImageTokens},
		{"InputAudioTokens", record.InputAudioTokens, inputAudioTokens},
		{"OutputAudioTokens", record.OutputAudioTokens, outputAudioTokens},
		{"OutputImageCount", record.OutputImageCount, outputImageCount},
	}
	for _, tokenCase := range tokenCases {
		if tokenCase.got == nil {
			t.Errorf("%s = nil, want %d", tokenCase.name, tokenCase.want)
			continue
		}
		if *tokenCase.got != tokenCase.want {
			t.Errorf("%s = %d, want %d", tokenCase.name, *tokenCase.got, tokenCase.want)
		}
	}
	if record.UpstreamResponseModel != "gpt-4.1-2025-04-14" {
		t.Errorf("UpstreamResponseModel = %q, want gpt-4.1-2025-04-14", record.UpstreamResponseModel)
	}
	if record.ReportedServiceTier != "priority" {
		t.Errorf("ReportedServiceTier = %q, want priority", record.ReportedServiceTier)
	}
	if record.Model != "gpt-test" {
		t.Errorf("Model = %q, want gpt-test", record.Model)
	}
	if record.Stream == nil || !*record.Stream {
		t.Errorf("Stream = %v, want true", record.Stream)
	}
}

// TestChainFinalizationUsageCompletedAttemptWithoutUsageKeepsTokensNil：无
// usage 证据时 token 字段保持缺失语义（nil → NULL），不得把 0 误当值。
func TestChainFinalizationUsageCompletedAttemptWithoutUsageKeepsTokensNil(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext:   gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_done_2", TrafficSource: "gateway"},
		StatusCode:     200,
		Success:        true,
		Stream:         false,
		RequestedModel: "gpt-test",
		Usage:          gatewayproto.EmptyUsage(),
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.InputTokens != nil || record.OutputTokens != nil ||
		record.CacheReadTokens != nil || record.CacheWriteTokens != nil ||
		record.ThinkingTokens != nil || record.OutputImageCount != nil {
		t.Fatalf("absent usage must stay nil (NULL), got %+v", record)
	}
	if record.UpstreamResponseModel != "" {
		t.Errorf("UpstreamResponseModel = %q, want empty", record.UpstreamResponseModel)
	}
	if record.ReportedServiceTier != "" {
		t.Errorf("ReportedServiceTier = %q, want empty (absent usage must stay NULL)", record.ReportedServiceTier)
	}
}
