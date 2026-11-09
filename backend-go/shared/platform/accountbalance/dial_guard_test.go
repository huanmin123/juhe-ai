package accountbalance

// SEC-001 回归：余额查询的两个出站分支（直连 / 代理）都必须消费部署级
// upstreamhttp.DialGuard。strict 模式（显式关闭私网放行）下，loopback /
// 私网目标在发出请求前即以非临时诊断拒绝；默认放行语义保持不变。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func dialGuardTestInput(t *testing.T, secret, baseURL string) Input {
	t.Helper()
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-guard"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return Input{
		AccountID:       "acct-guard",
		SystemAccountID: "sys-guard",
		InputVersion:    1,
		ConfigRevision:  1,
		Provider:        "openai",
		Type:            "api_key",
		Status:          "active",
		Schedulable:     true,
		BaseURL:         baseURL,
		Config: QueryConfig{
			Adapter:         Adapter("custom"),
			IntervalMinutes: 5,
			Custom:          &CustomConfig{Path: "/balance", RemainingPointer: "/remaining"},
		},
		APIKey:    credential,
		Trigger:   TriggerManual,
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Minute),
	}
}

func TestExecuteBalanceQueryAppliesDialGuard(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"remaining": "12.5"})
	}))
	defer server.Close()

	secret := "dial-guard-secret"
	input := dialGuardTestInput(t, secret, server.URL)
	now := time.Now
	options := QueryOptions{Secret: secret, Now: func() time.Time { return now() }}

	// strict：回环目标必须在任何请求发出前被拒绝。
	options.DialGuard = upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: false}, nil, nil)
	result, err := ExecuteBalanceQuery(context.Background(), input, options)
	if err != nil {
		t.Fatalf("strict guard must report a diagnostic, not a local error: %v", err)
	}
	if result.ErrorCode != "upstream_blocked" {
		t.Fatalf("strict guard error code = %q, want upstream_blocked", result.ErrorCode)
	}
	if result.Temporary {
		t.Fatal("blocked target must not be classified as transient")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("strict guard must not reach the upstream, got %d requests", got)
	}

	// 默认放行（2026-09-19 决策）：行为与改造前一致。
	options.DialGuard = upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}, nil, nil)
	result, err = ExecuteBalanceQuery(context.Background(), input, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" || result.Adapter != Adapter("custom") {
		t.Fatalf("permissive guard must keep the historical query: %#v", result)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("permissive guard upstream hits = %d, want 1", got)
	}
	if result.Snapshot.RemainingUSD == "" {
		t.Fatalf("permissive guard snapshot missing remaining: %#v", result.Snapshot)
	}
}
