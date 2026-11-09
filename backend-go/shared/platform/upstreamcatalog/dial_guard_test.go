package upstreamcatalog

// SEC-001 回归：模型目录抓取（账户凭据发现）必须消费部署级
// upstreamhttp.DialGuard，且目标校验先于请求发出（覆盖 HTTP(S) 代理分支，
// 代理传输的拨号期守卫只看得到代理主机）。strict 模式阻断回环/私网目标，
// 默认放行语义保持不变。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func TestFetchUpstreamModelIDsAppliesDialGuard(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
	}))
	defer server.Close()

	options := FetchOptions{BaseURL: server.URL, ProtocolCode: "openai", Credential: "sk-guard"}

	options.DialGuard = upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: false}, nil, nil)
	if _, err := FetchUpstreamModelIDs(context.Background(), options); err == nil ||
		!strings.Contains(err.Error(), "上游目标地址不被允许") {
		t.Fatalf("strict guard error = %v, want blocked target", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("strict guard must not reach the upstream, got %d requests", got)
	}

	options.DialGuard = upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}, nil, nil)
	ids, err := FetchUpstreamModelIDs(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("permissive guard ids = %v, want [m1]", ids)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("permissive guard upstream hits = %d, want 1", got)
	}
}
