package accountbalance

// SEC-001 回归：余额查询的两个出站分支（直连 / 代理）都必须消费部署级
// upstreamhttp.DialGuard。strict 模式（显式关闭私网放行）下，loopback /
// 私网目标在发出请求前即以非临时诊断拒绝；默认放行语义保持不变。

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// dialGuardScriptedResolver drives ValidateHost's DNS branch deterministically.
type dialGuardScriptedResolver struct {
	addresses []net.IPAddr
}

func (r *dialGuardScriptedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

// dialGuardMockDoer counts upstream requests without opening a socket.
type dialGuardMockDoer struct {
	hits int
}

func (d *dialGuardMockDoer) Do(*http.Request) (*http.Response, error) {
	d.hits++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"remaining":"12.5"}`)),
	}, nil
}

// TestExecuteBalanceQueryMixedResolution is the W55 regression at the balance
// consumer: a polluted resolver answer that mixes a public A record with the
// blocked Teredo AAAA must reach the upstream, while an all-blocked answer must
// still fail with upstream_blocked before any request is sent.
func TestExecuteBalanceQueryMixedResolution(t *testing.T) {
	secret := "dial-guard-mixed-secret"
	input := dialGuardTestInput(t, secret, "https://rebind.example")
	now := time.Now
	doer := &dialGuardMockDoer{}
	resolver := &dialGuardScriptedResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("31.13.112.4")},
		{IP: net.ParseIP("2001::68f4:2eb9")},
	}}
	options := QueryOptions{
		Secret:    secret,
		Client:    doer,
		Now:       func() time.Time { return now() },
		DialGuard: upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: false}, resolver, nil),
	}

	result, err := ExecuteBalanceQuery(context.Background(), input, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" || result.Adapter != Adapter("custom") {
		t.Fatalf("mixed resolution must keep the query: %#v", result)
	}
	if doer.hits != 1 {
		t.Fatalf("mixed resolution upstream hits = %d, want 1", doer.hits)
	}
	if result.Snapshot.RemainingUSD == "" {
		t.Fatalf("mixed resolution snapshot missing remaining: %#v", result.Snapshot)
	}

	resolver.addresses = []net.IPAddr{{IP: net.ParseIP("2001::68f4:2eb9")}}
	result, err = ExecuteBalanceQuery(context.Background(), input, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "upstream_blocked" {
		t.Fatalf("all-blocked error code = %q, want upstream_blocked", result.ErrorCode)
	}
	if result.Temporary {
		t.Fatal("blocked target must not be classified as transient")
	}
	if doer.hits != 1 {
		t.Fatalf("all-blocked upstream hits = %d, want the probe to stop before sending", doer.hits)
	}
}
