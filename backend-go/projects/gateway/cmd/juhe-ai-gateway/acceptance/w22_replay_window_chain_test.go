package acceptance

// W22 流式提交边界受限重放与异常终局处置全链验收（契约：docs/functions/
// 流式提交边界受限重放与异常终局处置设计.md §6 验收 10 / 11）：窗口内失败
// （首个语义事件入窗后的错误帧）的链级换号重放——
//   - 验收 10：候选耗尽 → 503 失败终态、客户端零业务字节；
//   - 验收 11：候选 1、2 均窗口内断 → 候选 3 成功，客户端零业务字节拿到
//     候选 3 完整流，审计 2 失败 + 1 成功。

import (
	"database/sql"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

const w22WindowFailKeyPrefix = "sk-w22-window-fail-"

// w22WindowFailStreamBody 组装「窗口内断流」流：首个 delta（入窗缓冲）+ 事故
// 同款 error 对象帧 + EOF（无终止事件）。delta 保持在窗口上限 B 之内（数十
// 字节），使失败帧落窗、走提交前换号。
func w22WindowFailStreamBody(marker string) []byte {
	return []byte(
		"data: " + sseChunk(`{"id":"chatcmpl-w22","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"`+marker+`"},"finish_reason":null}]}`) + "\n\n" +
			w31IncidentErrorFrame)
}

// TestW22WindowFailureExhaustedCandidatesRenders503WithoutBusinessBytes：
// 验收 10——唯一候选窗口内失败 → 耗尽终态 503，客户端零业务字节。
func TestW22WindowFailureExhaustedCandidatesRenders503WithoutBusinessBytes(t *testing.T) {
	var failHits atomic.Int64
	upstream := startChainFixtureWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+upstreamAccountKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		failHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(w22WindowFailStreamBody("W22-EXHAUSTED-DELTA"))
	})

	status, contentType, body, readErr := w31PostStreamChat(t, upstream.fixture.baseURL, upstream.defaultKey, "W22耗尽")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("窗口内失败候选耗尽应渲染 503 终态, got %d body=%s", status, body)
	}
	if contains(contentType, "text/event-stream") {
		t.Fatalf("耗尽终态应为 HTTP 错误体而非 SSE 流: content-type=%q body=%s", contentType, body)
	}
	// 客户端零业务字节：503 错误体内不得出现任何上游语义输出。
	if contains(body, "W22-EXHAUSTED-DELTA") || contains(body, "upstream sent an error event") {
		t.Fatalf("耗尽终态不得携带业务字节: %s", body)
	}
	if readErr != nil {
		t.Fatalf("503 错误体应干净收尾: %v", readErr)
	}
	if failHits.Load() < 1 {
		t.Fatal("候选未被尝试，前提不成立")
	}
}

// TestW22WindowFailureChainReplaysUntilCandidateSucceeds：验收 11——候选 1、2
// 均窗口内断流，候选 3 完整成功：客户端零业务字节（前两个候选的缓冲被丢弃）
// + 最终完整流 + 审计恰好 2 失败 + 1 成功。
func TestW22WindowFailureChainReplaysUntilCandidateSucceeds(t *testing.T) {
	var failHits, okHits atomic.Int64
	upstream := startChainFixtureWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "Bearer "+upstreamAccountKey || auth == "Bearer "+w22WindowFailKeyPrefix+"2":
			// 候选 1 / 2：首个语义事件入窗后断流（错误帧 + EOF）。
			failHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(w22WindowFailStreamBody("W22-FAIL-CANDIDATE"))
		case auth == "Bearer "+w31OKUpstreamKey:
			// 候选 3：正常完整流。
			okHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(w31ChatStreamBody("W22-OK-CONTENT"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	admin := upstream.admin

	// 候选 2 / 候选 3：同一 mock 上游不同凭据，priority 升序分层（0 → 1 → 2）。
	createCandidate := func(name, key string, priority int) {
		_, created := admin.do("POST", "/__aisys__/api/accounts", map[string]any{
			"providerCode": "gpt",
			"name":         name,
			"type":         "api_key",
			"credentials":  map[string]any{"api_key": key, "base_url": upstream.upstream.URL},
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
		accountID := str(data(created)["id"])
		if accountID == "" {
			t.Fatalf("候选账户创建失败: %#v", created)
		}
		admin.do("PATCH", "/__aisys__/api/accounts/"+accountID, map[string]any{"expectedConfigRevision": 1, "priority": priority}, wantStatus(http.StatusOK))
	}
	createCandidate("W22 窗口失败候选二", w22WindowFailKeyPrefix+"2", 1)
	createCandidate("W22 正常候选三", w31OKUpstreamKey, 2)
	admin.do("PATCH", "/__aisys__/api/accounts/"+upstream.accountID, map[string]any{"expectedConfigRevision": 1, "priority": 0}, wantStatus(http.StatusOK))

	status, contentType, body, readErr := w31PostStreamChat(t, upstream.fixture.baseURL, upstream.defaultKey, "W22连环换号")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !contains(contentType, "text/event-stream") {
		t.Fatalf("content-type=%q", contentType)
	}
	// 客户端零业务字节：失败候选的窗口缓冲被丢弃，最终只消费候选 3 完整流。
	if contains(body, "W22-FAIL-CANDIDATE") || contains(body, "upstream sent an error event") {
		t.Fatalf("失败候选的窗口缓冲不得透传客户端: %s", body)
	}
	if !contains(body, "W22-OK-CONTENT") || !contains(body, "[DONE]") {
		t.Fatalf("客户端应收到候选 3 的完整流: %s", body)
	}
	if readErr != nil {
		t.Fatalf("候选 3 的完整流应干净收尾: %v (body=%q)", readErr, body)
	}
	if failHits.Load() != 2 {
		t.Fatalf("失败候选命中 = %d, want 2", failHits.Load())
	}
	if okHits.Load() != 1 {
		t.Fatalf("成功候选命中 = %d, want 1", okHits.Load())
	}
	// 审计恰好 2 失败 + 1 成功（异步落库，轮询至稳定；fixture 审计库为本用例
	// 独占，直接全表计数）。PG 唯一形态：直连网关隔离库读 juhe_dataset 审计
	// 数据集（原 sqlite audit.sqlite3 文件轮询随存储形态退役重钉 pgx DSN）。
	db, err := sql.Open("pgx", upstream.fixture.dsn)
	if err != nil {
		t.Fatalf("open audit db: %v", err)
	}
	defer func() { _ = db.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	var failed, succeeded int
	for time.Now().Before(deadline) {
		queryErr := db.QueryRow(
			`SELECT COALESCE(SUM(CASE WHEN success = FALSE THEN 1 ELSE 0 END), 0),
			        COALESCE(SUM(CASE WHEN success = TRUE THEN 1 ELSE 0 END), 0)
			 FROM juhe_dataset.audit_log_attempts`).Scan(&failed, &succeeded)
		if queryErr == nil && failed == 2 && succeeded == 1 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if failed != 2 || succeeded != 1 {
		t.Fatalf("审计 attempts 期望 2 失败 + 1 成功, got failed=%d succeeded=%d", failed, succeeded)
	}
}
