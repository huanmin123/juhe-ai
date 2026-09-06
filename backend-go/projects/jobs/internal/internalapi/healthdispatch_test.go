package internalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// healthDispatchTestOptions 构造固定密钥与记录型派发回调的 handler。
type healthDispatchRecorder struct {
	mu         sync.Mutex
	accountIDs []string
	reasons    []string
	traceIDs   []string
	fences     []*HealthCheckSourceFence
	outcome    HealthCheckDispatchOutcome
	err        error
}

func (r *healthDispatchRecorder) dispatch(_ context.Context, accountID, reason, traceID string, fence *HealthCheckSourceFence) (HealthCheckDispatchOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accountIDs = append(r.accountIDs, accountID)
	r.reasons = append(r.reasons, reason)
	r.traceIDs = append(r.traceIDs, traceID)
	r.fences = append(r.fences, fence)
	return r.outcome, r.err
}

func (r *healthDispatchRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.accountIDs)
}

const healthDispatchTestSecret = "health-dispatch-test-secret"

func newHealthDispatchTestHandler(recorder *healthDispatchRecorder) http.Handler {
	return NewHealthCheckDispatchHandler(HealthCheckDispatchRouterOptions{
		Secret:   healthDispatchTestSecret,
		Dispatch: recorder.dispatch,
	})
}

// postHealthDispatch 以 loopback 地址 + 健康检查签名域发起 POST。
func postHealthDispatch(t *testing.T, handler http.Handler, secret string, rawBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, FullHealthCheckDispatchPath(), bytes.NewReader(rawBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", CreateHealthCheckDispatchSignature(secret, rawBody))
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	return record
}

func healthDispatchBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	payload := map[string]any{
		"version":   1,
		"accountId": "acc-1",
		"reason":    "request_failure",
	}
	if mutate != nil {
		mutate(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHealthCheckDispatchAcceptsSignedPayload(t *testing.T) {
	recorder := &healthDispatchRecorder{outcome: HealthCheckDispatchOutcome{Outcome: "queued", DecisionCode: "queued", TargetRole: "go-jobs", RequestID: "j1-abc"}}
	handler := newHealthDispatchTestHandler(recorder)
	raw := healthDispatchBody(t, func(payload map[string]any) {
		payload["traceId"] = "trace-9"
		payload["sourceFence"] = map[string]any{
			"state_key":         "state-key-1",
			"account_id":        "acc-1",
			"source_generation": 7,
			"source_fence_id":   "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8",
			"runtime_key":       "availability:acc-1:codex_source_avoidance:r3",
			"probe_generation":  11,
			"config_revision":   3,
		}
	})
	record := postHealthDispatch(t, handler, healthDispatchTestSecret, raw)
	if record.Code != http.StatusAccepted {
		t.Fatalf("签名派发必须 202: %d body=%s", record.Code, record.Body.String())
	}
	if recorder.calls() != 1 {
		t.Fatalf("派发回调必须恰好调用一次: %d", recorder.calls())
	}
	if recorder.accountIDs[0] != "acc-1" || recorder.reasons[0] != "request_failure" || recorder.traceIDs[0] != "trace-9" {
		t.Fatalf("派发参数不一致: %s %s %s", recorder.accountIDs[0], recorder.reasons[0], recorder.traceIDs[0])
	}
	fence := recorder.fences[0]
	if fence == nil {
		t.Fatal("sourceFence 必须传递到派发回调")
	}
	if fence.StateKey != "state-key-1" || fence.AccountID != "acc-1" || fence.SourceGeneration != 7 ||
		fence.SourceFenceID != "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8" ||
		fence.RuntimeKey != "availability:acc-1:codex_source_avoidance:r3" ||
		fence.ProbeGeneration != 11 || fence.ConfigRevision != 3 {
		t.Fatalf("fence 投影不一致: %+v", fence)
	}
	var outcome HealthCheckDispatchOutcome
	if err := json.Unmarshal(record.Body.Bytes(), &outcome); err != nil {
		t.Fatalf("202 响应体必须是 outcome JSON: %v", err)
	}
	if outcome.Outcome != "queued" || outcome.RequestID != "j1-abc" {
		t.Fatalf("outcome 不一致: %+v", outcome)
	}
}

func TestHealthCheckDispatchAcceptsPayloadWithoutFence(t *testing.T) {
	recorder := &healthDispatchRecorder{outcome: HealthCheckDispatchOutcome{Outcome: "queued", DecisionCode: "queued", TargetRole: "go-jobs"}}
	handler := newHealthDispatchTestHandler(recorder)
	record := postHealthDispatch(t, handler, healthDispatchTestSecret, healthDispatchBody(t, nil))
	if record.Code != http.StatusAccepted {
		t.Fatalf("无 fence 派发必须 202: %d", record.Code)
	}
	if recorder.fences[0] != nil {
		t.Fatalf("无 sourceFence 时回调不得收到 fence: %+v", recorder.fences[0])
	}
}

// TestHealthCheckDispatchRejectsTamperedSignature 覆盖篡改签名与签名域混用：
// 账户测试域的签名不得通过健康检查域校验。
func TestHealthCheckDispatchRejectsTamperedSignature(t *testing.T) {
	handler := newHealthDispatchTestHandler(&healthDispatchRecorder{})
	raw := healthDispatchBody(t, nil)
	record := postHealthDispatch(t, handler, "wrong-secret", raw)
	if record.Code != http.StatusUnauthorized {
		t.Fatalf("错误密钥必须 401: %d", record.Code)
	}
	// 域混用：用账户测试签名域签名健康检查 payload。
	request := httptest.NewRequest(http.MethodPost, FullHealthCheckDispatchPath(), bytes.NewReader(raw))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", CreateAccountTestDispatchSignature(healthDispatchTestSecret, raw))
	mixedRecord := httptest.NewRecorder()
	handler.ServeHTTP(mixedRecord, request)
	if mixedRecord.Code != http.StatusUnauthorized {
		t.Fatalf("账户测试域签名不得通过健康检查域校验: %d", mixedRecord.Code)
	}
}

func TestHealthCheckDispatchGuardMatrix(t *testing.T) {
	recorder := &healthDispatchRecorder{}
	handler := newHealthDispatchTestHandler(recorder)
	raw := healthDispatchBody(t, nil)

	nonLoopback := httptest.NewRequest(http.MethodPost, FullHealthCheckDispatchPath(), bytes.NewReader(raw))
	nonLoopback.Header.Set("Content-Type", "application/json")
	nonLoopback.Header.Set("X-Juhe-Ai-Signature", CreateHealthCheckDispatchSignature(healthDispatchTestSecret, raw))
	nonLoopbackRecord := httptest.NewRecorder()
	handler.ServeHTTP(nonLoopbackRecord, nonLoopback)
	if nonLoopbackRecord.Code != http.StatusForbidden {
		t.Fatalf("非 loopback 必须 403: %d", nonLoopbackRecord.Code)
	}

	plain := httptest.NewRequest(http.MethodPost, FullHealthCheckDispatchPath(), bytes.NewReader(raw))
	plain.RemoteAddr = "127.0.0.1:54321"
	plain.Header.Set("X-Juhe-Ai-Signature", CreateHealthCheckDispatchSignature(healthDispatchTestSecret, raw))
	plainRecord := httptest.NewRecorder()
	handler.ServeHTTP(plainRecord, plain)
	if plainRecord.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("缺 JSON Content-Type 必须 415: %d", plainRecord.Code)
	}

	compressed := httptest.NewRequest(http.MethodPost, FullHealthCheckDispatchPath(), bytes.NewReader(raw))
	compressed.RemoteAddr = "127.0.0.1:54321"
	compressed.Header.Set("Content-Type", "application/json")
	compressed.Header.Set("Content-Encoding", "gzip")
	compressed.Header.Set("X-Juhe-Ai-Signature", CreateHealthCheckDispatchSignature(healthDispatchTestSecret, raw))
	compressedRecord := httptest.NewRecorder()
	handler.ServeHTTP(compressedRecord, compressed)
	if compressedRecord.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("压缩请求体必须 415: %d", compressedRecord.Code)
	}

	oversized := strings.Repeat("a", rawBodyLimitBytes+1)
	oversizedRecord := postHealthDispatch(t, handler, healthDispatchTestSecret, []byte(oversized))
	if oversizedRecord.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限请求体必须 413: %d", oversizedRecord.Code)
	}

	getRecord := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, FullHealthCheckDispatchPath(), nil)
	getRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(getRecord, getRequest)
	if getRecord.Code != http.StatusNotFound {
		t.Fatalf("GET 必须 404: %d", getRecord.Code)
	}

	unknownRecord := httptest.NewRecorder()
	unknownRequest := httptest.NewRequest(http.MethodPost, AccountTestDispatchInternalPrefix+"/v1/account-health-check/other", nil)
	unknownRequest.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(unknownRecord, unknownRequest)
	if unknownRecord.Code != http.StatusNotFound {
		t.Fatalf("未知路径必须 404: %d", unknownRecord.Code)
	}
	if recorder.calls() != 0 {
		t.Fatalf("防护矩阵拦截的请求不得触达派发回调: %d", recorder.calls())
	}
}

func TestHealthCheckDispatchValidatesPayload(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"version 缺失", func(p map[string]any) { delete(p, "version") }},
		{"version 非法", func(p map[string]any) { p["version"] = 2 }},
		{"accountId 缺失", func(p map[string]any) { delete(p, "accountId") }},
		{"accountId 空白", func(p map[string]any) { p["accountId"] = "   " }},
		{"accountId 类型错误", func(p map[string]any) { p["accountId"] = 7 }},
		{"reason 缺失", func(p map[string]any) { delete(p, "reason") }},
		{"reason 空白", func(p map[string]any) { p["reason"] = "" }},
		{"traceId 类型错误", func(p map[string]any) { p["traceId"] = 9 }},
		{"sourceFence 非对象", func(p map[string]any) { p["sourceFence"] = "fence" }},
		{"fence state_key 缺失", func(p map[string]any) {
			p["sourceFence"] = map[string]any{"account_id": "acc-1", "source_fence_id": "f", "runtime_key": "r", "source_generation": 1, "probe_generation": 1, "config_revision": 1}
		}},
		{"fence account_id 不一致", func(p map[string]any) {
			p["sourceFence"] = map[string]any{"state_key": "s", "account_id": "acc-2", "source_fence_id": "f", "runtime_key": "r", "source_generation": 1, "probe_generation": 1, "config_revision": 1}
		}},
		{"fence source_generation 非正", func(p map[string]any) {
			p["sourceFence"] = map[string]any{"state_key": "s", "account_id": "acc-1", "source_fence_id": "f", "runtime_key": "r", "source_generation": 0, "probe_generation": 1, "config_revision": 1}
		}},
		{"fence config_revision 小数", func(p map[string]any) {
			p["sourceFence"] = map[string]any{"state_key": "s", "account_id": "acc-1", "source_fence_id": "f", "runtime_key": "r", "source_generation": 1, "probe_generation": 1, "config_revision": 1.5}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := &healthDispatchRecorder{}
			handler := newHealthDispatchTestHandler(recorder)
			record := postHealthDispatch(t, handler, healthDispatchTestSecret, healthDispatchBody(t, testCase.mutate))
			if record.Code != http.StatusBadRequest {
				t.Fatalf("payload 无效必须 400: %d body=%s", record.Code, record.Body.String())
			}
			if recorder.calls() != 0 {
				t.Fatalf("payload 无效不得触达派发回调: %d", recorder.calls())
			}
		})
	}
	t.Run("trailing JSON value rejected", func(t *testing.T) {
		recorder := &healthDispatchRecorder{}
		handler := newHealthDispatchTestHandler(recorder)
		raw := append(healthDispatchBody(t, nil), []byte(` {}`)...)
		record := postHealthDispatch(t, handler, healthDispatchTestSecret, raw)
		if record.Code != http.StatusBadRequest {
			t.Fatalf("trailing JSON value must be 400: %d body=%s", record.Code, record.Body.String())
		}
		if recorder.calls() != 0 {
			t.Fatalf("trailing JSON value must not reach dispatch: %d", recorder.calls())
		}
	})
	t.Run("invalid UTF-8 rejected", func(t *testing.T) {
		recorder := &healthDispatchRecorder{}
		handler := newHealthDispatchTestHandler(recorder)
		raw := append(healthDispatchBody(t, nil), byte(0xff))
		record := postHealthDispatch(t, handler, healthDispatchTestSecret, raw)
		if record.Code != http.StatusBadRequest {
			t.Fatalf("invalid UTF-8 must be 400: %d body=%s", record.Code, record.Body.String())
		}
		if recorder.calls() != 0 {
			t.Fatalf("invalid UTF-8 must not reach dispatch: %d", recorder.calls())
		}
	})
	t.Run("非对象 body", func(t *testing.T) {
		recorder := &healthDispatchRecorder{}
		handler := newHealthDispatchTestHandler(recorder)
		record := postHealthDispatch(t, handler, healthDispatchTestSecret, []byte(`[1,2,3]`))
		if record.Code != http.StatusBadRequest {
			t.Fatalf("非对象 body 必须 400: %d", record.Code)
		}
		if recorder.calls() != 0 {
			t.Fatalf("非对象 body 不得触达派发回调: %d", recorder.calls())
		}
	})
}

func TestHealthCheckDispatchMapsOutcomes(t *testing.T) {
	t.Run("派发能力未装配 503", func(t *testing.T) {
		handler := NewHealthCheckDispatchHandler(HealthCheckDispatchRouterOptions{Secret: healthDispatchTestSecret})
		record := postHealthDispatch(t, handler, healthDispatchTestSecret, healthDispatchBody(t, nil))
		if record.Code != http.StatusServiceUnavailable {
			t.Fatalf("Dispatch nil 必须 503: %d", record.Code)
		}
	})
	t.Run("input_unavailable 503", func(t *testing.T) {
		recorder := &healthDispatchRecorder{outcome: rejectedDispatchOutcome("input_unavailable")}
		record := postHealthDispatch(t, newHealthDispatchTestHandler(recorder), healthDispatchTestSecret, healthDispatchBody(t, nil))
		if record.Code != http.StatusServiceUnavailable {
			t.Fatalf("input_unavailable 必须 503: %d", record.Code)
		}
	})
	t.Run("dispatch_rejected 400", func(t *testing.T) {
		recorder := &healthDispatchRecorder{outcome: rejectedDispatchOutcome("dispatch_rejected")}
		record := postHealthDispatch(t, newHealthDispatchTestHandler(recorder), healthDispatchTestSecret, healthDispatchBody(t, nil))
		if record.Code != http.StatusBadRequest {
			t.Fatalf("dispatch_rejected 必须 400: %d", record.Code)
		}
	})
	t.Run("服务错误 500", func(t *testing.T) {
		recorder := &healthDispatchRecorder{err: errors.New("publish failed")}
		record := postHealthDispatch(t, newHealthDispatchTestHandler(recorder), healthDispatchTestSecret, healthDispatchBody(t, nil))
		if record.Code != http.StatusInternalServerError {
			t.Fatalf("派发错误必须 500: %d", record.Code)
		}
	})
}

// TestHealthCheckDispatchConcurrent 在 -race 下并发派发同一 handler。
func TestHealthCheckDispatchConcurrent(t *testing.T) {
	recorder := &healthDispatchRecorder{outcome: HealthCheckDispatchOutcome{Outcome: "queued", DecisionCode: "queued", TargetRole: "go-jobs"}}
	handler := newHealthDispatchTestHandler(recorder)
	raw := healthDispatchBody(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := postHealthDispatch(t, handler, healthDispatchTestSecret, raw)
			if record.Code != http.StatusAccepted {
				t.Errorf("并发派发必须 202: %d", record.Code)
			}
		}()
	}
	wg.Wait()
	if recorder.calls() != 32 {
		t.Fatalf("并发派发计数不一致: %d", recorder.calls())
	}
}
