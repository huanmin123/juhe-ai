package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// resetGrokIdentityOverrides 清空两层客户端版本覆盖（进程级 atomic，测试间
// 共享），避免覆盖状态泄漏到同包其他测试。
func resetGrokIdentityOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
}

// newGrokUsageCaptureServer 起一个捕获请求头的 httptest 上游，返回 server
// URL（fetch endpointURL 的基址）、client 与逐请求头快照读取器（handler 与
// 断言方通过互斥锁共享）。
func newGrokUsageCaptureServer(t *testing.T) (string, *http.Client, func() []http.Header) {
	t.Helper()
	var (
		mu      sync.Mutex
		capture []http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capture = append(capture, r.Header.Clone())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":42.5}}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, server.Client(), func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return capture
	}
}

// 无覆盖时两个 Grok 身份头与内置版本逐字一致（请求内身份快照默认路径）。
func TestFetchXAIGrokJSONUsageHeadersMatchEffectiveVersion(t *testing.T) {
	resetGrokIdentityOverrides(t)
	serverURL, client, captured := newGrokUsageCaptureServer(t)
	runtime := &xaiGrokUsageRuntime{nowFunc: func() time.Time { return time.Now().UTC() }}
	body, err := runtime.fetchXAIGrokJSON(context.Background(), client, "token", serverURL+"/billing?format=credits")
	if err != nil {
		t.Fatalf("fetchXAIGrokJSON() err=%v", err)
	}
	if len(body) == 0 {
		t.Fatal("fetchXAIGrokJSON() returned empty body")
	}
	headers := captured()
	if len(headers) != 1 {
		t.Fatalf("captured %d requests, want 1", len(headers))
	}
	want := upstreamidentity.EffectiveGrokCLIVersion()
	if got := headers[0].Get("User-Agent"); got != "xai-grok-workspace/"+want {
		t.Fatalf("User-Agent=%q, want %q", got, "xai-grok-workspace/"+want)
	}
	if got := headers[0].Get("x-grok-client-version"); got != want {
		t.Fatalf("x-grok-client-version=%q, want %q", got, want)
	}
}

// 并发热刷新下同请求两个身份头不再被拆散（请求内身份快照 2026-11-06）：一个
// goroutine 持续替换两层覆盖，反复 GET 断言每次请求的 UA 版本 ==
// x-grok-client-version（旧实现两条独立 EffectiveGrokCLIVersion() 取值链在
// 两次取值之间可被 Set 拆散）。
func TestFetchXAIGrokJSONUsageHeadersSameSourceUnderRefresh(t *testing.T) {
	resetGrokIdentityOverrides(t)
	serverURL, client, captured := newGrokUsageCaptureServer(t)
	runtime := &xaiGrokUsageRuntime{nowFunc: func() time.Time { return time.Now().UTC() }}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		manual := []map[string]string{nil, {"grokCLI": "9.8.7"}}
		for round := 0; ; round++ {
			select {
			case <-stop:
				return
			default:
			}
			upstreamidentity.SetClientVersionOverrides(manual[round%len(manual)])
			upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"grokCLI": "8.7.6"})
		}
	}()
	const fetches = 120
	var fetchErr error
	for round := 0; round < fetches; round++ {
		if _, err := runtime.fetchXAIGrokJSON(context.Background(), client, "token", serverURL+"/billing?format=credits"); err != nil {
			fetchErr = err
			break
		}
	}
	close(stop)
	wg.Wait()
	if fetchErr != nil {
		t.Fatalf("fetchXAIGrokJSON() err=%v", fetchErr)
	}
	headers := captured()
	if len(headers) != fetches {
		t.Fatalf("captured %d requests, want %d", len(headers), fetches)
	}
	for round, requestHeaders := range headers {
		userAgent := requestHeaders.Get("User-Agent")
		clientVersion := requestHeaders.Get("x-grok-client-version")
		if !strings.HasPrefix(userAgent, "xai-grok-workspace/") || strings.TrimPrefix(userAgent, "xai-grok-workspace/") != clientVersion {
			t.Fatalf("round %d: User-Agent %q and x-grok-client-version %q must come from one request-internal snapshot", round, userAgent, clientVersion)
		}
	}
}
