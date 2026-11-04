package gatewaycodex

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// compactHandoffWriter 是可 Hijack 的测试 writer：Hijack 成功后返回
// net.Pipe 一端，Close 由被测代码执行；记录调用次数供断言。
type compactHandoffWriter struct {
	hijackCalls int
	header      http.Header
}

func (w *compactHandoffWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *compactHandoffWriter) Write([]byte) (int, error) { return 0, nil }
func (w *compactHandoffWriter) WriteHeader(int)           {}
func (w *compactHandoffWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijackCalls++
	conn, _ := net.Pipe()
	return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), nil
}

// TestApplyChatBridgeCompactPreflightLockHoldInterruptsConnection：compact
// 摘要预检的锁死 hold 是非终态交接——可 Hijack 时必须真断开且不写 503，
// 同时仍落失败审计（outcome/phase/code 与 503 契约一致）。
func TestApplyChatBridgeCompactPreflightLockHoldInterruptsConnection(t *testing.T) {
	service, _, registry, _, dispatcher := compactPreflightService(t)
	req, _ := seedCompactRequest(t, service.Bridge, registry)
	dispatcher.err = fakeGatewayClientRetryError{}
	sink := &recordedSink{}
	service.Sink = sink
	writer := &compactHandoffWriter{}
	res := gatewaypreauth.NewTrackingWriter(writer)
	audit := &recordedAudit{}

	result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
		Req: req, Res: res, AuditCapture: audit,
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{},
		SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
		Signal:           context.Background(),
	})
	if err != nil || !result.Completed {
		t.Fatalf("result = %+v err %v", result, err)
	}
	if writer.hijackCalls != 1 {
		t.Fatalf("hijackCalls = %d，锁死 hold 必须真断开", writer.hijackCalls)
	}
	if _, ok := sink.lastFailure(); ok {
		t.Fatal("真断开时不得再写 503")
	}
	metadata, ok := audit.lastMetadata("gateway_client_handoff_transport")
	if !ok || metadata["handoff"] != "connection_interrupt" {
		t.Fatalf("handoff metadata = %+v", metadata)
	}
	if len(audit.finalizes) != 1 {
		t.Fatalf("finalize = %+v", audit.finalizes)
	}
	finalize := audit.finalizes[0]
	if finalize.StatusCode != http.StatusServiceUnavailable ||
		finalize.ErrorCode != gatewaypreauth.GatewayStreamClientRetryErrorCode ||
		finalize.ResponsePartType != "gateway_error" {
		t.Fatalf("finalize = %+v", finalize)
	}
}

// TestApplyChatBridgeCompactPreflightLockHoldFallsBackTo503：底层不支持
// Hijack 时必须回退 503，且不能因 TrackingWriter.Hijack 提前置
// wroteHeader 而被吞成空 200。
func TestApplyChatBridgeCompactPreflightLockHoldFallsBackTo503(t *testing.T) {
	service, _, registry, _, dispatcher := compactPreflightService(t)
	req, _ := seedCompactRequest(t, service.Bridge, registry)
	dispatcher.err = fakeGatewayClientRetryError{}
	sink := &recordedSink{}
	service.Sink = sink
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

	result, err := service.ApplyChatBridgeCompactPreflight(context.Background(), CompactPreflightInput{
		Req: req, Res: res, AuditCapture: &recordedAudit{},
		UsageContext:    gatewaypreauth.GatewayFailureUsageContext{},
		SystemAccountID: "sys", APIKeyID: "key", GroupID: "group",
		DispatchAccounts: []gatewayruntimecache.OpenAIAccountSecret{compactBridgeAccount()},
		Signal:           context.Background(),
	})
	if err != nil || !result.Completed {
		t.Fatalf("result = %+v err %v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != http.StatusServiceUnavailable ||
		failure.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("Hijack 不可用必须回退 503 记账：%+v", failure)
	}
	// InterruptClientConnection 不得先经 TrackingWriter.Hijack 污染 wroteHeader：
	// 否则真实 sink 的 503 写出会被 HeadersSent 守卫吞掉。
	if res.HeadersSent() {
		t.Fatal("Hijack 失败不得置 HeadersSent，否则后续 503 会被守卫吞掉")
	}
}
