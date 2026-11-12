package main

// w1: compose_accounts_reset.go 收割——运行态复位桥的纯内存面与探活 outbox
// 写入。（chat 组合守卫、SSE 附着订阅者与 chat 数据库打开的覆盖随 my-chat
// 会话面退役移除，设计 §8。）

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

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

func TestW1ProbeOutboxWriterPG(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`)
		return err
	})
	writer := newChainProbeRequestOutboxWriter(db, 1500)
	// 方言投影：PG 按生产 juhe_business 限定表名、绑定符转 $n（与生产
	// juhe_business.account_health_probe_request_outbox 读写面同一契约）。
	if got := writer.table(); got != "juhe_business.account_health_probe_request_outbox" {
		t.Fatalf("table = %q", got)
	}
	if !strings.Contains(writer.bind("INSERT INTO t VALUES (?,?)"), "$") {
		t.Fatal("PG 必须转换绑定符")
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
		if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_health_probe_request_outbox`).Scan(&rows); err == nil && rows >= 2 {
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
