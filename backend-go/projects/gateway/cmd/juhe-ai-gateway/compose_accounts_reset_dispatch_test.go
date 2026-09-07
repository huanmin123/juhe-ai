package main

// accounts RuntimeResetEffects.DispatchAccountHealthCheck 接线断言：生产装配把
// reset/激活面的健康检查派发接到 chain_request_failure_health.go 的进程内
// outbox writer（account_health_probe_request_outbox 行；消费端 jobs J1
// Runner drain；Node dispatchAccountHealthCheck，internal-api service）。本
// 文件用临时 SQLite 业务库断言：行契约（accountId/reason 投影 + pending 可
// 消费状态）、受理与 inert 降级两条路径（fire-and-forget，派发拒绝不中断
// reset），以及派发的异步边界（不阻塞 reset 调用方）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

const (
	resetDispatchTestAccountID = "acc-reset-dispatch"
	resetDispatchTestSecret    = "reset-dispatch-secret"
)

// newResetDispatchComposition 提供派发装配所需的最小 composition
// （accountkeystates 构造只 fail-fast 空句柄/空密钥，派发路径不触库）。
func newResetDispatchComposition(t *testing.T) *composition {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &composition{db: db, Bus: inval.New(time.Now)}
}

// newResetDispatchBridge 走生产同款构造：newAccountsRuntimeResetBridge +
// 进程内 probe-request outbox writer（与 chain 装配共用同一 DDL ensure 路径）。
func newResetDispatchBridge(t *testing.T, composed *composition) (accounts.RuntimeResetEffects, *chainProbeRequestOutboxWriter) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "probe-outbox.sqlite3")) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open outbox sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	writer := newChainProbeRequestOutboxWriter(db, false, 65_000)
	// 生产对等：与 chain 装配同一条 ensure 路径已在进程启动早期完成建表；
	// 测试在派发 goroutine 之前主动 ensure，避免行查询与建表竞速。
	if err := writer.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure outbox schema: %v", err)
	}
	resetEffects, err := newAccountsRuntimeResetBridge(composed, nil, &chainRuntimeServices{}, resetDispatchTestSecret, writer)
	if err != nil {
		t.Fatalf("assemble runtime reset bridge: %v", err)
	}
	return resetEffects, writer
}

// resetDispatchRows 返回 outbox 全部行的 (accountID, reason) 投影。
func resetDispatchRows(t *testing.T, writer *chainProbeRequestOutboxWriter) [][2]string {
	t.Helper()
	rows, err := writer.db.Query(`SELECT account_id, reason FROM account_health_probe_request_outbox ORDER BY created_at, request_id`)
	if err != nil {
		t.Fatalf("query outbox rows: %v", err)
	}
	defer rows.Close()
	out := [][2]string{}
	for rows.Next() {
		var pair [2]string
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			t.Fatal(err)
		}
		out = append(out, pair)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAccountsRuntimeResetBridgeDispatchesHealthCheckRow(t *testing.T) {
	composed := newResetDispatchComposition(t)
	resetEffects, writer := newResetDispatchBridge(t, composed)
	// 管理面接线同款：write.go 激活 / runtime_reset.go reset / routes.go PATCH
	// 都经 SetRuntimeResetEffects 后的该端口派发。
	accountStore, err := accounts.NewStore(composed.db, false, resetDispatchTestSecret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	accountStore.SetRuntimeResetEffects(resetEffects)

	resetEffects.DispatchAccountHealthCheck(resetDispatchTestAccountID, "activation")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := resetDispatchRows(t, writer); len(rows) == 1 {
			if rows[0][0] != resetDispatchTestAccountID || rows[0][1] != "activation" {
				t.Fatalf("unexpected dispatched row: %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health-check dispatch never landed in the outbox")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAccountsRuntimeResetBridgeDispatchRejectedKeepsReset(t *testing.T) {
	composed := newResetDispatchComposition(t)
	// inert writer（chain 关闭/缺业务库句柄的装配形态）：派发按
	// input_unavailable 拒绝——fire-and-forget，端口不 panic、不向调用方传
	// 错、不写行，reset 继续。
	inert, err := newAccountsRuntimeResetBridge(composed, nil, &chainRuntimeServices{}, resetDispatchTestSecret,
		newChainProbeRequestOutboxWriter(nil, false, 65_000))
	if err != nil {
		t.Fatalf("assemble inert reset bridge: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		inert.DispatchAccountHealthCheck(resetDispatchTestAccountID, "inert")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("inert bridge blocked the reset")
	}
}

func TestAccountsRuntimeResetBridgeDispatchIsFireAndForget(t *testing.T) {
	composed := newResetDispatchComposition(t)
	resetEffects, writer := newResetDispatchBridge(t, composed)
	returned := make(chan struct{})
	go func() {
		resetEffects.DispatchAccountHealthCheck(resetDispatchTestAccountID, "configuration")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("health-check dispatch blocked the reset caller")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := resetDispatchRows(t, writer); len(rows) == 1 {
			if rows[0][1] != "configuration" {
				t.Fatalf("unexpected dispatched row: %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health-check dispatch never landed in the outbox")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
