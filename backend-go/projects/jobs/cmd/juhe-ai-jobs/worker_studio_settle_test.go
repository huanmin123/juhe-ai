package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedStudioSettleFixture(t *testing.T, dir string) *sql.DB {
	t.Helper()
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business,
		`CREATE TABLE IF NOT EXISTS studio_tasks (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			client_operation_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL,
			settlement TEXT NOT NULL,
			account_id TEXT,
			provider_code TEXT,
			upstream_job_id TEXT,
			request_snapshot_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}',
			asset_id TEXT,
			poll_bookmark_json TEXT NOT NULL DEFAULT '{}',
			lease_owner TEXT,
			lease_expires_at TEXT,
			lease_attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (system_account_id, client_operation_id))`,
		`CREATE TABLE IF NOT EXISTS studio_assets (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			mime TEXT NOT NULL,
			storage_key TEXT NOT NULL UNIQUE,
			bytes INTEGER NOT NULL,
			width INTEGER,
			height INTEGER,
			duration_ms INTEGER,
			sha256 TEXT NOT NULL,
			source_task_id TEXT,
			params_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_studio_assets_owner_source_task
			ON studio_assets(system_account_id, source_task_id) WHERE source_task_id IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS studio_user_asset_usage (
			system_account_id TEXT PRIMARY KEY,
			asset_bytes INTEGER NOT NULL DEFAULT 0,
			asset_count INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS media_jobs (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			upstream_job_id TEXT NOT NULL,
			status TEXT NOT NULL,
			request_snapshot_json TEXT NOT NULL DEFAULT '{}',
			artifact_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}',
			usage_json TEXT NOT NULL DEFAULT '{}',
			cost_usd REAL NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT 'general')`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			credentials_encrypted TEXT NOT NULL DEFAULT '',
			protocol_code TEXT NOT NULL DEFAULT '',
			protocol_version TEXT NOT NULL DEFAULT '',
			deleted_at TEXT)`)
	return business
}

func TestStudioSettleWorkerWiringAndSettlementRound(t *testing.T) {
	dir := t.TempDir()
	seedDataRetention(t, dir)
	business := seedStudioSettleFixture(t, dir)

	// 上游 mock：轮询返回 completed（content 数组为契约形态，下载定位实际走
	// BuildContentRequest 形态：base_url + job id → GET /v1/videos/{raw}/content），
	// /content 返回视频字节。请求形态与认证断言对齐 gateway 任务面
	//（GET /v1/videos/{raw}，账户原凭据 Bearer）。
	var contentBytes = []byte("\x00\x00\x00\x18ftypmp42-studio-settle-wiring-bytes")
	pollHits := 0
	contentHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			contentHits++
			if auth := r.Header.Get("Authorization"); auth != "Bearer sk-upstream-account" {
				t.Errorf("上游下载认证 = %q, want 账户凭据 Bearer", auth)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(contentBytes)
		case strings.HasSuffix(r.URL.Path, "/videos/up-raw-1"):
			pollHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"up-raw-1","object":"video","status":"completed","seconds_length":4,"content":[{"status":"completed","type":"video","url":"https://content.invalid/raw.mp4"}]}`))
		default:
			t.Errorf("意外上游请求: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	mustExec(t, business,
		`INSERT INTO api_keys (id, system_account_id, purpose) VALUES ('studiokey-1', 'user-a', 'studio')`,
		`INSERT INTO accounts (id, system_account_id, provider_code, type, status, credentials_encrypted)
		 VALUES ('acc-1', 'user-a', 'openai', 'api_key', 'active', '{"api_key":"sk-upstream-account","base_url":"`+upstream.URL+`"}')`)
	if _, err := business.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
		upstream_job_id, status, created_at, updated_at)
		VALUES ('mj-1', 'video', 'studiokey-1', 'acc-1', 'openai', 'up-raw-1', 'completed', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, status, settlement,
		account_id, provider_code, upstream_job_id, created_at, updated_at)
		VALUES ('stask-1', 'user-a', 'op-1', 'video', 'completed', 'pending_settled',
		'acc-1', 'openai', 'mj-1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	config := retentionTestConfig(dir)
	assembly, err := buildWorkerAssembly(config, slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if _, ok := assembly.wiredTasks["studio-tasks-settle"]; !ok {
		t.Fatal("studio-tasks-settle 未在组合根装配（GoWired 登记但缺 worker 装配）")
	}

	// 零任务轮 + 实任务轮共用同一任务函数（runWiredJobOnce = 调度器消费面）。
	if _, err := assembly.runWiredJobOnce(context.Background(), "studio-tasks-settle"); err != nil {
		t.Fatalf("studio-tasks-settle 首轮: %v", err)
	}
	if pollHits == 0 {
		var dbgStatus, dbgSettlement, dbgError, dbgLease string
		_ = business.QueryRow(`SELECT status, settlement, error_json, COALESCE(lease_owner, '') FROM studio_tasks WHERE id = 'stask-1'`).
			Scan(&dbgStatus, &dbgSettlement, &dbgError, &dbgLease)
		t.Fatalf("结算轮未触发上游轮询（worker 装配未生效）: task=%s/%s lease=%s error=%s", dbgStatus, dbgSettlement, dbgLease, dbgError)
	}
	if contentHits != 1 {
		t.Fatalf("产物下载次数 = %d, want 1", contentHits)
	}

	var status, settlement, assetID string
	if err := business.QueryRow(`SELECT status, settlement, COALESCE(asset_id, '') FROM studio_tasks WHERE id = 'stask-1'`).
		Scan(&status, &settlement, &assetID); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || settlement != "settled" || assetID == "" {
		t.Fatalf("端到端结算态 = %s/%s/%q, want completed/settled/非空", status, settlement, assetID)
	}
	var kind, mime, storageKey string
	var bytes int64
	var durationMs sql.NullInt64
	if err := business.QueryRow(`SELECT kind, mime, storage_key, bytes, duration_ms FROM studio_assets WHERE source_task_id = 'stask-1'`).
		Scan(&kind, &mime, &storageKey, &bytes, &durationMs); err != nil {
		t.Fatalf("产物行未落库: %v", err)
	}
	if kind != "video" || mime != "video/mp4" || bytes != int64(len(contentBytes)) {
		t.Fatalf("产物行 = %s/%s/%d, want video/video/mp4/%d", kind, mime, bytes, len(contentBytes))
	}
	if !durationMs.Valid || durationMs.Int64 != 4000 {
		t.Fatalf("duration_ms = %v, want 4000（seconds_length=4 换算）", durationMs)
	}
	var usageBytes, usageCount int64
	if err := business.QueryRow(`SELECT asset_bytes, asset_count FROM studio_user_asset_usage WHERE system_account_id = 'user-a'`).
		Scan(&usageBytes, &usageCount); err != nil {
		t.Fatalf("配额账本未增量: %v", err)
	}
	if usageCount != 1 || usageBytes != int64(len(contentBytes)) {
		t.Fatalf("配额账本 = %d/%d, want 1/%d", usageBytes, usageCount, len(contentBytes))
	}

	// 对象落在与 gateway 同根的 ChatAssetsRoot（storage key 摘要分桶形状）。
	objectPath := filepath.Join(config.ChatAssetsRoot, filepath.FromSlash(storageKey))
	written, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatalf("产物对象未落在 ChatAssetsRoot: %v", err)
	}
	if len(written) != len(contentBytes) {
		t.Fatalf("产物对象字节 = %d, want %d", len(written), len(contentBytes))
	}

	// 幂等重放：第二轮任务已 settled 不再领取（上游零新增调用）。
	if _, err := assembly.runWiredJobOnce(context.Background(), "studio-tasks-settle"); err != nil {
		t.Fatalf("studio-tasks-settle 重放轮: %v", err)
	}
	if pollHits != 1 || contentHits != 1 {
		t.Fatalf("重放轮触发上游调用: poll=%d content=%d, want 1/1", pollHits, contentHits)
	}
	if count := queryCount(t, business, `SELECT COUNT(*) FROM studio_assets`); count != 1 {
		t.Fatalf("重放后资产行 = %d, want 1（三道闸幂等）", count)
	}
}

func queryCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
