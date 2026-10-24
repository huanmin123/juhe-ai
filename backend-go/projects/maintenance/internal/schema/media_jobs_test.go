package schema

// M2 media_jobs 表结构测试（媒体设计 §8.2）：SQLite 形态与 PG 同构——
// kind/status CHECK 词表、JSON 列默认值、cost_usd 默认 0、时间列非空、
// 三个查询索引存在。幂等由 EnsureAllSQLite 的 IF NOT EXISTS 语义与
// golden 计数断言（sqlite_schema_test.go）覆盖。
import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openMediaJobsSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:media-jobs-shape?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := EnsureAllSQLite(context.Background(), db); err != nil {
		t.Fatalf("EnsureAllSQLite: %v", err)
	}
	return db
}

func TestMediaJobsTableShape(t *testing.T) {
	db := openMediaJobsSQLite(t)

	// 列集（含 NOT NULL DEFAULT 语义关键列）。provider_code 已随 B4① 存储
	// 迁移批退役（读面供应商经 accounts.vendor_code 水合）；owner_system_
	// account_id 是归属列（AI 问答免 Key 直调计划 §3.3，默认空串）。
	required := map[string]bool{
		"id": true, "kind": true, "api_key_id": true, "owner_system_account_id": true, "account_id": true,
		"provider_protocol_profile_id": true,
		"upstream_job_id":              true, "status": true,
		"request_snapshot_json": true, "artifact_json": true, "error_json": true,
		"usage_json": true, "cost_usd": true,
		"params_applied_json": true, "params_ignored_json": true,
		"created_at": true, "updated_at": true,
	}
	for column := range required {
		if !sqliteTableHasColumn(t, db, "media_jobs", column) {
			t.Fatalf("media_jobs missing column %s", column)
		}
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_jobs')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != len(required) {
		t.Fatalf("media_jobs columns = %d, want %d", columns, len(required))
	}

	// 合法行：默认值落库（JSON '{}'/'[]'、cost_usd 0、owner 空串——INSERT
	// 不给 owner_system_account_id 时落 DEFAULT ''）。
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, upstream_job_id, status, created_at, updated_at)
      VALUES ('video_1', 'video', 'key_1', 'acct_1', 'job_up_1', 'queued', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert valid media job: %v", err)
	}
	var snapshot, artifact, params, owner string
	var cost float64
	if err := db.QueryRow(`SELECT request_snapshot_json, artifact_json, params_applied_json, cost_usd, owner_system_account_id
      FROM media_jobs WHERE id = 'video_1'`).Scan(&snapshot, &artifact, &params, &cost, &owner); err != nil {
		t.Fatal(err)
	}
	if snapshot != "{}" || artifact != "{}" || params != "[]" || cost != 0 {
		t.Fatalf("defaults = %q %q %q %v", snapshot, artifact, params, cost)
	}
	if owner != "" {
		t.Fatalf("owner_system_account_id default = %q, want 空串", owner)
	}

	// CHECK 词表：非法 kind / status 拒绝。
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, upstream_job_id, status, created_at, updated_at)
      VALUES ('video_2', 'image', 'key_1', 'acct_1', 'job_up_2', 'queued', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err == nil {
		t.Fatal("kind=image must be rejected by CHECK")
	}
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, upstream_job_id, status, created_at, updated_at)
      VALUES ('video_3', 'video', 'key_1', 'acct_1', 'job_up_3', 'running', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err == nil {
		t.Fatal("status=running must be rejected by CHECK")
	}

	// 查询索引存在（api_key+created_at / account / status）。
	for _, indexName := range []string{"idx_media_jobs_api_key_created", "idx_media_jobs_account", "idx_media_jobs_status"} {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("index %s missing", indexName)
		}
	}
}

// TestSQLiteMediaJobsOwnerColumnBackfill 是归属改列批（AI 问答免 Key 直调计划
// §3.3）的存量库回归：旧形态表（无 owner 列）经 ensureSQLiteMediaJobsOwnerColumn
// 守卫补列并从 api_keys.system_account_id 回填一次；api_key_id 悬空的行不写
// NULL（NOT NULL 不破裂），内部通道行（api_key_id 空串）不参与回填；重复执行
// 幂等且不覆盖已有非空 owner。
func TestSQLiteMediaJobsOwnerColumnBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", "file:media-jobs-owner-backfill?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	// 旧形态 media_jobs（列集与 owner 列引入前的建表 DDL 同构）+ 最小 api_keys。
	if _, err := db.Exec(`CREATE TABLE media_jobs (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL CHECK (kind IN ('video', 'audio_transcription', 'audio_speech')),
		api_key_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		provider_protocol_profile_id TEXT,
		upstream_job_id TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired')),
		request_snapshot_json TEXT NOT NULL DEFAULT '{}',
		artifact_json TEXT NOT NULL DEFAULT '{}',
		error_json TEXT NOT NULL DEFAULT '{}',
		usage_json TEXT NOT NULL DEFAULT '{}',
		cost_usd REAL NOT NULL DEFAULT 0,
		params_applied_json TEXT NOT NULL DEFAULT '[]',
		params_ignored_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL);
		CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create legacy tables: %v", err)
	}
	for _, row := range []struct{ id, owner string }{
		{"key_1", "sys_1"}, {"key_2", "sys_2"},
	} {
		if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id) VALUES (?, ?)`, row.id, row.owner); err != nil {
			t.Fatalf("seed api key: %v", err)
		}
	}
	for _, row := range []struct{ id, apiKeyID string }{
		{"video_hit", "key_1"},
		{"video_orphan", "key_missing"},
		{"video_internal", ""},
	} {
		if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, upstream_job_id, status, created_at, updated_at)
			VALUES (?, 'video', ?, 'acct_1', 'up_1', 'queued', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`, row.id, row.apiKeyID); err != nil {
			t.Fatalf("seed media job %s: %v", row.id, err)
		}
	}

	if err := ensureSQLiteMediaJobsOwnerColumn(context.Background(), db); err != nil {
		t.Fatalf("ensure owner column: %v", err)
	}
	if !sqliteTableHasColumn(t, db, "media_jobs", "owner_system_account_id") {
		t.Fatal("owner_system_account_id 列未补齐")
	}
	owners := map[string]string{}
	rows, err := db.Query(`SELECT id, owner_system_account_id FROM media_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		owners[id] = owner
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if owners["video_hit"] != "sys_1" {
		t.Fatalf("video_hit owner = %q, want sys_1", owners["video_hit"])
	}
	// api_key_id 悬空：不参与回填，落空串（不是 NULL——相关子查询无匹配时
	// 结果为 NULL，EXISTS 守卫必须挡住，否则 NOT NULL 破裂报错）。
	if owners["video_orphan"] != "" {
		t.Fatalf("video_orphan owner = %q, want 空串（悬空 Key 不回填）", owners["video_orphan"])
	}
	if owners["video_internal"] != "" {
		t.Fatalf("video_internal owner = %q, want 空串（内部通道行不回填）", owners["video_internal"])
	}

	// 幂等：重复执行零副作用，且已有非空 owner 不被覆盖。
	if _, err := db.Exec(`UPDATE media_jobs SET owner_system_account_id = 'sys_manual' WHERE id = 'video_hit'`); err != nil {
		t.Fatal(err)
	}
	if err := ensureSQLiteMediaJobsOwnerColumn(context.Background(), db); err != nil {
		t.Fatalf("re-ensure owner column: %v", err)
	}
	var owner string
	if err := db.QueryRow(`SELECT owner_system_account_id FROM media_jobs WHERE id = 'video_hit'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "sys_manual" {
		t.Fatalf("video_hit owner after re-ensure = %q, want sys_manual（非空 owner 不覆盖）", owner)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_jobs')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 17 {
		t.Fatalf("media_jobs columns = %d, want 17", count)
	}
}
