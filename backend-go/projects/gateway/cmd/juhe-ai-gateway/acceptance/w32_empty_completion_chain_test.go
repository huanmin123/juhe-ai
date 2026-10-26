// W32 上游「空壳成功流」终局判定全链验收（契约：流式提交边界受限重放与异常
// 终局处置设计 §3.2.3，生产事件待办 W32）：上游中转渠道以合法 JSON、无 error
// 对象的三帧空壳流（零值 chunk + choices:[] 空 usage 帧 + [DONE]，xiaoyi/
// kid1412 生产事故字节原文形态）表达"上游排队/资源紧张的空完成"时——
//   - 场景 B（提交前）：空壳流全程零输出，所有帧滞留网关预提交缓冲，[DONE]
//     触发空完成判定 → 客户端零字节，失败交回调度层换候选，客户端最终收到
//     候选 2 的正常完整流，审计 attempts 记 error_code=upstream_stream_empty_completion；
//   - 场景 C（内容流夹零值帧）：正常内容流中间夹生产同款零值帧 → L2 协议
//     形状隔离把该帧对下游剥除，内容完整透传，流按 success 收尾。
package acceptance

import (
	"database/sql"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// w32IncidentShellStream 是 2026-10-26 生产事故（trace d2fe5b50/a36d8aa8）
// 客户端实收三帧的字节原文形态：全部合法 JSON、无顶层 error 对象、带 [DONE]。
var w32IncidentShellStream = []byte(
	"data: {\"id\":\"\",\"object\":\"\",\"created\":0,\"model\":\"\",\"system_fingerprint\":null,\"choices\":null,\"usage\":null}\n\n" +
		"data: {\"id\":\"\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"\",\"system_fingerprint\":\"\",\"choices\":[],\"usage\":{\"prompt_tokens\":168586,\"completion_tokens\":0,\"total_tokens\":168586,\"prompt_tokens_details\":{\"cached_tokens\":0,\"text_tokens\":0,\"audio_tokens\":0,\"image_tokens\":0},\"completion_tokens_details\":{\"text_tokens\":0,\"audio_tokens\":0,\"image_tokens\":0,\"reasoning_tokens\":0},\"input_tokens\":0,\"output_tokens\":0,\"input_tokens_details\":null,\"claude_cache_creation_5_m_tokens\":0,\"claude_cache_creation_1_h_tokens\":0}}\n\n" +
		"data: [DONE]\n\n")

// w32IncidentZeroChunk 是事故首帧（客户端 AI SDK schema 校验拒绝的那一帧），
// 供「客户端永不见零值帧」断言复用。
const w32IncidentZeroChunk = `{"id":"","object":"","created":0,"model":"","system_fingerprint":null,"choices":null,"usage":null}`

const w32OKUpstreamKey = "sk-w32-ok-upstream"

// 场景 B（提交前换候选，生产事故主形态）：候选 1 的 SSE 全文即事故三帧空壳，
// 候选 2 正常完成。断言：客户端零字节收到空壳（choices:null 帧不得出现）、
// 最终消费候选 2 的完整流并干净收尾；审计 attempts 出现
// error_code=upstream_stream_empty_completion 的失败行。
func TestW32EmptyCompletionShellStreamBeforeCommitSwitchesCandidate(t *testing.T) {
	var shellHits, okHits atomic.Int64
	upstream := startChainFixtureWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + upstreamAccountKey:
			// 候选 1：生产事故空壳三帧 + [DONE]（全部合法 JSON，无 error 对象）。
			shellHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(w32IncidentShellStream)
		case "Bearer " + w32OKUpstreamKey:
			okHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(w31ChatStreamBody("W32-OK-CONTENT"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	admin := upstream.admin

	// 候选 2：同一 mock 上游、OK 凭据，绑定同一 seed 默认分组（w31 场景 B 同款）。
	_, created := admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode": "gpt",
		"name":         "W32 正常候选账户",
		"type":         "api_key",
		"credentials":  map[string]any{"api_key": w32OKUpstreamKey, "base_url": upstream.upstream.URL},
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
	// 候选顺序按 priority 升序分层：候选 1（空壳上游）0 先试，候选 2 兜底。
	admin.do(http.MethodPatch, "/__aisys__/api/accounts/"+upstream.accountID, map[string]any{"expectedConfigRevision": 1, "priority": 0}, wantStatus(http.StatusOK))
	admin.do(http.MethodPatch, "/__aisys__/api/accounts/"+okAccountID, map[string]any{"expectedConfigRevision": 1, "priority": 1}, wantStatus(http.StatusOK))

	status, contentType, body, readErr := w31PostStreamChat(t, upstream.fixture.baseURL, upstream.defaultKey, "W32场景B")
	if status != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", status, body)
	}
	if !contains(contentType, "text/event-stream") {
		t.Fatalf("content-type=%q", contentType)
	}
	// 空壳流全程零输出 → 提交前判定换候选：客户端干净收尾（网络级断开只作用
	// 于提交后失败，不误伤换号成功路径）。
	if readErr != nil {
		t.Fatalf("候选 2 的完整流应干净收尾（readErr=nil）, got %v (body=%q)", readErr, body)
	}
	// 客户端永不见零值帧与空壳 usage 帧：生产事故的客户端死因字节。
	if contains(body, `"choices":null`) || contains(body, w32IncidentZeroChunk) {
		t.Fatalf("客户端收到了生产事故零值帧: %s", body)
	}
	if !contains(body, "W32-OK-CONTENT") || !contains(body, "[DONE]") {
		t.Fatalf("客户端应收到候选 2 的正常完整流: %s", body)
	}
	// 候选被尝试证据：候选 1 空壳首发失败、候选 2 接管。
	if shellHits.Load() < 1 {
		t.Fatalf("候选 1（空壳上游）未被尝试，priority 分层候选顺序与预期不符: shellHits=%d okHits=%d", shellHits.Load(), okHits.Load())
	}
	if okHits.Load() < 1 {
		t.Fatalf("候选 2 未被尝试：空完成失败未触发服务端换候选: shellHits=%d okHits=%d body=%s", shellHits.Load(), okHits.Load(), body)
	}

	// 审计 attempts：error_code=upstream_stream_empty_completion（异步落库，轮询）。
	db, err := sql.Open("pgx", upstream.fixture.dsn)
	if err != nil {
		t.Fatalf("open audit db: %v", err)
	}
	defer func() { _ = db.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	matched := false
	for time.Now().Before(deadline) {
		var count int
		queryErr := db.QueryRow(
			`SELECT COUNT(*) FROM juhe_dataset.audit_log_attempts WHERE error_phase = 'stream' AND error_code = $1`,
			"upstream_stream_empty_completion").Scan(&count)
		if queryErr == nil && count >= 1 {
			matched = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !matched {
		t.Fatalf("audit_log_attempts 未在时限内出现 error_code=upstream_stream_empty_completion 的失败记录")
	}
}

// 场景 C（内容流夹零值帧，L2 协议形状隔离）：单一账户正常内容流中间夹生产
// 同款零值帧。断言：零值帧被隔离不透传（客户端字节流无 choices:null）、前后
// 内容与 [DONE] 完整、流按 success 收尾（有语义输出，不触发空完成判定）。
func TestW32InvalidProtocolFrameIsolatedInsideContentStreamChain(t *testing.T) {
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
		_, _ = w.Write([]byte(
			"data: " + sseChunk(`{"id":"chatcmpl-w32c","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"W32-内容甲"},"finish_reason":null}]}`) + "\n\n" +
				"data: " + w32IncidentZeroChunk + "\n\n" +
				"data: " + sseChunk(`{"id":"chatcmpl-w32c","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"content":"W32-内容乙"},"finish_reason":null}]}`) + "\n\n" +
				"data: " + sseChunk(`{"id":"chatcmpl-w32c","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`) + "\n\n" +
				"data: [DONE]\n\n"))
	})

	status, contentType, body, readErr := w31PostStreamChat(t, upstream.fixture.baseURL, upstream.defaultKey, "W32场景C")
	if status != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", status, body)
	}
	if !contains(contentType, "text/event-stream") {
		t.Fatalf("content-type=%q", contentType)
	}
	if readErr != nil {
		t.Fatalf("内容流应干净收尾, got %v (body=%q)", readErr, body)
	}
	if contains(body, `"choices":null`) || contains(body, w32IncidentZeroChunk) {
		t.Fatalf("零值帧应被 L2 隔离不透传客户端: %s", body)
	}
	if !contains(body, "W32-内容甲") || !contains(body, "W32-内容乙") || !contains(body, "[DONE]") {
		t.Fatalf("内容与终止帧应完整透传: %s", body)
	}
}
