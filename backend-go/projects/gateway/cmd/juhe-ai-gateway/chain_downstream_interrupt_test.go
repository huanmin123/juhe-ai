package main

// 2026-10-23 契约收口的两个守卫面：
//  1. helper 单测：newDownstreamInterrupt 的 Hijack+Close 真断开装配语义
//    （幂等、销毁标记、bufio 残留冲净、Hijack 失败降级不 panic）；
//  2. 链级契约：两账户候选 + mock 上游「流式部分输出后提交失败」场景——
//    客户端必须以网络级读错误（io.ErrUnexpectedEOF）结束，且只有一次上游
//    请求（提交后不再换号：writableEndedOf 门阻止向死连接派发新候选）。

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// helper 单测：假 Hijacker ResponseWriter + 可观测假 net.Conn
// ---------------------------------------------------------------------------

// interruptFakeConn 记录 Close 次数的假 net.Conn（w12h w12hFakeConn 同模式）。
type interruptFakeConn struct {
	closeCalls int
}

func (c *interruptFakeConn) Read(p []byte) (int, error)       { return 0, io.EOF }
func (c *interruptFakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *interruptFakeConn) Close() error                     { c.closeCalls++; return nil }
func (c *interruptFakeConn) LocalAddr() net.Addr              { return nil }
func (c *interruptFakeConn) RemoteAddr() net.Addr             { return nil }
func (c *interruptFakeConn) SetDeadline(time.Time) error      { return nil }
func (c *interruptFakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *interruptFakeConn) SetWriteDeadline(time.Time) error { return nil }

// interruptFakeWriter 实现 http.Hijacker 的假 ResponseWriter：记录 Hijack 次
// 数，可注入 Hijack 错误；Hijack 成功时返回假连接 + 带残留字节的 bufio
// ReadWriter（写侧绑到共享 sink，用于断言 interrupt 冲净了 bufio 残留）。
type interruptFakeWriter struct {
	hijackCalls int
	hijackErr   error
	conn        *interruptFakeConn
	sink        *strings.Builder
}

func (w *interruptFakeWriter) Header() http.Header         { return http.Header{} }
func (w *interruptFakeWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *interruptFakeWriter) WriteHeader(int)             {}

func (w *interruptFakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijackCalls++
	if w.hijackErr != nil {
		return nil, nil, w.hijackErr
	}
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(w.sink))
	// 预置 bufio 写侧残留：真连接上这对应尚未冲到 socket 的响应字节。
	_, _ = rw.WriteString("residual")
	return w.conn, rw, nil
}

// TestNewDownstreamInterruptHijacksAndClosesOnce：interrupt 首次调用恰好一次
// Hijack+Close，bufio 残留被冲净，销毁与 End 标记置位；第二次调用幂等。
func TestNewDownstreamInterruptHijacksAndClosesOnce(t *testing.T) {
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	res := gatewaypreauth.NewTrackingWriter(fake)
	interrupt, destroyed := newDownstreamInterrupt(res)

	if destroyed() {
		t.Fatal("destroyed must be false before interrupt")
	}

	interrupt()

	if fake.hijackCalls != 1 {
		t.Fatalf("hijack calls = %d, want 1", fake.hijackCalls)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("conn close calls = %d, want 1", conn.closeCalls)
	}
	if got := fake.sink.String(); got != "residual" {
		t.Fatalf("bufio residual not flushed before close: %q", got)
	}
	if !destroyed() {
		t.Fatal("destroyed must be true after interrupt")
	}
	if !res.WritableEnded() {
		t.Fatal("res.WritableEnded() must be true after interrupt (End 标记使 writableEndedOf 门立即生效)")
	}

	// 幂等：第二次调用不得二次 Hijack / Close。
	interrupt()
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("interrupt must be idempotent: hijack=%d close=%d", fake.hijackCalls, conn.closeCalls)
	}
}

// TestNewDownstreamInterruptHijackFailureDegradesGracefully：Hijack 失败（底层
// 不支持/已劫持）时降级为优雅收尾——不 panic、不触碰连接，销毁与 End 标记仍
// 置位（守卫语义不依赖 Hijack 成败）。
func TestNewDownstreamInterruptHijackFailureDegradesGracefully(t *testing.T) {
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}, hijackErr: errors.New("底层 writer 未实现 http.Hijacker")}
	res := gatewaypreauth.NewTrackingWriter(fake)
	interrupt, destroyed := newDownstreamInterrupt(res)

	interrupt()
	interrupt()

	if conn.closeCalls != 0 {
		t.Fatalf("failed hijack must not touch the conn: close calls = %d", conn.closeCalls)
	}
	if !destroyed() || !res.WritableEnded() {
		t.Fatalf("marked state must survive hijack failure: destroyed=%v writableEnded=%v", destroyed(), res.WritableEnded())
	}
}

// ---------------------------------------------------------------------------
// 链级契约：提交后失败 → 网络级断开 + 不换号
// ---------------------------------------------------------------------------

// TestGatewayChainCommittedStreamFailureInterruptsWithoutAccountSwitch：候选 1
// 流式提交语义输出后流内失败（codex 画像解释为协议失败）→ 网关中断连接——
// 客户端已收到的部分输出之后是 unexpected EOF（真·网络级断开，不是干净
// EOF）；writableEndedOf 门使循环不再向候选 2 派发（只有一次上游请求）。
func TestGatewayChainCommittedStreamFailureInterruptsWithoutAccountSwitch(t *testing.T) {
	fixture := newChainFixture(t)
	var upstreamHits, failKeyHits, okKeyHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer sk-upstream-account-key":
			// 候选 1：先提交语义输出，再发流内失败帧，随后 EOF。
			failKeyHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-cut\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"部分输出\"},\"finish_reason\":null}]}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"上游流内签名校验失败\",\"code\":\"thinking_signature_invalid\",\"type\":\"invalid_request_error\"}}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case "Bearer sk-upstream-account-key-2":
			// 候选 2：可正常完成的流——提交后中断契约下必须零命中。
			okKeyHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-next\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"候选二内容\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	// 候选 2：acc_2 同上游 OK 凭据，local_priority=10 排在 acc_1 之后。
	if _, err := fixture.db.Exec(`INSERT INTO accounts (id, system_account_id, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model) VALUES ('acc_2', ?, 'prof_1', 'openai', 'v1', '账户二', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key-2", "base_url": upstream.URL})); err != nil {
		t.Fatalf("seed acc_2: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_priority, created_at) VALUES (?, ?, 'acc_2', 1, 10, '2026-09-04T00:00:00.000Z')`,
		fixture.groupID, fixture.systemAccount); err != nil {
		t.Fatalf("seed acc_2 group binding: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES ('acc_2', 'gpt-test', 'mdl_gpt-test', '2026-09-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed acc_2 models: %v", err)
	}
	// B4 第一批单连接事实源：acc_2 需要 enabled 连接行（含 chat_sse 模式）才能水合。
	chainSeedAccountConnection(t, fixture.db, "acc_2", upstream.URL, "chat_json", "chat_sse")
	// 候选 1：上游指向同步改凭据与连接行。
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL,
			"supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update acc_1 credentials: %v", err)
	}
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL, "chat_json", "chat_sse")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	// codex 画像 InterpretSemantics=true：提交后的流内失败帧被解释为协议失败，
	// 走提交后中断契约（generic 画像对 SSE 保留不透明语义，不触发本契约）。
	request.Header.Set("X-Juhe-Client-Profile", "codex")
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d want 200", response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type=%q want text/event-stream", response.Header.Get("Content-Type"))
	}

	// 真断开契约：读取以 io.ErrUnexpectedEOF 结束（FIN、无 chunked 终止帧），
	// 而不是干净 EOF（帧完整但缺协议终止事件的旧优雅收尾形态）。
	raw, readErr := io.ReadAll(response.Body)
	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("提交后失败流必须以网络级读错误（io.ErrUnexpectedEOF）收尾, got %v (body=%q)", readErr, string(raw))
	}
	if !strings.Contains(string(raw), "部分输出") {
		t.Fatalf("已提交的部分输出必须先于断开到达客户端: %q", string(raw))
	}
	if strings.Contains(string(raw), "上游流内签名校验失败") || strings.Contains(string(raw), `{"error"`) {
		t.Fatalf("客户端不得收到失败载荷（含网关补发形态）: %q", string(raw))
	}

	// 提交后不换号：候选 1 恰好一次请求，候选 2 零命中，上游总量为 1。
	if failKeyHits.Load() != 1 {
		t.Fatalf("candidate-1 hits = %d, want 1", failKeyHits.Load())
	}
	if okKeyHits.Load() != 0 {
		t.Fatalf("candidate-2 hits = %d, want 0（提交后中断不得换号重试）", okKeyHits.Load())
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1（提交后失败只有一次上游请求）", upstreamHits.Load())
	}
}
