package runtimelog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// w14i_pg_store_test.go 覆盖 postgresStore 全链路：OpenStore 参数臂、游标
// 读写、Commit（含 facet 累加）、Cleanup（记录批次与游标批次）、owner lease
// 生命周期、RuntimeRetentionDays 与错误注入臂。
//
// 建库走 pgtestkit 隔离 PG 测试库 + 生产 OpenStore/EnsureSchema
// （PLAN-20261024T051925945Z B6 测试迁移半场），替代旧 w1cover 共享库 +
// skip 门禁：每测试独立 fresh 库，PG 不可达显式失败而非跳过；隔离库随测试
// 结束删除，不再需要共享库行级清理纪律。schema 校验/列校验错误臂、连接级
// 故障臂仍由 wn_pgfake 脚本化臂覆盖（真实库上注入会破坏生产 DDL 形状）。

func TestW14iOpenStorePostgresParameterArms(t *testing.T) {
	if _, err := OpenStore(context.Background(), Config{PostgresURL: "w14i-not-a-url"}); err == nil {
		t.Fatalf("非法 URL 应报错")
	}
	// MinConns>MaxConns 校验先于建池，合法但不可达的占位 DSN 不会被真正连接。
	_, err := OpenStore(context.Background(), Config{PostgresURL: "postgres://127.0.0.1:1/w14i-param-arms?sslmode=disable", PostgresMaxConns: 2, PostgresMinConns: 4})
	if err == nil || !strings.Contains(err.Error(), "min connections") {
		t.Fatalf("MinConns>MaxConns 应报错: %v", err)
	}
}

func TestW14iPostgresStoreFullLifecycle(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logFile := "w14i-lifecycle.log"

	lease, acquired, err := store.AcquireOwnerLease(ctx, "w14i-pg-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("fresh 库 owner lease 必须可获得: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = store.ReleaseOwnerLease(releaseCtx, lease)
	})
	if err := store.VerifyOwnerLease(ctx, lease); err != nil {
		t.Fatalf("有效 lease 校验应通过: %v", err)
	}
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("续租应成功: renewed=%t err=%v", renewed, err)
	}
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return nil }); err != nil {
		t.Fatalf("fence 内回调应成功: %v", err)
	}
	lost := OwnerLease{OwnerID: "w14i-pg-owner", FenceToken: lease.FenceToken + 999}
	if err := store.VerifyOwnerLease(ctx, lost); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("伪造 fence 应判 lease 丢失: %v", err)
	}
	if err := store.WithOwnerLeaseFence(ctx, lost, func() error { return nil }); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("丢失 lease 的 fence 应失败: %v", err)
	}

	// 游标初始不存在。
	if cursor, err := store.FindCursor(ctx, logFile); err != nil || cursor != nil {
		t.Fatalf("缺失游标应返回 nil: cursor=%#v err=%v", cursor, err)
	}

	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	records := []Record{
		{ID: "w14i-rec-1", LogFile: logFile, LogOffset: 0, LineNumber: 1, Time: past, Level: "INFO", Event: "w14i-event", Message: "w14i message", RawJSON: "{}", CreatedAt: past},
		{ID: "w14i-rec-2", LogFile: logFile, LogOffset: 21, LineNumber: 2, Time: past, Level: "error", RawJSON: "{}", CreatedAt: past},
	}
	cursor := Cursor{LogFile: logFile, FileIdentity: "w14i-identity", CursorOffset: 42, LineNumber: 2, FileSize: 42, CreatedAt: past, UpdatedAt: past}
	if err := store.Commit(ctx, lease, records, cursor, time.Now().UTC()); err != nil {
		t.Fatalf("PG Commit 应成功: %v", err)
	}
	found, err := store.FindCursor(ctx, logFile)
	if err != nil || found == nil || found.CursorOffset != 42 {
		t.Fatalf("游标应已落库: %#v err=%v", found, err)
	}
	byIdentity, err := store.FindCursorByIdentity(ctx, "w14i-identity")
	if err != nil || byIdentity == nil || byIdentity.LogFile != logFile {
		t.Fatalf("按 identity 查游标应命中: %#v err=%v", byIdentity, err)
	}

	copyCursor := *found
	copyCursor.CursorOffset = 43
	if err := store.CopyCursor(ctx, lease, copyCursor); err != nil {
		t.Fatalf("CopyCursor 应成功: %v", err)
	}
	displaced := *found
	displaced.LogFile = displacedIdentityPrefix + logFile
	replacement := *found
	replacement.CursorOffset = 44
	if err := store.ReplaceCursor(ctx, lease, &displaced, replacement); err != nil {
		t.Fatalf("ReplaceCursor 应成功: %v", err)
	}

	if fallback, err := store.RuntimeRetentionDays(ctx, 7); err != nil || fallback < 1 {
		t.Fatalf("RuntimeRetentionDays fallback=%d err=%v", fallback, err)
	}

	// 记录错误注入：空 time 在 normalizeRecord 处失败。
	bad := []Record{{ID: "w14i-rec-bad", LogFile: logFile, Time: " ", RawJSON: "{}"}}
	if err := store.Commit(ctx, lease, bad, cursor, time.Now().UTC()); err == nil {
		t.Fatalf("非法记录应使 Commit 失败")
	}
	// 游标错误注入：非法 CreatedAt 在 normalizeCursor 处失败。
	badCursor := cursor
	badCursor.CreatedAt = "not-a-time"
	if err := store.Commit(ctx, lease, nil, badCursor, time.Now().UTC()); err == nil {
		t.Fatalf("非法游标应使 Commit 失败")
	}
	badDisplaced := displaced
	badDisplaced.CreatedAt = "not-a-time"
	if err := store.ReplaceCursor(ctx, lease, &badDisplaced, replacement); err == nil {
		t.Fatalf("非法 displaced 应使 ReplaceCursor 失败")
	}
	badReplacement := replacement
	badReplacement.LastReadAt = "not-a-time"
	if err := store.ReplaceCursor(ctx, lease, nil, badReplacement); err == nil {
		t.Fatalf("非法 replacement 应使 ReplaceCursor 失败")
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: logFile, CreatedAt: "not-a-time"}); err == nil {
		t.Fatalf("非法 CopyCursor 游标应失败")
	}
}

func TestW14iPostgresCleanupBatches(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logFile := "w14i-cleanup.log"

	lease, acquired, err := store.AcquireOwnerLease(ctx, "w14i-pg-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("fresh 库 owner lease 必须可获得: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = store.ReleaseOwnerLease(releaseCtx, lease)
	})

	base := time.Now().UTC().Add(-48 * time.Hour)
	records := make([]Record, 0, 3)
	for index := 0; index < 3; index++ {
		records = append(records, Record{ID: "w14i-clean-" + string(rune('a'+index)), LogFile: logFile, LogOffset: int64(index), LineNumber: int64(index + 1), Time: base.Format(time.RFC3339Nano), RawJSON: "{}", CreatedAt: base.Format(time.RFC3339Nano)})
	}
	cursor := Cursor{LogFile: logFile, FileIdentity: "w14i-clean-identity", CursorOffset: 3, LineNumber: 3, FileSize: 3, CreatedAt: base.Format(time.RFC3339Nano), UpdatedAt: base.Format(time.RFC3339Nano)}
	if err := store.Commit(ctx, lease, records, cursor, base.Add(-time.Hour)); err != nil {
		t.Fatalf("PG Commit 应成功: %v", err)
	}

	// 截断窗口取 24h 前：批删本波次记录，第二个批次空转退出。
	result, err := store.Cleanup(ctx, lease, time.Now().UTC().Add(-24*time.Hour), 2, 4)
	if err != nil {
		t.Fatalf("PG Cleanup 失败: %v", err)
	}
	if result.RuntimeLogs != 3 {
		t.Fatalf("应清理 3 条记录: %#v", result)
	}
	// 游标批次：updated_at 未到窗口时 count=0 也必须正常返回。
	if remaining, err := store.FindCursor(ctx, logFile); err != nil || remaining == nil {
		t.Fatalf("未到窗口的游标应保留: %#v err=%v", remaining, err)
	}
	if lost, err := store.Cleanup(ctx, OwnerLease{OwnerID: "w14i-pg-owner", FenceToken: 991}, time.Now().UTC().Add(-24*time.Hour), 2, 1); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("丢失 lease 的 Cleanup 应失败: result=%#v err=%v", lost, err)
	}
}
