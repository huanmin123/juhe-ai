package main

// w1: chain_chat_mount.go 与 compose_accounts_reset.go 收割——chat 组合守卫、
// SSE 附着订阅者的背压丢弃契约、chat 数据库打开、运行态复位桥的纯内存面
// 与探活 outbox 写入。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestW1ComposeChatFamilyGuards(t *testing.T) {
	if _, err := composeChatFamily(nil, runtimeConfig{}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "composition") {
		t.Fatalf("nil composed = %v", err)
	}
	if _, err := composeChatFamily(&composition{}, runtimeConfig{}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "聊天数据库") {
		t.Fatalf("nil chatDB = %v", err)
	}
	if _, err := composeChatFamily(&composition{}, runtimeConfig{}, &sql.DB{}, nil, nil); err == nil || !strings.Contains(err.Error(), "runtime cache") {
		t.Fatalf("nil services = %v", err)
	}
	if _, err := composeChatFamily(&composition{}, runtimeConfig{}, &sql.DB{}, &chainRuntimeServices{}, nil); err == nil || !strings.Contains(err.Error(), "网关链") {
		t.Fatalf("nil chain = %v", err)
	}
}

func TestW1ChatAttachSubscriberBackpressure(t *testing.T) {
	subscriber := &chatAttachSubscriber{events: make(chan chat.ChatGenerationEvent, 2)}
	// 缓冲未满：投递成功。
	if !subscriber.TrySend(chat.ChatGenerationEvent{Type: "delta"}) {
		t.Fatal("缓冲内必须投递成功")
	}
	if !subscriber.TrySend(chat.ChatGenerationEvent{Type: "delta"}) {
		t.Fatal("第二投递必须成功")
	}
	// 缓冲满：丢弃订阅者（Node destroy-on-backpressure 契约）。
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "delta"}) {
		t.Fatal("缓冲满必须返回 false")
	}
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "delta"}) {
		t.Fatal("丢弃后必须持续 false")
	}
	// 通道已关闭：读取端先排空缓冲内的既有事件，再读到关闭信号。
	for i := 0; i < 2; i++ {
		event := <-subscriber.events
		if event.Type != "delta" {
			t.Fatalf("event %d = %v", i, event.Type)
		}
	}
	if _, open := <-subscriber.events; open {
		t.Fatal("缓冲排空后必须读到关闭")
	}
}

func TestW1ChatAttachStreamHandlerNotAttached(t *testing.T) {
	// 未注册的 generation：订阅失败 → false，不写响应。
	handler := chatAttachStreamHandler(chat.NewGenerationHub(func() string { return "2026-01-01T00:00:00Z" }))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/attach", nil)
	if handler(recorder, request, chat.GenerationIdentity{ConversationID: "conv_missing"}) {
		t.Fatal("未附着必须 false")
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("不得写响应 = %d, %q", recorder.Code, recorder.Body.String())
	}
}

func TestW1OpenChatDatabaseSQLite(t *testing.T) {
	// SQLite 模式缺路径：报错。
	if _, _, err := openChatDatabase(runtimeConfig{}, nil, nil, false); err == nil || !strings.Contains(err.Error(), "JUHE_AI_CHAT_DATABASE_PATH") {
		t.Fatalf("missing path = %v", err)
	}
	// 有效路径：打开并配置（单连接）。
	db, owns, err := openChatDatabase(runtimeConfig{ChatDatabasePath: t.TempDir() + "/chat.sqlite3"}, nil, nil, false)
	if err != nil || !owns || db == nil {
		t.Fatalf("open = %v, %v", owns, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("SELECT 1"); err != nil {
		t.Fatalf("ping = %v", err)
	}
}

func TestW1ResetBridgePureMemoryFace(t *testing.T) {
	bridge := &accountsRuntimeResetBridge{now: time.Now}
	// 瞬态代次记忆：写后可读，键按账户+指纹隔离。
	bridge.rememberTransientGeneration("acc_1", "fp_1", "gen_9")
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_1"); got != "gen_9" {
		t.Fatalf("remembered = %q", got)
	}
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_x"); got != "" {
		t.Fatalf("miss = %q", got)
	}
	// 未记忆代次：不发起 CAS，报告未清除。
	cleared, err := bridge.ClearAPIKeyTransientFailure(context.Background(), "acc_1", "fp_missing", w1Int64Ptr(3))
	if err != nil || cleared {
		t.Fatalf("unremembered = %v, %v", cleared, err)
	}
	// nil 代次：直接未清除。
	if cleared, err := bridge.ClearAPIKeyTransientFailure(context.Background(), "acc_1", "fp_1", nil); err != nil || cleared {
		t.Fatalf("nil generation = %v, %v", cleared, err)
	}
	// 账户守卫投影：指纹与代次挂载。
	projected := bridge.guardAccount("acc_1", "fp_1", "gen_9")
	if projected.ID != "acc_1" || projected.SelectedAPIKeyFingerprint == nil || *projected.SelectedAPIKeyFingerprint != "fp_1" {
		t.Fatalf("guard account = %+v", projected)
	}
	if projected.SelectedAPIKeyTransientGeneration == nil || *projected.SelectedAPIKeyTransientGeneration != "gen_9" {
		t.Fatalf("generation = %+v", projected.SelectedAPIKeyTransientGeneration)
	}
}

func w1Int64Ptr(value int64) *int64 { return &value }

func TestW1ProbeOutboxWriterSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/probe-outbox.sqlite3")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	writer := newChainProbeRequestOutboxWriter(db, false, 1500)
	// 方言投影：SQLite 无前缀、绑定符保持。
	if got := writer.table(); got != "account_health_probe_request_outbox" {
		t.Fatalf("table = %q", got)
	}
	if strings.Contains(writer.bind("INSERT INTO t VALUES (?,?)"), "$") {
		t.Fatal("SQLite 不得转换绑定符")
	}
	// 首次入队自建 schema 并落行。
	outcome := writer.EnqueueProbeRequest(context.Background(), "acc_probe", "reset", "trace_1", nil)
	if outcome.Outcome == gatewaycodex.HealthDispatchRejected {
		t.Fatalf("outcome = %+v", outcome)
	}
	// 派发器：请求去重标记 + 入队。
	dispatcher := newChainRequestFailureHealthDispatcher(writer)
	first := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	if !dispatcher.DispatchRequestFailureAccountHealthCheck(first, "gateway", "acc_probe") {
		t.Fatal("首次派发必须受理")
	}
	if dispatcher.DispatchRequestFailureAccountHealthCheck(first, "gateway", "acc_probe") {
		t.Fatal("同请求不得重复派发")
	}
	// 直接派发（带 trace）：落第二行。
	if outcome := dispatcher.dispatch("acc_probe", "reset_retry", "trace_2", nil); outcome.Outcome == gatewaycodex.HealthDispatchRejected {
		t.Fatalf("dispatch outcome = %+v", outcome)
	}
	// 等待异步落库：轮询最多 3 秒。
	deadline := time.Now().Add(3 * time.Second)
	rows := 0
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox`).Scan(&rows); err == nil && rows >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rows < 2 {
		t.Fatalf("outbox rows = %d", rows)
	}
	// 运行态复位桥的派发：nil writer 时只告警不 panic（异步 goroutine）。
	bridge := &accountsRuntimeResetBridge{now: time.Now}
	bridge.DispatchAccountHealthCheck("acc_x", "reset")
}
