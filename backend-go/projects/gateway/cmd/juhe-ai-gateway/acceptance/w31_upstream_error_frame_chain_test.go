// W31 上游流式失败帧拦截全链验收（契约：docs/functions/上游流式失败帧拦截
// 设计.md）：mock 上游以带内顶层 error 对象帧（无 event: error、JSON 无
// type，DeepSeek 生产事故形态）表达失败时——
//   - 场景 A（提交后）：客户端永不收到上游错误载荷帧，流被网关中断，审计
//     attempts 记 error_phase=stream 且 error_message 为上游原文；
//   - 场景 B（提交前）：error 帧为候选 1 SSE 首个事件 → 失败交回调度层换
//     候选，客户端最终收到候选 2 的正常完整流。
package acceptance

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// w31IncidentErrorFrame 是生产事故同款带内失败帧原文形态（设计 §1）。
var w31IncidentErrorFrame = "data: {\"error\":{\"message\":\"upstream sent an error event\",\"type\":\"upstream_error\"}}\n\n"

const w31OKUpstreamKey = "sk-w31-ok-upstream"

// w31ChatStreamBody 组装正常 chat SSE 流（delta 内容 + finish + [DONE]）。
func w31ChatStreamBody(deltaContent string) []byte {
	return []byte(
		"data: " + sseChunk(`{"id":"chatcmpl-w31","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"`+deltaContent+`"},"finish_reason":null}]}`) + "\n\n" +
			"data: " + sseChunk(`{"id":"chatcmpl-w31","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`) + "\n\n" +
			"data: [DONE]\n\n")
}

// w31PostStreamChat 以默认 key 发起 /v1/chat/completions 流式请求，读取全部
// 可达字节并把终态读错误带回调用方：2026-10-23 契约收口后，提交后失败场景的
// 读取必须以网络级错误（io.ErrUnexpectedEOF）结束——该错误是断言对象而非失败；
// 提交前换号场景则以干净 EOF 收尾（readErr == nil）。
func w31PostStreamChat(t *testing.T, baseURL, key, userContent string) (int, string, string, error) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}`, acceptanceModel, userContent)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Content-Type"), string(body), readErr
}

// 场景 A（提交后）：mock 上游 200 SSE，先正常 delta，再发事故同款 error 对象
// 帧，随后 EOF（无 event: error、无 [DONE]）。断言：客户端收到的全部 SSE 字节
// 不含 error 载荷、流以网络级读错误截断收尾（真断开：Hijack 后 Close，无
// chunked 终止帧——干净 EOF 属「帧完整但缺协议终止事件」的契约回退）；审计
// attempts 该次 error_phase=stream 且 error_message 为上游原文。
func TestW31StreamTopLevelErrorFrameAfterCommitNeverReachesClient(t *testing.T) {
	upstream := startChainFixtureWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+upstreamAccountKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-w31a","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"W31-DELTA-ONE"},"finish_reason":null}]}`) + "\n\n"))
		_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-w31a","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"content":"W31-DELTA-TWO"},"finish_reason":null}]}`) + "\n\n"))
		_, _ = w.Write([]byte(w31IncidentErrorFrame))
	})
	base := upstream.fixture.baseURL

	status, contentType, body, readErr := w31PostStreamChat(t, base, upstream.defaultKey, "W31场景A")
	if status != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", status, body)
	}
	if !contains(contentType, "text/event-stream") {
		t.Fatalf("content-type=%q", contentType)
	}
	// 真断开契约（2026-10-23 收口）：提交后失败以网络级读错误收场，客户端
	// HTTP 栈据此走自身的网络错误自动重试，而不是把截断流当完整响应消费。
	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("提交后失败流必须以网络级读错误（io.ErrUnexpectedEOF）收尾, got %v (body=%q)", readErr, body)
	}
	if !contains(body, "W31-DELTA-ONE") || !contains(body, "W31-DELTA-TWO") {
		t.Fatalf("提交前的正常 delta 应已透传客户端: %s", body)
	}
	if contains(body, "upstream sent an error event") {
		t.Fatalf("客户端收到了上游错误载荷原文: %s", body)
	}
	if contains(body, `{"error"`) {
		t.Fatalf("客户端收到了 error 对象帧（含网关补发形态）: %s", body)
	}

	// 审计 attempts：error_phase=stream + 上游错误原文（异步落库，轮询）。
	auditPath := upstream.fixture.root + "/storage/audit.sqlite3"
	deadline := time.Now().Add(20 * time.Second)
	matched := false
	for time.Now().Before(deadline) {
		db, err := sql.Open("sqlite", "file:"+auditPath+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatalf("open audit db: %v", err)
		}
		var count int
		queryErr := db.QueryRow(
			`SELECT COUNT(*) FROM audit_log_attempts WHERE error_phase = 'stream' AND error_message = ?`,
			"upstream sent an error event").Scan(&count)
		_ = db.Close()
		if queryErr == nil && count >= 1 {
			matched = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !matched {
		t.Fatalf("audit_log_attempts 未在时限内出现 error_phase=stream 且 error_message=上游原文的记录（db=%s）", auditPath)
	}
}

// 场景 B（提交前，关键未验证环节）：候选 1 的 SSE 首个事件即 error 对象帧，
// 候选 2 正常完成。断言：客户端最终收到候选 2 的正常完整流——调度层对提交前
// error 帧失败做了服务端重试/换候选（finalize.go RetryUpstream +
// ExcludeCurrentAccount）。候选顺序经账户 priority 分层固定：候选 1（失败
// 上游）priority 0 先试，候选 2 priority 1 兜底。
func TestW31StreamTopLevelErrorFrameBeforeCommitSwitchesCandidate(t *testing.T) {
	var failHits, okHits atomic.Int64
	upstream := startChainFixtureWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + upstreamAccountKey:
			// 候选 1：SSE 首个字节就是 error 对象帧，随后 EOF。
			failHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(w31IncidentErrorFrame))
		case "Bearer " + w31OKUpstreamKey:
			okHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(w31ChatStreamBody("W31-OK-CONTENT"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	admin := upstream.admin

	// 候选 2：同一 mock 上游、OK 凭据，绑定同一 seed 默认分组。
	_, created := admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode": "gpt",
		"name":         "W31 正常候选账户",
		"type":         "api_key",
		"credentials":  map[string]any{"api_key": w31OKUpstreamKey, "base_url": upstream.upstream.URL},
		// §8 连接集合 + 健康检查三件套显式提交（写链必填）。
		"connections": []map[string]any{{
			"connectionPresetCode": "gpt_openai_v1",
			"baseUrl":              upstream.upstream.URL,
			"endpointModes":        []string{"chat_json", "chat_sse", "responses_json", "responses_sse"},
			"enabled":              true,
		}},
		"healthCheckConnectionPresetCode": "gpt_openai_v1",
		"healthCheckEndpointMode":         "chat_json",
		"healthCheckModel":                acceptanceModel,
		"supportedModels":                 []string{acceptanceModel},
		"status":                          "active",
		"groupId":                         "grp_default_gpt_sys_admin",
	}, wantStatus(http.StatusCreated))
	okAccountID := str(data(created)["id"])
	if okAccountID == "" {
		t.Fatalf("candidate account create wrong: %#v", created)
	}
	// 候选顺序按 priority 升序分层（chain_accounts.go
	// orderChainCandidateRowsForDispatch）：候选 1 = 0 先试，候选 2 = 1 兜底。
	// PATCH 携带 expectedConfigRevision 乐观锁（两账户均为新建，config_revision=1）。
	admin.do(http.MethodPatch, "/__aisys__/api/accounts/"+upstream.accountID, map[string]any{"expectedConfigRevision": 1, "priority": 0}, wantStatus(http.StatusOK))
	admin.do(http.MethodPatch, "/__aisys__/api/accounts/"+okAccountID, map[string]any{"expectedConfigRevision": 1, "priority": 1}, wantStatus(http.StatusOK))

	status, contentType, body, readErr := w31PostStreamChat(t, upstream.fixture.baseURL, upstream.defaultKey, "W31场景B")
	if status != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", status, body)
	}
	if !contains(contentType, "text/event-stream") {
		t.Fatalf("content-type=%q", contentType)
	}
	// 提交前失败交回调度层换候选：客户端最终消费候选 2 的完整流，必须干净
	// 收尾（中断契约只作用于提交后失败，不误伤换号成功路径）。
	if readErr != nil {
		t.Fatalf("候选 2 的完整流应干净收尾（readErr=nil）, got %v (body=%q)", readErr, body)
	}
	if contains(body, "upstream sent an error event") || contains(body, `{"error"`) {
		t.Fatalf("客户端不得收到候选 1 的错误载荷: %s", body)
	}
	if !contains(body, "W31-OK-CONTENT") || !contains(body, "[DONE]") {
		t.Fatalf("客户端应收到候选 2 的正常完整流: %s", body)
	}
	// 候选被尝试证据：候选 1 首发失败、候选 2 接管。候选 1 零命中说明
	// priority 分层未按预期先行，本用例前提不成立（如实报错，不硬凑断言）。
	if failHits.Load() < 1 {
		t.Fatalf("候选 1（失败上游）未被尝试，priority 分层候选顺序与预期不符: failHits=%d okHits=%d", failHits.Load(), okHits.Load())
	}
	if okHits.Load() < 1 {
		t.Fatalf("候选 2 未被尝试：调度层可能未对提交前 error 帧失败做换候选重试: failHits=%d okHits=%d body=%s", failHits.Load(), okHits.Load(), body)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
