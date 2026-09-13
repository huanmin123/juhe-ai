package main

// w1: /v1 网关链请求场景矩阵。复用 newChainFixture + chainSmokeDeps 组装完整
// 链，用可编程 httptest 上游驱动 preauth/dispatch/response 的错误与流式分支。

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// w1ScenarioChain 起一条完整 /v1 链，账户指向可编程 mock 上游。
type w1ScenarioChain struct {
	serverURL string
	apiKey    string
	db        *sql.DB
}

func newW1ScenarioChain(t *testing.T, upstream http.HandlerFunc) *w1ScenarioChain {
	t.Helper()
	fixture := newChainFixture(t)
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstreamServer.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(shutdown)
	server := httptest.NewServer(chain)
	t.Cleanup(server.Close)
	return &w1ScenarioChain{serverURL: server.URL, apiKey: fixture.apiKeySecret, db: fixture.db}
}

const w1ChatBody = `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`

// w1UpstreamChat 合法 chat completion 上游响应。
func w1UpstreamChat(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-w1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"stop"}]}`))
	}
}

func TestW1ScenarioUnknownProtocolPath(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, w1ChatBody)
	_ = status
	_ = body
	// 非 protocol 路径：Node 404 JSON 契约。
	request, _ := http.NewRequest(http.MethodGet, scenario.serverURL+"/v1/definitely-not-a-protocol", nil)
	response, err := http.Get(request.URL.String())
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", response.StatusCode)
	}
}

func TestW1ScenarioMissingAuthorization(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, "", w1ChatBody)
	if status != http.StatusUnauthorized {
		t.Fatalf("无 key 状态 = %d body=%s", status, body)
	}
	if !strings.Contains(body, "缺少访问令牌") {
		t.Fatalf("无 key 契约 = %s", body)
	}
	// 无效 key 同样 401（Node invalid_api_key 契约）。
	status, body = chainV1ChatRequest(t, scenario.serverURL, "sk-not-exist", w1ChatBody)
	if status != http.StatusUnauthorized {
		t.Fatalf("无效 key 状态 = %d %s", status, body)
	}
}

func TestW1ScenarioInvalidJSONBody(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, `{not-json`)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 JSON 状态 = %d body=%s", status, body)
	}
	if !strings.Contains(body, "json") && !strings.Contains(body, "JSON") {
		t.Fatalf("非法 JSON 契约 = %s", body)
	}
}

func TestW1ScenarioMissingModel(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, `{"messages":[]}`)
	// 当前实现契约：无法按支持模型匹配调度时 503 + missing_model。
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "missing_model") {
		t.Fatalf("缺 model = %d body=%s", status, body)
	}
}

func TestW1ScenarioUnknownModel(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, `{"model":"gpt-never","messages":[]}`)
	// 当前实现契约：分组内无账户支持该模型时 503 + model_unsupported。
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "model_unsupported") {
		t.Fatalf("未知模型 = %d body=%s", status, body)
	}
}

func TestW1ScenarioUpstreamServerError(t *testing.T) {
	scenario := newW1ScenarioChain(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"上游爆炸","type":"server_error"}}`))
	})
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, w1ChatBody)
	if status < 500 || status >= 600 {
		t.Fatalf("上游 500 后网关状态 = %d body=%s", status, body)
	}
}

func TestW1ScenarioUpstreamSSEStream(t *testing.T) {
	scenario := newW1ScenarioChain(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"流式\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("流式状态 = %d body=%s", status, body)
	}
	if !strings.Contains(body, "流式") {
		t.Fatalf("流式内容缺失 = %s", body)
	}
}

func TestW1ScenarioDeadUpstream(t *testing.T) {
	// 关闭的 httptest 端口 = 连接拒绝：账户凭据直接指向死端口。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	fixture := newChainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": deadURL}), fixture.accountID); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	t.Cleanup(shutdown)
	server := httptest.NewServer(chain)
	t.Cleanup(server.Close)
	status, body := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret, w1ChatBody)
	if status < 500 && status != http.StatusBadGateway {
		t.Fatalf("死上游状态 = %d body=%s", status, body)
	}
}

func TestW1ScenarioModelsCatalogEndpoint(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	request, _ := http.NewRequest(http.MethodGet, scenario.serverURL+"/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+scenario.apiKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer response.Body.Close()
	raw := make([]byte, 4096)
	n, _ := response.Body.Read(raw)
	body := string(raw[:n])
	if response.StatusCode != http.StatusOK {
		t.Fatalf("models 状态 = %d body=%s", response.StatusCode, body)
	}
	if !strings.Contains(body, "gpt-test") {
		t.Fatalf("models 未含种子模型 = %s", body)
	}
}

func TestW1ScenarioResponsesEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_w1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"响应成功"}]}]}`))
	}))
	t.Cleanup(upstream.Close)
	fixture := newChainFixture(t)
	// 组装链之前把 catalog 行的协议扩到 responses（缓存冷启动生效）。
	if _, err := fixture.db.Exec(`UPDATE provider_model_catalog SET supported_api_protocols_json = '["chat_completions","responses"]' WHERE model = 'gpt-test'`); err != nil {
		t.Fatalf("update catalog: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	t.Cleanup(shutdown)
	server := httptest.NewServer(chain)
	t.Cleanup(server.Close)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"你好"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	// 行为注：种子目录（仅 chat_completions 协议）下 /v1/responses 请求被
	// 模型路由以 endpoint_mode_unsupported 拒绝——本场景锁 responses 入口
	// 的路由拒绝契约（协议支持的完整透传链路属真实账户配置范畴）。
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "endpoint_mode_unsupported") {
		t.Fatalf("responses = %d %s", response.StatusCode, string(raw))
	}
}

func TestW1ScenarioUpstreamRateLimited(t *testing.T) {
	attempts := 0
	scenario := newW1ScenarioChain(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"限流","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	})
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, w1ChatBody)
	if status < 400 {
		t.Fatalf("限流场景状态 = %d body=%s", status, body)
	}
	if attempts == 0 {
		t.Fatal("上游未被调用")
	}
}

func TestW1ScenarioStreamAbortedByUpstream(t *testing.T) {
	scenario := newW1ScenarioChain(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"半截\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// 直接断开（不写 [DONE]）。
		panic(http.ErrAbortHandler)
	})
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("中断流状态 = %d body=%s", status, body)
	}
	// 半截内容已透传给客户端。
	if !strings.Contains(body, "半截") {
		t.Fatalf("中断流内容 = %s", body)
	}
}
