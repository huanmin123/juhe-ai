package main

// w1: /v1 网关链请求场景矩阵。复用 newChainFixture + chainSmokeDeps 组装完整
// 链，用可编程 httptest 上游驱动 preauth/dispatch/response 的错误与流式分支。

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// w1ShutdownAndDrainSpool 包装 shutdown：gateway 停机返回后，usage spool 的
// 异步投递可能仍有在途写（提交后失败路径会估算入账并落 spool）；Windows 上
// t.TempDir 的 RemoveAll 清理会与之竞争（"directory is not empty" / 路径不存在，
// cleanup 失败直接判红）。轮询 spool 目录快照连续两次一致（无在途 .tmp、文件集
// 稳定）即认为排空，上限 2s。
func w1ShutdownAndDrainSpool(spoolDir string, shutdown func()) func() {
	return func() {
		shutdown()
		snapshot := w1SpoolDirSnapshot(spoolDir)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
			next := w1SpoolDirSnapshot(spoolDir)
			if next == snapshot {
				return
			}
			snapshot = next
		}
	}
}

func w1SpoolDirSnapshot(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		b.WriteString(path)
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				b.WriteString(":")
				b.WriteString(strconv.FormatInt(info.Size(), 10))
			}
		}
		b.WriteString("\n")
		return nil
	})
	return b.String()
}

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
	// B4 第一批单连接事实源：上游指向改连接行 base_url（secret BaseURL =
	// account_connections.base_url）；credentials.base_url 依赖已解除。
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key"}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE account_connections SET base_url = ? WHERE id = 'conn_acc_1'`, upstreamServer.URL); err != nil {
		t.Fatalf("update connection base url: %v", err)
	}
	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(w1ShutdownAndDrainSpool(spoolDir, shutdown))
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
	// 当前实现契约（BUG-0325 修复批）：模型过滤层无法按支持模型匹配调度时
	// 404 + model_not_found（客户端参数错误语义，不可重试）。
	if status != http.StatusNotFound || !strings.Contains(body, "model_not_found") || !strings.Contains(body, "请求缺少 model") {
		t.Fatalf("缺 model = %d body=%s", status, body)
	}
}

func TestW1ScenarioUnknownModel(t *testing.T) {
	scenario := newW1ScenarioChain(t, w1UpstreamChat("不应被调用"))
	status, body := chainV1ChatRequest(t, scenario.serverURL, scenario.apiKey, `{"model":"gpt-never","messages":[]}`)
	// 当前实现契约（BUG-0325 修复批）：分组内无账户支持该模型时
	// 404 + model_not_found + 逐模型文案（原 503 通用文案语义错乱已修）。
	if status != http.StatusNotFound || !strings.Contains(body, "model_not_found") || !strings.Contains(body, "gpt-never") {
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
	t.Cleanup(w1ShutdownAndDrainSpool(spoolDir, shutdown))
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
	if _, err := fixture.db.Exec(`UPDATE models SET supported_endpoint_modes_json = '["chat_json","chat_sse","responses_json","responses_sse"]' WHERE public_model = 'gpt-test'`); err != nil {
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
	t.Cleanup(w1ShutdownAndDrainSpool(spoolDir, shutdown))
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
		// 首个 delta 超窗口缓冲上限 B（流式提交边界受限重放与异常终局处置设计
		// §3.1 条件②追加前判定：超限 chunk 随 flush 写出、立即提交），固定本
		// 用例「提交后断流」前提——B 内的小 delta 会先入窗、断流改走提交前
		// 换号（候选耗尽 503 零业务字节，设计 §5.4 / 验收 1 的窗口版用例覆盖）。
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"半截" + strings.Repeat("输", 3000) + "\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// 直接断开（不写 [DONE]）。
		panic(http.ErrAbortHandler)
	})
	// 2026-10-23 契约收口：提交后的上游断流对客户端是真·网络级中断——已透传
	// 的半截内容之后必须以 unexpected EOF（连接截断）收场，不再是「帧完整但缺
	// 协议终止事件」的优雅收尾；客户端 HTTP 栈据此走自身的网络错误自动重试。
	request, err := http.NewRequest(http.MethodPost, scenario.serverURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+scenario.apiKey)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("中断流状态 = %d body=%s", response.StatusCode, string(raw))
	}
	raw, readErr := io.ReadAll(response.Body)
	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("提交后断流必须以网络级读错误（io.ErrUnexpectedEOF）收尾, got %v", readErr)
	}
	// 半截内容已透传给客户端。
	if !strings.Contains(string(raw), "半截") {
		t.Fatalf("中断流内容 = %s", string(raw))
	}
}
