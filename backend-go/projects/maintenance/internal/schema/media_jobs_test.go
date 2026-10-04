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

	// 列集（含 NOT NULL DEFAULT 语义关键列）。
	required := map[string]bool{
		"id": true, "kind": true, "api_key_id": true, "account_id": true,
		"provider_code": true, "provider_protocol_profile_id": true,
		"upstream_job_id": true, "status": true,
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

	// 合法行：默认值落库（JSON '{}'/'[]'、cost_usd 0）。
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
      upstream_job_id, status, created_at, updated_at)
      VALUES ('video_1', 'video', 'key_1', 'acct_1', 'gpt', 'job_up_1', 'queued', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert valid media job: %v", err)
	}
	var snapshot, artifact, params string
	var cost float64
	if err := db.QueryRow(`SELECT request_snapshot_json, artifact_json, params_applied_json, cost_usd
      FROM media_jobs WHERE id = 'video_1'`).Scan(&snapshot, &artifact, &params, &cost); err != nil {
		t.Fatal(err)
	}
	if snapshot != "{}" || artifact != "{}" || params != "[]" || cost != 0 {
		t.Fatalf("defaults = %q %q %q %v", snapshot, artifact, params, cost)
	}

	// CHECK 词表：非法 kind / status 拒绝。
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
      upstream_job_id, status, created_at, updated_at)
      VALUES ('video_2', 'image', 'key_1', 'acct_1', 'gpt', 'job_up_2', 'queued', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err == nil {
		t.Fatal("kind=image must be rejected by CHECK")
	}
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
      upstream_job_id, status, created_at, updated_at)
      VALUES ('video_3', 'video', 'key_1', 'acct_1', 'gpt', 'job_up_3', 'running', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z')`); err == nil {
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
