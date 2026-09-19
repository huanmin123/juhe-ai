package main

// w1（单元层）：用量适配器（G17）——usageAttemptRecorderAdapter 经真实
// gatewayusage.Service 记录失败尝试；attemptStatusCodeOf / 上下文投影已
// 在前批覆盖，此处打通服务装配与记录调用。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"log/slog"
	"os"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func TestW1UsageAttemptRecorderRoundTrip(t *testing.T) {
	spool := newUsageSpool(t.TempDir(), gatewaypreauth.SystemClock{}, newTestSlogLogger(), usageSpoolCapacity{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4096, Logger: newTestSlogLogger()}, spool)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, spoolOverflow{spool: spool}, 0, 0)
	dispatch.OverflowEnabled = spool != nil
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithClock(gatewaypreauth.SystemClock{})
	adapter := usageAttemptRecorderAdapter{service: service}
	request := gatewaypreauth.NewGatewayRequest(gatewaypreauthHTTPPost())
	usageContext := gatewaypreauth.GatewayFailureUsageContext{
		TrafficSource: gatewayTrafficSource, SystemAccountID: "sys_1",
		APIKeyID: "key_1", GroupID: "grp_1", Endpoint: "chat_completions",
	}
	err := adapter.RecordFailedUpstreamAttempt(context.Background(), request, usageContext,
		gatewaydispatch.AccountCandidate{ID: "acc_1", ProviderCode: "openai"},
		gatewaydispatch.FailedAttemptRecord{
			UpstreamURL: "https://u/v1/chat/completions", StartedAt: 1728000000000,
			HasStatusCode: true, StatusCode: 502, ErrorMessage: "上游 502",
			InterpretUpstreamSemantics: boolPtr(true),
		})
	if err != nil {
		t.Fatalf("record = %v", err)
	}
	// usageDispatchAdapter：真实 recorder（spool 化）派发一条用量记录。
	usageDispatchAdapter{service: service, recorder: recorder}.DispatchUsageRecord(
		gatewayresponse.ModelsUsageDispatchInput{
			UsageContext: gatewaypreauth.GatewayFailureUsageContext{
				SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "grp_1",
			},
			ProviderCode: "openai", Success: true, StatusCode: 200,
			FirstTokenMs: 120, DurationMs: 800, Stream: true,
		})
	// 后台 dispatch worker 异步写 spool 文件；先等 dispatch 队列排空，再
	// Close recorder 排空其 4096 缓冲（Persist 全部落盘），之后才能让
	// t.TempDir 清理，否则与 RemoveAll 竞争（间歇性 "directory is not empty"）。
	if !dispatch.WaitForIdle(5000) {
		t.Fatal("usage dispatch 5s 内未排空")
	}
	recorder.Close()
}

func newTestSlogLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func gatewaypreauthHTTPPost() *http.Request {
	return httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
}
