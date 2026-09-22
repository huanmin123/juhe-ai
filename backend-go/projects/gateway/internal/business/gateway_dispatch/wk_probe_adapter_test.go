package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

// wkLostRenewGate 驱动 RenewForeground 失去租约的分支。
type wkLostRenewGate struct {
	fakeGate
}

func (g *wkLostRenewGate) RenewForeground(context.Context, keymodelruntime.ForegroundPermit) (keymodelruntime.ForegroundPermit, bool, error) {
	return keymodelruntime.ForegroundPermit{}, false, nil
}

func TestAttemptRenewLostAndUnknownSuccess(t *testing.T) {
	gate := &wkLostRenewGate{}
	attempt := &Attempt{gate: gate, permit: keymodelruntime.ForegroundPermit{CapabilityHash: "h", AttemptID: "a"}, cap: dispatchCapability(), attemptID: "a"}
	if ok, err := attempt.Renew(context.Background()); ok || err != nil {
		t.Fatalf("失去租约的 renew 必须 (false,nil): %v %v", ok, err)
	}
	// Unknown 生命周期把失败意图与前台租约一起交给 gate。
	gate2 := &fakeGate{admitted: true}
	attempt2 := &Attempt{gate: gate2, permit: keymodelruntime.ForegroundPermit{CapabilityHash: "h", AttemptID: "a"}, cap: dispatchCapability(), attemptID: "a"}
	if err := attempt2.Unknown(context.Background(), time.UnixMilli(5), "req-1"); err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if gate2.unknown != 1 {
		t.Fatalf("unknown 未记录失败意图: %+v", gate2)
	}
}

func TestCompleteSuccessKeepsReleaseError(t *testing.T) {
	// framing 成功但释放失败：CompleteSuccess 必须保留释放错误。
	attempt := &Attempt{gate: &wkErrReleaseGate{}}
	if err := attempt.CompleteSuccess(context.Background()); err == nil || !strings.Contains(err.Error(), "release boom") {
		t.Fatalf("release 错误被吞掉: %v", err)
	}
}

func TestProbeAdapterLegacyDispatchFallsBackToDefault(t *testing.T) {
	// 历史缺陷：旧版 Dispatch 把字面 nil 的 *http.Client 装入非 nil 的
	// Client 接口（typed nil），绕过 Dispatcher.Dispatch 的 client 判空，
	// 在 Do 上触发 nil 指针 panic。dispatch 现在只在 client 非 nil 时装入
	// 接口，字面 nil 保持 nil 接口并回退到 Dispatcher 的默认 client。
	gate := &fakeGate{admitted: true}
	defaultClient := &countingClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("default"))}}
	adapter := ProbeAdapter{Dispatcher: &Dispatcher{Client: defaultClient, KeyModel: gate}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	response, _, err := adapter.Dispatch(context.Background(), req, dispatchCapability(), "probe-attempt-1")
	if err != nil || response == nil {
		t.Fatalf("字面 nil client 必须回退默认 client: %v", err)
	}
	if defaultClient.calls != 1 {
		t.Fatalf("默认 client 未被使用: %d", defaultClient.calls)
	}
}

func TestProbeAdapterNilTargetClientFallsBackToDefault(t *testing.T) {
	// 生产回归（2026-09-22 快速测试进程崩溃）：模型测试目标无 scoped proxy
	// 时 target.Client 为 nil，DispatchWithClient 收到 nil *http.Client；
	// 装入接口会形成 typed nil panic，必须回退 Dispatcher 的默认 client。
	gate := &fakeGate{admitted: true}
	defaultClient := &countingClient{response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("default"))}}
	adapter := ProbeAdapter{Dispatcher: &Dispatcher{Client: defaultClient, KeyModel: gate}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	response, _, err := adapter.DispatchWithClient(context.Background(), req, dispatchCapability(), "probe-attempt-4", nil)
	if err != nil || response == nil {
		t.Fatalf("nil target client 必须回退默认 client: %v", err)
	}
	if defaultClient.calls != 1 {
		t.Fatalf("默认 client 未被使用: %d", defaultClient.calls)
	}
}

func TestProbeAdapterSettleSuccessWithCircuit(t *testing.T) {
	gate := &fakeGate{admitted: true}
	circuit := &fakeCircuitGate{}
	adapter := ProbeAdapter{Dispatcher: &Dispatcher{Client: fakeClient{response: &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("default"))}}, KeyModel: gate, Circuit: circuit}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, settle, err := adapter.dispatch(context.Background(), req, dispatchCapability(), "probe-attempt-0", fakeHTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	settle(true)
	// once 语义：重复结算必须是 no-op。
	settle(false)
	if gate.released != 1 || gate.unknown != 0 {
		t.Fatalf("成功结算语义不符: %+v", gate)
	}
	if circuit.attempt.framing != 1 || circuit.attempt.unknown != 0 {
		t.Fatalf("成功结算必须上报 framing: %+v", circuit.attempt)
	}
}

// fakeHTTPClient 提供非 nil 的 *http.Client 以满足 DispatchWithClient 签名，
// 实际请求由 fakeClient 顶替（Dispatcher.Client 为 per-request 优先级更低的默认值）。
func fakeHTTPClient() *http.Client { return &http.Client{Transport: fakeRoundTripper{}} }

type fakeRoundTripper struct{}

func (fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestProbeAdapterDispatchWithClientUsesTargetClient(t *testing.T) {
	gate := &fakeGate{admitted: true}
	defaultClient := &countingClient{response: &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("default"))}}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	t.Cleanup(target.Close)
	adapter := ProbeAdapter{Dispatcher: &Dispatcher{Client: defaultClient, KeyModel: gate}}
	req, _ := http.NewRequest(http.MethodGet, target.URL, nil)
	response, settle, err := adapter.DispatchWithClient(context.Background(), req, dispatchCapability(), "probe-attempt-2", target.Client())
	if err != nil || response == nil {
		t.Fatalf("probe: %v", err)
	}
	if defaultClient.calls != 0 {
		t.Fatalf("默认 client 被误用: %d", defaultClient.calls)
	}
	// 失败结算：circuit unknown（本 adapter 未接电路时为中性）+ permit 释放。
	settle(false)
	if gate.released != 1 {
		t.Fatalf("失败结算未释放 permit: %+v", gate)
	}
	// 重复结算 no-op（released 不再增加）。
	settle(true)
	if gate.released != 1 {
		t.Fatalf("once 语义失效: %+v", gate)
	}
}

func TestProbeAdapterNilDispatcherAndTransportError(t *testing.T) {
	if _, _, err := (ProbeAdapter{}).Dispatch(context.Background(), nil, dispatchCapability(), "a"); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("nil dispatcher err=%v", err)
	}
	// 用已关闭的 httptest 端口制造确定性传输错误。
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := server.Client()
	server.Close()
	adapter := ProbeAdapter{Dispatcher: &Dispatcher{KeyModel: &fakeGate{admitted: true}}}
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, settle, err := adapter.DispatchWithClient(context.Background(), req, dispatchCapability(), "probe-attempt-3", client)
	if err == nil {
		t.Fatal("传输错误必须透出")
	}
	if response != nil {
		t.Fatal("失败响应必须为 nil")
	}
	if settle == nil {
		t.Fatal("失败也必须返回可安全调用的结算函数")
	}
	settle(true)
}
