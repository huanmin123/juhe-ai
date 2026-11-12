package gatewaydispatch

// 客户端断开后的换号重试闸门（与门户实现对齐）：首个候选尝试后若原始客户端
// ctx 已取消，停止后续候选，避免为无人接收的请求重复消耗上游。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDispatchStopsAccountRetryAfterClientCancel(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	defer cancelClient()

	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		cancelClient()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	defer second.Close()

	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{1, 1, 1}
	driver.urlByAccount = map[string][]string{
		"a-1": {first.URL + "/v1/chat/completions"},
		"a-2": {second.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.Settings.SameAccountRetryFastFailMaxMs = 0
	args.Signal = context.WithoutCancel(clientCtx)
	args.ClientSignal = clientCtx

	_, _ = engine.FetchFirstAvailableUpstream(context.Background(), args)

	if firstCalls.Load() != 1 {
		t.Fatalf("first account calls = %d, want 1", firstCalls.Load())
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("client canceled must stop account retry, second calls = %d", secondCalls.Load())
	}
}
