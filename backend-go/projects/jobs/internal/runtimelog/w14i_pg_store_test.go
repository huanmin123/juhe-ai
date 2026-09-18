package runtimelog

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// w14i_pg_store_test.go 以 w1cover 门禁覆盖库覆盖 postgresStore 全链路：
// OpenStore 参数臂、游标读写、Commit（含 facet 累加）、Cleanup（记录批次与
// 游标批次）、owner lease 生命周期、RuntimeRetentionDays 与错误注入臂。
//
// 共享库纪律：
//   - 只写 w14i- 前缀的 log_file / 记录 ID，结束前全部删除；
//   - owner lease 是共享单例行，测试用短租约并在结束时释放回空闲态；
//   - facet 聚合是全局 bucket，本测试的增量与清理减量相互抵消；
//   - 连接失败一律 t.Skip，不把环境问题当测试失败。
//
// w14i 波次不可达清单（PG 臂，均已核对）：
//   - CheckSchema 缺表/缺索引/列校验错误臂：覆盖库 schema 冻结齐全，
//     不允许在共享库上删表注入；
//   - OpenStore 的 pool.Ping 失败臂：连接失败路径由 t.Skip 语义承接；
//   - beginPostgresTx/rollback 的连接级故障臂：pgx 池无本地注入点。

func w14iW1CoverPostgresURL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w14i PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w14i PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w14iOpenPostgresStore(t *testing.T) *postgresStore {
	t.Helper()
	url := w14iW1CoverPostgresURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opened, err := OpenStore(ctx, Config{Mode: ModePostgres, PostgresURL: url, PostgresMaxConns: 4})
	if err != nil {
		t.Skipf("w14i PG gated: 打开失败: %v", err)
	}
	store, ok := opened.(*postgresStore)
	if !ok {
		_ = opened.Close()
		t.Fatalf("OpenStore(ModePostgres) 返回 %T", opened)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := EnsureSchema(ctx, store); err != nil {
		t.Skipf("w14i PG gated: schema 不可用: %v", err)
	}
	return store
}

func w14iCleanupRows(t *testing.T, store *postgresStore, logFile string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, target := range []string{logFile, displacedIdentityPrefix + logFile} {
		if _, err := store.pool.Exec(ctx, `DELETE FROM juhe_dataset.runtime_logs WHERE log_file = $1`, target); err != nil {
			t.Errorf("清理 w14i 记录失败: %v", err)
		}
		if _, err := store.pool.Exec(ctx, `DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1`, target); err != nil {
			t.Errorf("清理 w14i 游标失败: %v", err)
		}
	}
}

func TestW14iOpenStorePostgresParameterArms(t *testing.T) {
	if _, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: "w14i-not-a-url"}); err == nil {
		t.Fatalf("非法 URL 应报错")
	}
	url := w14iW1CoverPostgresURL(t)
	_, err := OpenStore(context.Background(), Config{Mode: ModePostgres, PostgresURL: url, PostgresMaxConns: 2, PostgresMinConns: 4})
	if err == nil || !strings.Contains(err.Error(), "min connections") {
		t.Fatalf("MinConns>MaxConns 应报错: %v", err)
	}
}

func TestW14iPostgresStoreFullLifecycle(t *testing.T) {
	store := w14iOpenPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logFile := "w14i-lifecycle.log"
	t.Cleanup(func() { w14iCleanupRows(t, store, logFile) })

	lease, acquired, err := store.AcquireOwnerLease(ctx, "w14i-pg-owner", time.Minute)
	if err != nil || !acquired {
		t.Skipf("w14i PG gated: 共享 owner lease 暂不可用: acquired=%t err=%v", acquired, err)
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
	store := w14iOpenPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logFile := "w14i-cleanup.log"
	t.Cleanup(func() { w14iCleanupRows(t, store, logFile) })

	lease, acquired, err := store.AcquireOwnerLease(ctx, "w14i-pg-owner", time.Minute)
	if err != nil || !acquired {
		t.Skipf("w14i PG gated: 共享 owner lease 暂不可用: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = store.ReleaseOwnerLease(releaseCtx, lease)
	})

	// 保护共享数据：确认批删窗口内没有非 w14i 行。
	var foreign int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM juhe_dataset.runtime_logs WHERE log_file <> $1 AND time < $2`, logFile, time.Now().UTC().Add(-24*time.Hour)).Scan(&foreign); err != nil {
		t.Skipf("w14i PG gated: 读取共享记录失败: %v", err)
	}
	if foreign != 0 {
		t.Skip("w14i PG gated: 共享库存在 24h 前的他人记录，跳过时间窗批删")
	}

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
