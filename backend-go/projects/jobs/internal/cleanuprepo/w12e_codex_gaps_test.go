package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// w12eCodexShard 懒加载并返回分片句柄（store.shards 是懒填充缓存）。
func w12eCodexShard(t *testing.T, store *CodexContextStore, index int, query string, args ...any) sql.Result {
	t.Helper()
	db, err := store.shard(index)
	if err != nil {
		t.Fatalf("shard %d: %v", index, err)
	}
	return mustExecKit(t, db, query, args...)
}

// w12e_codex_gaps_test.go 补齐 codexcontext.go 错误臂：SQLite 触发器注入
// 逐语句失败、分片遍历/限额分支，PG 录制驱动的逐阶段失败与 referenced
// key 过滤。

func TestW12ECodexShardAndLimitArms(t *testing.T) {
	// 分片句柄仅 SQLite 可用。
	pgStore := &CodexContextStore{Postgres: true}
	if _, err := pgStore.shard(0); err == nil || !strings.Contains(err.Error(), "仅在 SQLite 模式可用") {
		t.Fatalf("PG 模式分片应拒绝: %v", err)
	}
	store := newKitCodexStore(t, 2)
	// expiredBefore 空白回落 now；limit 超界收敛。
	if _, err := store.CleanupExpiredStates(context.Background(), "   ", 20000); err != nil {
		t.Fatalf("空白 before 不应报错: %v", err)
	}
	// selectExpiredSessionsSQLite：limit 饱和 → hasMore。
	seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
	seedKitCodexSession(t, store, 1, "s-2", "2026-01-02T00:00:00.000Z")
	rows, hasMore, err := store.selectExpiredSessionsSQLite(context.Background(), "2026-09-10T00:00:00.000Z", 1, store.shardIndexes())
	if err != nil || !hasMore || len(rows) != 1 {
		t.Fatalf("限额饱和应 hasMore: rows=%d hasMore=%v err=%v", len(rows), hasMore, err)
	}
	// pending 限额收敛。
	if _, err := store.selectPendingStorageKeysSQLite(context.Background(), 20000); err != nil {
		t.Fatalf("pending 限额收敛不应报错: %v", err)
	}
	// filterUnreferenced：空集合直通。
	empty, err := store.filterUnreferencedStorageKeysSQLite(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空集合应直通: %v %v", empty, err)
	}
}

// TestW12ECodexSQLiteAbortArms：触发器注入逐语句失败。
func TestW12ECodexSQLiteAbortArms(t *testing.T) {
	ctx := context.Background()
	seedTwo := func(t *testing.T, store *CodexContextStore) {
		seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_compacts", "c-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
	}
	// 响应删除失败（入队已行 -> 回滚）。
	{
		store := newKitCodexStore(t, 1)
		seedTwo(t, store)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_del_resp BEFORE DELETE ON codex_context_responses
      BEGIN SELECT RAISE(ABORT,'w12e resp del boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e resp del boom") {
			t.Fatalf("响应删除失败应透传: %v", err)
		}
	}
	// 压缩删除失败。
	{
		store := newKitCodexStore(t, 1)
		seedTwo(t, store)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_del_comp BEFORE DELETE ON codex_context_compacts
      BEGIN SELECT RAISE(ABORT,'w12e comp del boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e comp del boom") {
			t.Fatalf("压缩删除失败应透传: %v", err)
		}
	}
	// storage key 入队失败。
	{
		store := newKitCodexStore(t, 1)
		seedTwo(t, store)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_enqueue BEFORE INSERT ON codex_context_storage_cleanup_queue
      BEGIN SELECT RAISE(ABORT,'w12e enqueue boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e enqueue boom") {
			t.Fatalf("入队失败应透传: %v", err)
		}
	}
	// 会话残余刷新失败。
	{
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_compacts", "c-1", "s-1", "k-2", "2026-09-30T00:00:00.000Z")
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_refresh BEFORE UPDATE ON codex_context_sessions
      BEGIN SELECT RAISE(ABORT,'w12e refresh boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e refresh boom") {
			t.Fatalf("残余刷新失败应透传: %v", err)
		}
	}
	// 会话删除失败。
	{
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_del_session BEFORE DELETE ON codex_context_sessions
      BEGIN SELECT RAISE(ABORT,'w12e session del boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e session del boom") {
			t.Fatalf("会话删除失败应透传: %v", err)
		}
	}
	// referenced key 队列清理失败。
	{
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-1", "2026-09-30T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-ref", "2026-09-30T00:00:00.000Z")
		w12eCodexShard(t, store, 0, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-ref','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_codex_queue_del BEFORE DELETE ON codex_context_storage_cleanup_queue
      BEGIN SELECT RAISE(ABORT,'w12e queue del boom'); END`)
		if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil || !strings.Contains(err.Error(), "w12e queue del boom") {
			t.Fatalf("referenced 队列清理失败应透传: %v", err)
		}
	}
	// 空会话残余跳过臂：responses 行 session_id 为空。
	{
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-empty", "", "k-e", "2026-09-30T00:00:00.000Z")
		result, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10)
		if err != nil || result.DeletedSessions != 1 {
			t.Fatalf("空 session_id 应跳过且会话删除: %+v %v", result, err)
		}
	}
}

// TestW12ECodexSettleSQLiteAbortArms：结算链触发器注入。
func TestW12ECodexSettleSQLiteAbortArms(t *testing.T) {
	ctx := context.Background()
	// 确认删除失败。
	{
		store := newKitCodexStore(t, 1)
		w12eCodexShard(t, store, 0, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_settle_del BEFORE DELETE ON codex_context_storage_cleanup_queue
      BEGIN SELECT RAISE(ABORT,'w12e settle del boom'); END`)
		if _, err := store.SettleStorageCleanup(ctx, Settlement{
			SucceededStorageKeys: []string{"k-1"},
			Failures:             []SettlementFailure{{StorageKey: "k-1", Error: "x"}},
		}); err == nil || !strings.Contains(err.Error(), "w12e settle del boom") {
			t.Fatalf("确认删除失败应透传: %v", err)
		}
	}
	// 失败延后更新失败。
	{
		store := newKitCodexStore(t, 1)
		w12eCodexShard(t, store, 0, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at, attempt_count) VALUES ('k-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z',2)`)
		w12eCodexShard(t, store, 0, `CREATE TRIGGER w12e_settle_upd BEFORE UPDATE ON codex_context_storage_cleanup_queue
      BEGIN SELECT RAISE(ABORT,'w12e settle upd boom'); END`)
		if _, err := store.SettleStorageCleanup(ctx, Settlement{
			Failures: []SettlementFailure{{StorageKey: "k-1", Error: "x"}},
		}); err == nil || !strings.Contains(err.Error(), "w12e settle upd boom") {
			t.Fatalf("延后更新失败应透传: %v", err)
		}
	}
	// 未入队 key 的失败结算（attempt 0 起步）。
	{
		store := newKitCodexStore(t, 1)
		result, err := store.SettleStorageCleanup(ctx, Settlement{
			Failures: []SettlementFailure{{StorageKey: "k-missing", Error: "boom"}},
		})
		if err != nil || result.Deferred != 0 {
			t.Fatalf("未入队 key 不应延后: %+v %v", result, err)
		}
	}
}

// TestW12ECodexStorageCleanupRetryAtArms：退避上界与抖动臂。
func TestW12ECodexStorageCleanupRetryAtArms(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	base := storageCleanupRetryAt(now, nil, 1)
	if base != "2026-09-10T00:00:30.000Z" {
		t.Fatalf("基础退避 = %q", base)
	}
	capped := storageCleanupRetryAt(now, nil, 99)
	if capped != "2026-09-10T06:00:00.000Z" {
		t.Fatalf("上界退避 = %q", capped)
	}
	jittered := storageCleanupRetryAt(now, func(int64) int64 { return 1 }, 1)
	if jittered != "2026-09-10T00:00:30.001Z" {
		t.Fatalf("抖动退避 = %q", jittered)
	}
	// 抖动溢出保护。
	overflow := storageCleanupRetryAt(now, func(int64) int64 { return 1 << 62 }, 99)
	if overflow != "2026-09-10T06:00:00.000Z" {
		t.Fatalf("溢出应回落上界: %q", overflow)
	}
	// normalizedFailures 过滤空白项。
	normalized := normalizedFailures([]SettlementFailure{
		{StorageKey: " ", Error: "x"},
		{StorageKey: "k", Error: ""},
		{StorageKey: " k ", Error: "boom"},
	})
	if len(normalized) != 1 || normalized[0].StorageKey != "k" {
		t.Fatalf("normalizedFailures = %+v", normalized)
	}
}

// TestW12ECodexPostgresArms：PG 模式逐阶段失败与全链成功。
func TestW12ECodexPostgresArms(t *testing.T) {
	ctx := context.Background()
	buildStore := func(rec *pgRecorder, failOn ...string) *CodexContextStore {
		return &CodexContextStore{Postgres: true, PG: openKitFailingRecorderPG(rec, failOn...), Now: kitNow,
			RetryJitter: func(int64) int64 { return 0 }}
	}
	// 会话查询失败。
	if _, err := buildStore(newPGRecorder(), "FROM juhe_codex_context.codex_context_sessions").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
		t.Fatalf("会话查询失败应透传")
	}
	// 会话行扫描失败（列数不匹配）。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"a", "b", "c"}, [][]driver.Value{{"s", "e", "x"}})
		if _, err := buildStore(rec).CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("会话扫描失败应透传")
		}
	}
	// 响应删除查询失败。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}})
		if _, err := buildStore(rec, "FROM juhe_codex_context.codex_context_responses").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("响应删除失败应透传")
		}
	}
	// 残余 expires 查询失败。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}})
		if _, err := buildStore(rec, "MAX(expires_at)").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("残余查询失败应透传")
		}
	}
	// 会话刷新失败（残余存在）。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}})
		rec.script("FROM juhe_codex_context.codex_context_responses", []string{"session_id", "expires_at"}, [][]driver.Value{{"s-1", "2026-09-30T00:00:00.000Z"}})
		rec.script("FROM juhe_codex_context.codex_context_compacts", []string{"session_id", "expires_at"}, [][]driver.Value{})
		if _, err := buildStore(rec, "UPDATE juhe_codex_context.codex_context_sessions").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("会话刷新失败应透传")
		}
	}
	// 会话删除失败（无残余）。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}})
		if _, err := buildStore(rec, "DELETE FROM juhe_codex_context.codex_context_sessions").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("会话删除失败应透传")
		}
	}
	// pending 队列查询失败（无会话路径）。
	{
		rec := newPGRecorder()
		if _, err := buildStore(rec, "FROM juhe_codex_context.codex_context_storage_cleanup_queue").CleanupExpiredStates(ctx, kitUpdatedAt, 10); err == nil {
			t.Fatalf("队列查询失败应透传")
		}
	}
	// referenced key 清理成功路径：pending 2 个 key，其一仍被引用。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{})
		rec.script("FROM juhe_codex_context.codex_context_storage_cleanup_queue", []string{"storage_key"}, [][]driver.Value{{"k-ref"}, {"k-free"}})
		rec.script("FROM juhe_codex_context.codex_context_responses", []string{"storage_key"}, [][]driver.Value{{"k-ref"}})
		store := buildStore(rec)
		result, err := store.CleanupExpiredStates(ctx, kitUpdatedAt, 10)
		if err != nil {
			t.Fatalf("referenced 清理失败: %v", err)
		}
		if len(result.StorageKeys) != 1 || result.StorageKeys[0] != "k-free" {
			t.Fatalf("应只返回未引用 key: %v", result.StorageKeys)
		}
	}
	// 全链成功：limit=1 触发 hasMore；残余刷新 + 删除混合。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"}, [][]driver.Value{
			{"s-1", "2026-01-01T00:00:00.000Z"}, {"s-2", "2026-01-02T00:00:00.000Z"},
		})
		rec.script("FROM juhe_codex_context.codex_context_responses", []string{"storage_key"}, [][]driver.Value{})
		// 删除阶段的 compacts 查询（单列 storage_key）。
		rec.script("FROM juhe_codex_context.codex_context_compacts", []string{"storage_key"}, [][]driver.Value{})
		// 残余 expires 查询（两列）：s-1 在 compacts 中仍有未过期行 → 刷新。
		rec.script("MAX(expires_at) AS expires_at", []string{"session_id", "expires_at"}, [][]driver.Value{{"s-1", "2026-09-30T00:00:00.000Z"}})
		store := buildStore(rec)
		result, err := store.CleanupExpiredStates(ctx, kitUpdatedAt, 1)
		// limit=1：仅处理最旧的 s-1（有残余 → 刷新，不删除）。
		if err != nil || !result.HasMore || result.DeletedSessions != 0 {
			t.Fatalf("全链结果: %+v %v", result, err)
		}
	}
	// 结算：确认删除失败 / 延后更新失败 / 成功。
	{
		if _, err := buildStore(newPGRecorder(), "DELETE FROM codex_context_storage_cleanup_queue").
			SettleStorageCleanup(ctx, Settlement{SucceededStorageKeys: []string{"k-1"}}); err == nil {
			t.Fatalf("确认删除失败应透传")
		}
		if _, err := buildStore(newPGRecorder(), "UPDATE juhe_codex_context.codex_context_storage_cleanup_queue").
			SettleStorageCleanup(ctx, Settlement{Failures: []SettlementFailure{{StorageKey: "k-1", Error: "x"}}}); err == nil {
			t.Fatalf("延后更新失败应透传")
		}
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_storage_cleanup_queue", []string{"attempt_count"}, [][]driver.Value{{int64(3)}})
		result, err := buildStore(rec).SettleStorageCleanup(ctx, Settlement{
			SucceededStorageKeys: []string{"k-ok", " "},
			Failures:             []SettlementFailure{{StorageKey: "k-fail", Error: "boom"}},
			Now:                  kitUpdatedAt,
		})
		if err != nil || result.Acknowledged != 1 || result.Deferred != 1 {
			t.Fatalf("结算结果: %+v %v", result, err)
		}
	}
	// selectRemainingExpiresAtPostgres：空 session_id 跳过臂。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_codex_context.codex_context_responses", []string{"session_id", "expires_at"}, [][]driver.Value{{"", "2026-09-30T00:00:00.000Z"}})
		store := buildStore(rec)
		tx, err := store.PG.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		remaining, err := selectRemainingExpiresAtPostgres(ctx, tx, []string{"s-1"})
		if err != nil || len(remaining) != 0 {
			t.Fatalf("空 session_id 应跳过: %v %v", remaining, err)
		}
	}
	_ = sql.ErrNoRows
}
