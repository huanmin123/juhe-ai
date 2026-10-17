package gatewaybody

// 调用方分级影子观测信号接线测试（调用方分级调度设计 §3/§8 M1）：解析器拒
// 绝面只在本地判定、责任方=调用方（gateway_policy 400「网关请求体无效」）
// 时透传一次 invalid_json 信号；413 尺寸与 408 上传中断（下游断开）属设计
// 排除清单不透传；未装配/身份不可用静默跳过。信号是影子观测：响应状态码
// 与响应体逐字节不变。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycallertier"
)

func newCallerTierTestMiddleware(t *testing.T) (*Middleware, *gatewaycallertier.MemoryStore) {
	t.Helper()
	store := gatewaycallertier.NewMemoryStore(time.Now)
	recorder, err := gatewaycallertier.NewRecorder(store, gatewaycallertier.Metrics{}, nil)
	if err != nil {
		t.Fatalf("构造 caller tier recorder: %v", err)
	}
	m := NewMiddleware(Config{
		CallerTierSignals: recorder,
		GatewayAPIKeyIDOf: func(r *http.Request) string {
			return r.Header.Get("X-Test-Api-Key-Id")
		},
	})
	return m, store
}

// TestHandleParserRejectionRecordsCallerTierInvalidJSONSignal：责任方=调用方
// 的 400 解析器拒绝计一次 invalid_json 信号，响应语义不变。
func TestHandleParserRejectionRecordsCallerTierInvalidJSONSignal(t *testing.T) {
	m, store := newCallerTierTestMiddleware(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Test-Api-Key-Id", "key_1")
	if !m.HandleParserRejection(w, r, &RawBodyParserError{}) {
		t.Fatal("expected handled")
	}
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（影子信号不得改写响应）", w.Result().StatusCode)
	}
	snapshot := store.Snapshot()
	state, ok := snapshot["key_1"]
	if !ok {
		t.Fatalf("缺少 key_1 状态: %v", snapshot)
	}
	if state.DaySignals != 1 || state.DayRequests != 1 {
		t.Fatalf("信号=%d 分母=%d，期望各 1", state.DaySignals, state.DayRequests)
	}
}

// TestHandleParserRejectionSkipsCallerTierSignal：408 下游断开与 413 尺寸超
// 限不在信号清单；身份不可用同样跳过。
func TestHandleParserRejectionSkipsCallerTierSignal(t *testing.T) {
	cases := []struct {
		name        string
		perr        *RawBodyParserError
		apiKeyIDSet bool
	}{
		{name: "408 上传中断不透传", perr: &RawBodyParserError{Type: "request.aborted"}, apiKeyIDSet: true},
		{name: "413 尺寸超限不透传", perr: &RawBodyParserError{Type: "entity.too.large", StatusCode: http.StatusRequestEntityTooLarge, Limit: 67108864}, apiKeyIDSet: true},
		{name: "身份不可用跳过", perr: &RawBodyParserError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, store := newCallerTierTestMiddleware(t)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.apiKeyIDSet {
				r.Header.Set("X-Test-Api-Key-Id", "key_1")
			}
			if !m.HandleParserRejection(w, r, tc.perr) {
				t.Fatal("expected handled")
			}
			if _, err := io.ReadAll(w.Result().Body); err != nil {
				t.Fatal(err)
			}
			if snapshot := store.Snapshot(); len(snapshot) != 0 {
				t.Fatalf("排除清单/无身份不得计信号: %v", snapshot)
			}
		})
	}
}

// TestHandleParserRejectionCallerTierUnwiredSilent：未装配（nil 口或 nil 身
// 份 provider）时拒绝面照常响应，不 panic 不计信号。
func TestHandleParserRejectionCallerTierUnwiredSilent(t *testing.T) {
	recorder, err := gatewaycallertier.NewRecorder(gatewaycallertier.NewMemoryStore(time.Now), gatewaycallertier.Metrics{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		name string
		cfg  Config
	}{
		{name: "无信号口", cfg: Config{GatewayAPIKeyIDOf: func(*http.Request) string { return "key_1" }}},
		{name: "无身份 provider", cfg: Config{CallerTierSignals: recorder}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			m := NewMiddleware(variant.cfg)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if !m.HandleParserRejection(w, r, &RawBodyParserError{}) {
				t.Fatal("expected handled")
			}
			if w.Result().StatusCode != http.StatusBadRequest {
				t.Fatal("未装配时响应语义必须保持不变")
			}
		})
	}
}
