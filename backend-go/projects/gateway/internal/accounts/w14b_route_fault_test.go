package accounts

// w14b 路由层故障注入：用失败 DB 的 Store 挂载第二套路由，命中各处理器的
// Store 错误渲染臂（writeError / writeM11ReadError / 内部错误分支）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func TestW14BRouteErrorRenderingArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-rerr", adminID, "w14b-rerr", "active")

	// 失败 DB 的 Store 挂载到独立 kernel。
	boomStore := w14bBoomStore(env)
	k2 := kernel.New(kernel.Options{CompressionDisabled: true})
	(&Deps{Store: boomStore, Auth: env.deps, Sink: env.sink}).Mount(k2)
	server2 := httptest.NewServer(k2.Handler())
	t.Cleanup(server2.Close)

	request := func(method, path, body string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		httpRequest, err := http.NewRequest(method, server2.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			httpRequest.Header.Set("Content-Type", "application/json")
		}
		env.mu.Lock()
		for name, value := range env.jar {
			httpRequest.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		env.mu.Unlock()
		response, err := http.DefaultClient.Do(httpRequest)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(response.Body)
		response.Body.Close()
		return response.StatusCode
	}

	// m11 读路由错误臂。
	for _, path := range []string{
		"/__aisys__/api/accounts/acc-w14b-rerr/advanced",
		"/__aisys__/api/accounts/acc-w14b-rerr/oauth-reauthorization-context",
		"/__aisys__/api/accounts/acc-w14b-rerr/api-key-runtime",
		"/__aisys__/api/accounts/acc-w14b-rerr/balance/details",
	} {
		if code := request(http.MethodGet, path, ""); code < 400 {
			t.Fatalf("%s 应返回错误状态：%d", path, code)
		}
	}
	// m11 写路由错误臂。
	for _, item := range []struct{ method, path, body string }{
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/balance/refresh", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/balance/test-draft", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/force-activate", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/traffic-migration", "{}"},
		{http.MethodPatch, "/__aisys__/api/accounts/acc-w14b-rerr/authorized-dispatch", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/group", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/return-authorization", "{}"},
	} {
		if code := request(item.method, item.path, item.body); code < 400 {
			t.Fatalf("%s 应返回错误状态：%d", item.path, code)
		}
	}
	// 基础路由错误臂。
	for _, item := range []struct{ method, path, body string }{
		{http.MethodGet, "/__aisys__/api/accounts", ""},
		{http.MethodGet, "/__aisys__/api/accounts/options", ""},
		{http.MethodGet, "/__aisys__/api/accounts/tags", ""},
		{http.MethodGet, "/__aisys__/api/accounts/acc-w14b-rerr", ""},
		{http.MethodGet, "/__aisys__/api/accounts/acc-w14b-rerr/clone-context", ""},
		{http.MethodPatch, "/__aisys__/api/accounts/acc-w14b-rerr", "{}"},
		{http.MethodPatch, "/__aisys__/api/accounts/acc-w14b-rerr/tags", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/lock", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/lock-config", "{}"},
		{http.MethodDelete, "/__aisys__/api/accounts/acc-w14b-rerr", ""},
		{http.MethodPost, "/__aisys__/api/accounts/batch-edit-context", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/batch-update", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/import/preview", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/import/confirm", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/export", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/revalidate-api-key-runtime", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/runtime-reset", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/api-key-runtime/revalidate", "{}"},
		{http.MethodGet, "/__aisys__/api/accounts/acc-w14b-rerr/test-options", ""},
		{http.MethodGet, "/__aisys__/api/accounts/acc-w14b-rerr/test-options/models/gpt-4o-mini", ""},
		{http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rerr/test", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/test-draft", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/test-sessions", "{}"},
		{http.MethodGet, "/__aisys__/api/accounts/test-sessions/sess-x", ""},
		{http.MethodPost, "/__aisys__/api/accounts/test-sessions/sess-x/heartbeat", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/test-sessions/sess-x/complete", "{}"},
		{http.MethodPost, "/__aisys__/api/accounts/test-sessions/sess-x/cancel", "{}"},
	} {
		if code := request(item.method, item.path, item.body); code < 400 {
			t.Fatalf("%s %s 应返回错误状态：%d", item.method, item.path, code)
		}
	}
}
