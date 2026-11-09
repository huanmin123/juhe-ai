package main

// SEC-001 回归：xai-grok-usage-refresh 的 billing/settings 端点由账户凭据
// base_url 派生，必须与 /v1 派发、J2、健康探针共用部署级 URL 守卫。
// strict 模式（显式关闭私网放行）下不得发出任何上游请求；默认放行语义不变。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func TestFetchXAIGrokJSONAppliesDialGuard(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"credits":1}`))
	}))
	defer server.Close()

	newRuntime := func(strict bool) *xaiGrokUsageRuntime {
		return &xaiGrokUsageRuntime{
			dialGuard: upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: !strict}, nil, nil),
			nowFunc:   func() time.Time { return time.Now().UTC() },
		}
	}

	strict := newRuntime(true)
	if _, err := strict.fetchXAIGrokJSON(context.Background(), server.Client(), "token", server.URL+"/billing?format=credits"); err == nil ||
		!strings.Contains(err.Error(), "用量端点地址不被允许") {
		t.Fatalf("strict guard error = %v, want blocked target", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("strict guard must not reach the upstream, got %d requests", got)
	}

	permissive := newRuntime(false)
	if _, err := permissive.fetchXAIGrokJSON(context.Background(), server.Client(), "token", server.URL+"/billing?format=credits"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("permissive guard upstream hits = %d, want 1", got)
	}
}
