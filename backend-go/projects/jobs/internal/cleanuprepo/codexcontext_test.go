package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codexcontext.go 的语义测试：SQLite 多分片（state-<i>.sqlite3）覆盖过期
// 会话批清理、storage key 入队、残余过期时间刷新、pending key 过滤与结算
// 退避；PG 路径用录制驱动覆盖同构语句链。

func createKitCodexShardDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open shard: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	execKitSchema(t, db,
		`CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id TEXT PRIMARY KEY, expires_at TEXT NOT NULL, updated_at TEXT DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS codex_context_responses (
      id TEXT PRIMARY KEY, session_id TEXT NOT NULL, storage_key TEXT DEFAULT '',
      expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS codex_context_compacts (
      id TEXT PRIMARY KEY, session_id TEXT NOT NULL, storage_key TEXT DEFAULT '',
      expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
      storage_key TEXT PRIMARY KEY, enqueued_at TEXT NOT NULL, updated_at TEXT NOT NULL,
      next_attempt_at TEXT NOT NULL, attempt_count INTEGER DEFAULT 0, last_error TEXT)`)
	return db
}

func newKitCodexStore(t *testing.T, shardCount int) *CodexContextStore {
	t.Helper()
	root := t.TempDir()
	store := &CodexContextStore{
		ShardRoot:   root,
		ShardCount:  shardCount,
		Now:         kitNow,
		RetryJitter: func(int64) int64 { return 0 },
	}
	for index := 0; index < shardCount; index++ {
		createKitCodexShardDB(t, store.shardPath(index))
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedKitCodexSession(t *testing.T, store *CodexContextStore, shardIndex int, id, expiresAt string) {
	t.Helper()
	db, err := store.shard(shardIndex)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	mustExecKit(t, db, `INSERT INTO codex_context_sessions (id, expires_at, updated_at) VALUES (?, ?, '2026-01-01T00:00:00.000Z')`, id, expiresAt)
}

func seedKitCodexRow(t *testing.T, store *CodexContextStore, shardIndex int, table, id, sessionID, storageKey, expiresAt string) {
	t.Helper()
	db, err := store.shard(shardIndex)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	mustExecKit(t, db, `INSERT INTO `+table+` (id, session_id, storage_key, expires_at) VALUES (?, ?, ?, ?)`,
		id, sessionID, storageKey, expiresAt)
}

// TestCodexCleanupExpiredStatesSQLite：跨分片过期清理 + 残余刷新 + storage
// key 入队 + pending 返回。
func TestCodexCleanupExpiredStatesSQLite(t *testing.T) {
	store := newKitCodexStore(t, 2)
	// shard0：完全过期的会话 + 仍在有效期内的会话。
	seedKitCodexSession(t, store, 0, "s-old", "2026-09-01T00:00:00.000Z")
	seedKitCodexSession(t, store, 0, "s-keep", "2026-10-01T00:00:00.000Z")
	seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-old", "k-old", "2026-09-01T00:00:00.000Z")
	seedKitCodexRow(t, store, 0, "codex_context_responses", "r-2", "s-keep", "k-keep", "2026-10-01T00:00:00.000Z")
	seedKitCodexRow(t, store, 0, "codex_context_compacts", "c-1", "s-old", "k-old2", "2026-08-01T00:00:00.000Z")
	// shard1：部分过期的会话（还有未过期 response → 刷新而非删除）。
	seedKitCodexSession(t, store, 1, "s-mixed", "2026-09-02T00:00:00.000Z")
	seedKitCodexRow(t, store, 1, "codex_context_responses", "r-3", "s-mixed", "k-mixed", "2026-09-02T00:00:00.000Z")
	seedKitCodexRow(t, store, 1, "codex_context_responses", "r-4", "s-mixed", "k-mixed2", "2026-12-01T00:00:00.000Z")

	result, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 100)
	if err != nil {
		t.Fatalf("CleanupExpiredStates: %v", err)
	}
	if result.DeletedSessions != 1 || result.DeletedResponses != 2 || result.DeletedCompacts != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.HasMore {
		t.Fatalf("无剩余时应 HasMore=false")
	}
	// s-old 删除、s-mixed 刷新到残余最大过期时间。
	if got := mustQueryCountKit(t, store.shards[0], `SELECT COUNT(*) FROM codex_context_sessions WHERE id = 's-old'`); got != 0 {
		t.Fatalf("完全过期会话应删除")
	}
	var refreshed string
	if err := store.shards[1].QueryRowContext(context.Background(), `SELECT expires_at FROM codex_context_sessions WHERE id = 's-mixed'`).Scan(&refreshed); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if refreshed != "2026-12-01T00:00:00.000Z" {
		t.Fatalf("残余刷新 = %q", refreshed)
	}
	// storage key 入队。
	if got := mustQueryCountKit(t, store.shards[0], `SELECT COUNT(*) FROM codex_context_storage_cleanup_queue`); got != 2 {
		t.Fatalf("shard0 队列 = %d, 期望 2", got)
	}
	// pending：三个 key 均无残余引用。
	// 顺序按分片与入队次序：shard0 的 k-old/k-old2 先于 shard1 的 k-mixed。
	if strings.Join(result.StorageKeys, ",") != "k-old,k-old2,k-mixed" {
		t.Fatalf("StorageKeys = %v", result.StorageKeys)
	}
}

// TestCodexPendingFiltersReferencedKeys：仍被引用的 key 被丢弃（含队列行删除），
// 跨分片重复 key 去重。
func TestCodexPendingFiltersReferencedKeys(t *testing.T) {
	store := newKitCodexStore(t, 2)
	seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-referenced", "2026-10-01T00:00:00.000Z")
	// 两个分片各入队一次同一 key。
	for index := 0; index < 2; index++ {
		shardDB, err := store.shard(index)
		if err != nil {
			t.Fatalf("shard: %v", err)
		}
		mustExecKit(t, shardDB, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-referenced', ?, ?, ?)`,
			kitUpdatedAt, kitUpdatedAt, kitUpdatedAt)
		mustExecKit(t, shardDB, `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-free', ?, ?, ?)`,
			kitUpdatedAt, kitUpdatedAt, kitUpdatedAt)
	}
	// 无过期会话 → 仅返回 pending。
	result, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 100)
	if err != nil {
		t.Fatalf("CleanupExpiredStates: %v", err)
	}
	if len(result.StorageKeys) != 1 || result.StorageKeys[0] != "k-free" {
		t.Fatalf("StorageKeys = %v", result.StorageKeys)
	}
	// 引用中的 key 队列行被丢弃，两分片只剩 k-free 行（去重后单行/分片）。
	for index := 0; index < 2; index++ {
		shardDB, err := store.shard(index)
		if err != nil {
			t.Fatalf("shard: %v", err)
		}
		if got := mustQueryCountKit(t, shardDB, `SELECT COUNT(*) FROM codex_context_storage_cleanup_queue`); got != 1 {
			t.Fatalf("shard%d 队列残余 = %d, 期望 1", index, got)
		}
	}
}

// TestSettleStorageCleanupSQLite：成功 key 确认删除；失败 key 计退避（含抖动）。
func TestSettleStorageCleanupSQLite(t *testing.T) {
	store := newKitCodexStore(t, 1)
	shardDB, err := store.shard(0)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	mustExecKit(t, shardDB, `INSERT INTO codex_context_storage_cleanup_queue
    (storage_key, enqueued_at, updated_at, next_attempt_at, attempt_count) VALUES
    ('k-1', ?, ?, ?, 0), ('k-2', ?, ?, ?, 2)`,
		kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt, kitUpdatedAt)

	result, err := store.SettleStorageCleanup(context.Background(), Settlement{
		SucceededStorageKeys: []string{"k-1", " "},
		Failures: []SettlementFailure{
			{StorageKey: "k-2", Error: "boom"},
			{StorageKey: " ", Error: "忽略"},
			{StorageKey: "k-3", Error: ""},
		},
		Now: kitUpdatedAt,
	})
	if err != nil {
		t.Fatalf("SettleStorageCleanup: %v", err)
	}
	if result.Acknowledged != 1 || result.Deferred != 1 {
		t.Fatalf("result = %+v", result)
	}
	var attempts int64
	var lastError, nextAttempt string
	if err := shardDB.QueryRowContext(context.Background(), `SELECT attempt_count, last_error, next_attempt_at
    FROM codex_context_storage_cleanup_queue WHERE storage_key = 'k-2'`).Scan(&attempts, &lastError, &nextAttempt); err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if attempts != 3 || lastError != "boom" {
		t.Fatalf("失败结算 = attempts %d / %q", attempts, lastError)
	}
	// attempt 2 → 第 3 次退避 30s<<2 = 120s。
	want := ISOOf(kitNow().Add(120 * time.Second))
	if nextAttempt != want {
		t.Fatalf("next_attempt_at = %q, 期望 %q", nextAttempt, want)
	}

	// 抖动注入叠加。
	store.RetryJitter = func(delayMs int64) int64 { return 5_000 }
	if _, err := store.SettleStorageCleanup(context.Background(), Settlement{
		Failures: []SettlementFailure{{StorageKey: "k-2", Error: "boom"}},
		Now:      kitUpdatedAt,
	}); err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if err := shardDB.QueryRowContext(context.Background(), `SELECT next_attempt_at
    FROM codex_context_storage_cleanup_queue WHERE storage_key = 'k-2'`).Scan(&nextAttempt); err != nil {
		t.Fatalf("read queue: %v", err)
	}
	// 首次结算已把 attempt 提到 3：第二次退避 30s<<3=240s + 抖动 5s。
	if nextAttempt != ISOOf(kitNow().Add(245*time.Second)) {
		t.Fatalf("抖动未叠加：%q", nextAttempt)
	}
}

// TestStorageCleanupRetryAt：指数退避上限与溢出防护。
func TestStorageCleanupRetryAt(t *testing.T) {
	base := kitNow()
	cases := []struct {
		attempts int
		wantMs   int64
	}{
		{0, 30_000}, {1, 30_000}, {2, 60_000}, {10, 15_360_000}, {11, 21_600_000}, {99, 21_600_000},
	}
	for _, item := range cases {
		got := storageCleanupRetryAt(base, nil, item.attempts)
		want := ISOOf(base.Add(time.Duration(item.wantMs) * time.Millisecond))
		if got != want {
			t.Fatalf("storageCleanupRetryAt(%d) = %q, 期望 %q", item.attempts, got, want)
		}
	}
	// 溢出防护：抖动超过 MaxInt64-delayMs 时封顶。
	overflow := storageCleanupRetryAt(base, func(int64) int64 { return math.MaxInt64 }, 99)
	if overflow != ISOOf(base.Add(21_600_000*time.Millisecond)) {
		t.Fatalf("溢出防护失败：%q", overflow)
	}
}

// TestCodexShardGuards：PG 模式禁用分片句柄；ShardCount 缺省 1；Close 幂等。
func TestCodexShardGuards(t *testing.T) {
	store := newKitCodexStore(t, 1)
	pgStore := &CodexContextStore{Postgres: true}
	if _, err := pgStore.shard(0); err == nil {
		t.Fatalf("PG 模式应拒绝分片句柄")
	}
	if indexes := (&CodexContextStore{}).shardIndexes(); len(indexes) != 1 || indexes[0] != 0 {
		t.Fatalf("ShardCount 缺省 = %v", indexes)
	}
	if _, err := store.shard(0); err != nil {
		t.Fatalf("shard: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close 应幂等：%v", err)
	}
	// Close 后缓存清空：可重新打开新句柄。
	if _, err := store.shard(0); err != nil {
		t.Fatalf("关闭后重开失败: %v", err)
	}
}

// TestCodexCleanupExpiredPostgres：录制驱动覆盖 PG 同构语句链。
func TestCodexCleanupExpiredPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &CodexContextStore{Postgres: true, PG: openRecorderPG(rec), Now: kitNow}
	rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"},
		[][]driver.Value{{"s-1", "2026-09-01T00:00:00.000Z"}, {"s-2", "2026-09-02T00:00:00.000Z"}})
	rec.script("juhe_codex_context.codex_context_responses\n      WHERE session_id IN", []string{"storage_key"},
		[][]driver.Value{{"k-1"}})
	rec.script("juhe_codex_context.codex_context_compacts\n      WHERE session_id IN", []string{"storage_key"}, nil)
	rec.script("juhe_codex_context.codex_context_responses\n        WHERE session_id IN", []string{"session_id", "expires_at"},
		[][]driver.Value{{"s-2", "2026-12-01T00:00:00.000Z"}})
	rec.script("juhe_codex_context.codex_context_compacts\n        WHERE session_id IN", []string{"session_id", "expires_at"}, nil)
	rec.script("WHERE next_attempt_at <=", []string{"storage_key"},
		[][]driver.Value{{"k-1"}, {"k-9"}})
	rec.script("juhe_codex_context.codex_context_responses\n        WHERE storage_key IN", []string{"storage_key"},
		[][]driver.Value{{"k-9"}})
	rec.script("juhe_codex_context.codex_context_compacts\n        WHERE storage_key IN", []string{"storage_key"}, nil)

	result, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 2)
	if err != nil {
		t.Fatalf("CleanupExpiredStates: %v", err)
	}
	if result.DeletedSessions != 1 || result.DeletedResponses != 1 || result.DeletedCompacts != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.StorageKeys) != 1 || result.StorageKeys[0] != "k-1" {
		t.Fatalf("StorageKeys = %v", result.StorageKeys)
	}
	// 语句家族断言：responses/compacts 删除、会话刷新、引用 key 队列清理。
	joined := make([]string, 0, 8)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"DELETE FROM juhe_codex_context.codex_context_responses",
		"UPDATE juhe_codex_context.codex_context_sessions",
		"DELETE FROM juhe_codex_context.codex_context_sessions WHERE id =",
		"INSERT INTO juhe_codex_context.codex_context_storage_cleanup_queue",
		"DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key IN",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 PG 语句：%s", needle)
		}
	}
}

// TestSettleStorageCleanupPostgres：PG 结算（attempt 读取 + UPDATE 退避）。
func TestSettleStorageCleanupPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &CodexContextStore{Postgres: true, PG: openRecorderPG(rec), Now: kitNow}
	rec.script("SELECT attempt_count", []string{"attempt_count"}, [][]driver.Value{{int64(2)}})

	result, err := store.SettleStorageCleanup(context.Background(), Settlement{
		SucceededStorageKeys: []string{"k-1"},
		Failures:             []SettlementFailure{{StorageKey: "k-2", Error: "boom"}},
		Now:                  kitUpdatedAt,
	})
	if err != nil {
		t.Fatalf("SettleStorageCleanup: %v", err)
	}
	if result.Acknowledged != 1 || result.Deferred != 1 {
		t.Fatalf("result = %+v", result)
	}
	var deferredUpdate *recordedStatement
	for index := range rec.statements {
		if strings.Contains(rec.statements[index].query, "UPDATE juhe_codex_context.codex_context_storage_cleanup_queue") {
			deferredUpdate = &rec.statements[index]
		}
	}
	if deferredUpdate == nil {
		t.Fatalf("缺少退避 UPDATE")
	}
	// attempt 2 → 第 3 次退避 120s。
	if fmtNextAttempt := fmt.Sprintf("%v", deferredUpdate.args[2]); fmtNextAttempt != ISOOf(kitNow().Add(120*time.Second)) {
		t.Fatalf("next_attempt_at = %v", deferredUpdate.args[2])
	}
}
