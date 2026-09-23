package accounts

// 生产 SQLite 双文件回归：account_usage_snapshots 只存在于独立 stats 库，
// account_usage_snapshots 三条语句（openai_codex 快照读 / relay_balance 快照
// 读 / 旧快照清理 DELETE）经 AttachStatsDatabase 落 stats 句柄；单文件测试
// （nil 回退共享句柄）与 PG 同池语义不回归。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func openSplitStatsDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "stats.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	// 列集与 maintenance/internal/schema/sqlite_schema_stats.go 对齐（本包
	// accounts_test.go 的单文件 DDL 同源）。
	_, err = handle.Exec(`CREATE TABLE IF NOT EXISTS account_usage_snapshots (
		system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('openai_codex', 'relay_balance')),
		source TEXT,
		snapshot_json TEXT NOT NULL,
		refresh_status TEXT,
		last_attempt_at TEXT,
		last_success_at TEXT,
		next_refresh_after TEXT,
		last_error_message TEXT,
		updated_at TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, account_id, kind)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func statsDBRowCount(t *testing.T, handle *sql.DB, kind, accountID string) int {
	t.Helper()
	var count int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM account_usage_snapshots WHERE kind = ? AND account_id = ?`,
		kind, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStatsDatabaseSplitFilesListAndSnapshotReads(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-statsdb-oauth", adminID, "statsdb-oauth", "active")
	env.exec(t, `UPDATE accounts SET type = 'oauth' WHERE id = 'acc-statsdb-oauth'`)

	// 修复前的失败面：业务库没有 account_usage_snapshots 表，独立 stats 库才有。
	statsHandle := openSplitStatsDB(t)
	env.store.AttachStatsDatabase(statsHandle)

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	codexPayload, err := json.Marshal(map[string]any{
		"codex_5h_used_percent":        42.5,
		"codex_5h_reset_after_seconds": 3600,
		"codex_5h_window_minutes":      300,
		"codex_5h_reset_at":            now,
		"codex_7d_used_percent":        61.0,
		"codex_7d_reset_after_seconds": 86400,
		"codex_7d_window_minutes":      10080,
		"codex_7d_reset_at":            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statsHandle.Exec(`INSERT INTO account_usage_snapshots
		(system_account_id, account_id, kind, source, snapshot_json, refresh_status,
		 last_attempt_at, last_success_at, next_refresh_after, updated_at, created_at)
		VALUES (?, 'acc-statsdb-oauth', 'openai_codex', 'codex', ?, 'ok', ?, ?, ?, ?, ?)`,
		adminID, string(codexPayload), now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := statsHandle.Exec(`INSERT INTO account_usage_snapshots
		(system_account_id, account_id, kind, source, snapshot_json, updated_at, created_at)
		VALUES (?, 'acc-statsdb-oauth', 'relay_balance', 'relay', '{"balance":1}', ?, ?)`,
		adminID, now, now); err != nil {
		t.Fatal(err)
	}

	// 账户列表（此前 no such table → 500）。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("双文件拆分下列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	oauthUsage, ok := items["acc-statsdb-oauth"]["oauthUsage"].(map[string]any)
	if !ok {
		t.Fatalf("oauthUsage 应从独立 stats 库水合：%v", items["acc-statsdb-oauth"])
	}
	if oauthUsage["fiveHour"] == nil || oauthUsage["sevenDay"] == nil {
		t.Fatalf("5h/7d 窗口应解析：%v", oauthUsage)
	}

	// relay_balance 快照读（此前 no such table）。
	record, err := env.store.balanceService().LoadBalanceSnapshotRecord(context.Background(), "acc-statsdb-oauth")
	if err != nil {
		t.Fatalf("relay_balance 快照读应落 stats 句柄：%v", err)
	}
	if record == nil || record.Snapshot["balance"] != float64(1) {
		t.Fatalf("relay_balance 快照应读出：%v", record)
	}

	// 旧快照清理 DELETE（此前 no such table）。
	cleaner := &StoreBalanceSnapshotCleaner{store: env.store, now: time.Now, suppressedItems: map[string]cleanupQueueItem{}, exhaustedAccounts: map[string]bool{}}
	if err := cleaner.deleteSupersededSnapshot(context.Background(), BalanceSnapshotCleanupRequest{AccountID: "acc-statsdb-oauth"}); err != nil {
		t.Fatalf("快照清理 DELETE 应落 stats 句柄：%v", err)
	}
	if got := statsDBRowCount(t, statsHandle, "relay_balance", "acc-statsdb-oauth"); got != 0 {
		t.Fatalf("relay_balance 行应被删除，剩余 %d", got)
	}
	if got := statsDBRowCount(t, statsHandle, "openai_codex", "acc-statsdb-oauth"); got != 1 {
		t.Fatalf("openai_codex 行不应波及，剩余 %d", got)
	}
}
