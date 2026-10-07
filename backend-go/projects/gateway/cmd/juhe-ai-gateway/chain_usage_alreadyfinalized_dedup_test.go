package main

// BUG-0290 记账双写回归：gatewayresponse 的 AlreadyFinalized 生产者
//（finalizeStreamFailure / finalizeBufferedJSONProtocolFailure 等）在返回前已
// 自写 usage 失败行并 finalize 审计；chain 层（chain_v1.go handleUpstreamResponse
// 的 finalize 短路）必须凭 handling.AlreadyFinalized 短路，不得再走
// FinalizeHandledUpstreamResponse——否则对上游 HTTP 200 的失败流补一条伪
// success=1（无 usage / 无 first_token / 无错误码）记录，污染成功计数与质量分。
// 契约见 internal/gatewayresponse/handlingresult.go 的三态判定注释。
//
// 夹具沿用既有端到端惯用法（newChainFixture + chainSmokeDeps + httptest 上游）：
// usage 记录经 chainFinalizationUsage → spooledUsageRecorder 异步落 spool
// JSON 文件；两条竞争写入都发生在请求处理器返回前（同步入队），客户端读到
// 响应结尾即代表入队完成，shutdown() 的 recorder.Close() 同步排空缓冲后
// spool 内容即终态。轮询仅作为既有套件惯例的兜底，不承载时序假设。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// alreadyFinalizedSpoolRecords 递归收集 spool 目录下的 usage 记录 JSON。
func alreadyFinalizedSpoolRecords(t *testing.T, spoolDir string) []gatewayusage.UsageRecordInput {
	t.Helper()
	var records []gatewayusage.UsageRecordInput
	err := filepath.WalkDir(spoolDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var record gatewayusage.UsageRecordInput
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("parse spool record %s: %v", path, err)
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatalf("walk spool dir: %v", err)
	}
	return records
}

// alreadyFinalizedAwaitStableRecords 先等待至少 minCount 条记录落盘，再等待
// 记录数在稳定窗口内不再增长（有界轮询，兜底未知异步源；两条竞争写入本身
// 在 shutdown 前已同步入队）。必须在 shutdown() 之后调用。
func alreadyFinalizedAwaitStableRecords(t *testing.T, spoolDir string, minCount int) []gatewayusage.UsageRecordInput {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	lastCount := -1
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		records := alreadyFinalizedSpoolRecords(t, spoolDir)
		if len(records) != lastCount {
			lastCount = len(records)
			lastChange = time.Now()
		}
		if len(records) >= minCount && time.Since(lastChange) >= 250*time.Millisecond {
			return records
		}
		time.Sleep(25 * time.Millisecond)
	}
	return alreadyFinalizedSpoolRecords(t, spoolDir)
}

// alreadyFinalizedAssertSingleFailureRow 是三条用例共用的核心断言：恰好一条
// 记录且为失败行（Success=false、ErrorCode 非空）。修复前伪 success 行使其
// 必红。
func alreadyFinalizedAssertSingleFailureRow(t *testing.T, records []gatewayusage.UsageRecordInput) {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1（BUG-0290：AlreadyFinalized 后 chain 层不得再走 FinalizeHandledUpstreamResponse 补伪成功行）\nrecords=%+v",
			len(records), records)
	}
	record := records[0]
	if record.Success {
		t.Fatalf("usage record Success = true, want false（伪成功行）: %+v", record)
	}
	if record.ErrorCode == "" {
		t.Fatalf("usage record ErrorCode = empty, want 非空失败码: %+v", record)
	}
}

// TestGatewayChainCommittedStreamFailureRecordsSingleUsageRow 是修复前必红的
// 核心回归：上游 HTTP 200 SSE 先提交语义输出（下游已 commit），随后流内
// error 帧触发协议失败——管道经 signalCommittedStreamFailure 收尾，
// finalizeStreamFailure 自写失败行后返回 AlreadyFinalized=true。chain 层缺
// AlreadyFinalized 守卫时会再落一条伪 success=1（无 usage/无 first_token）。
func TestGatewayChainCommittedStreamFailureRecordsSingleUsageRow(t *testing.T) {
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 首个语义事件：内容 delta 提交下游（OutputReceived → 预提交缓冲放行）。
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-dedup\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"部分输出\"},\"finish_reason\":null}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// 流内失败帧：已提交后的协议失败（生产 thinking_signature_invalid 同构）。
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"上游流内签名校验失败\",\"code\":\"thinking_signature_invalid\",\"type\":\"invalid_request_error\"}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL,
			"supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	server := httptest.NewServer(chain)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	// 生产触发面是 precise 客户端（Codex 画像）：generic 客户端对 SSE 保留
	// 不透明语义（clean EOF 即成功，pipefinal.go:361），error 帧不会被解释为
	// 协议失败；codex 画像 InterpretSemantics=true 才走提交后失败 →
	// AlreadyFinalized 臂。
	request.Header.Set("X-Juhe-Client-Profile", "codex")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	// 已提交后中断的流以连接截断收尾：读取到的那部分必须包含已提交的语义
	// 输出；读取错误（unexpected EOF）属于该契约，不视为失败。
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(raw), "部分输出") {
		t.Fatalf("committed semantic output missing from client body: %q", string(raw))
	}
	server.Close()
	// 请求处理器返回即代表两条竞争写入均已同步入队；shutdown 排空缓冲落盘。
	shutdown()

	records := alreadyFinalizedAwaitStableRecords(t, spoolDir, 1)
	alreadyFinalizedAssertSingleFailureRow(t, records)
	if records[0].Stream == nil || !*records[0].Stream {
		t.Errorf("usage record Stream = %v, want true", records[0].Stream)
	}
}

// TestGatewayChainSuccessfulStreamKeepsSingleUsageRow 是绿卫兵：正常成功流式
// 请求恰好一条 Success=true 记录，usage 与 FirstTokenMs 保留——修复的守卫
// 不得破坏正常流的唯一记账点（chain:739 是成功流唯一写行点）。
func TestGatewayChainSuccessfulStreamKeepsSingleUsageRow(t *testing.T) {
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"正常内容\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"total_tokens\":8}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL,
			"supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, contentType, raw := chainV1StreamChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200: %s", status, raw)
	}
	if !strings.Contains(raw, "正常内容") || !strings.Contains(raw, "[DONE]") {
		t.Fatalf("successful stream body incomplete: %q (content-type=%s)", raw, contentType)
	}

	records := alreadyFinalizedAwaitStableRecords(t, spoolDir, 1)
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1\nrecords=%+v", len(records), records)
	}
	record := records[0]
	if !record.Success {
		t.Fatalf("usage record Success = false, want true: %+v", record)
	}
	if record.InputTokens == nil || *record.InputTokens != 3 || record.OutputTokens == nil || *record.OutputTokens != 5 {
		t.Fatalf("usage tokens = %v/%v, want 3/5（usage 必须保留）", record.InputTokens, record.OutputTokens)
	}
	if record.FirstTokenMs == nil {
		t.Fatalf("usage record FirstTokenMs = nil, want set: %+v", record)
	}
}

// TestGatewayChainNonStreamProtocolFailureRecordsSingleUsageRow 覆盖同构双写
// 的非流式生产者（finalizeBufferedJSONProtocolFailure）：上游 HTTP 200 JSON
// 根节点 error 是确凿失败——自写失败行 + AlreadyFinalized=true 后，chain 层
// 不得再补伪成功行。
func TestGatewayChainNonStreamProtocolFailureRecordsSingleUsageRow(t *testing.T) {
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"上游成功响应携带失败终态","code":"thinking_signature_invalid","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL,
			"supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusBadGateway {
		t.Fatalf("status=%d want 502（200 失败终态按协议失败渲染）: %s", status, raw)
	}

	records := alreadyFinalizedAwaitStableRecords(t, spoolDir, 1)
	alreadyFinalizedAssertSingleFailureRow(t, records)
}
