// jobsrepo_test.go 是 media_jobs 归属语义的仓储直连回归（AI 问答免 Key 直调
// 计划 §3.3）：owner 列读写、内外双归属键分流（外部 Key 级隔离保持）、owner
// 过滤不经 api_keys join 推导。
package gatewaymedia

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openJobsRepoSQLite 打开仓储单测库：media_jobs 与 maintenance 建表 DDL 同构
// （含 owner_system_account_id），accounts 是 vendor 水合 JOIN 的最小表，
// api_keys 仅用于证明 owner 过滤不依赖它。
func openJobsRepoSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "media-jobs-owner.sqlite3"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE media_jobs (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL CHECK (kind IN ('video', 'audio_transcription', 'audio_speech')),
		api_key_id TEXT NOT NULL,
		owner_system_account_id TEXT NOT NULL DEFAULT '',
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
		CREATE TABLE accounts (id TEXT PRIMARY KEY, vendor_code TEXT);
		CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

// ownerJobRecordOf 构造一行带归属的任务（Insert 由仓储时间源写 created_at）。
func ownerJobRecordOf(id, apiKeyID, ownerSystemAccountID string) MediaJobRecord {
	return MediaJobRecord{
		ID:                   id,
		Kind:                 JobKindVideo,
		APIKeyID:             apiKeyID,
		OwnerSystemAccountID: ownerSystemAccountID,
		AccountID:            "acc_1",
		UpstreamJobID:        "up_" + id,
		Status:               JobStatusQueued,
		RequestSnapshot:      MediaJobRequestSnapshot{Model: "sora-2", Prompt: "owner"},
	}
}

func seedOwnerJob(t *testing.T, repo *MediaJobsRepo, record MediaJobRecord) {
	t.Helper()
	if err := repo.Insert(context.Background(), record); err != nil {
		t.Fatalf("insert %s: %v", record.ID, err)
	}
}

func TestMediaJobsRepoOwnerSemantics(t *testing.T) {
	db := openJobsRepoSQLite(t)
	repo := NewMediaJobsRepo(db, false, time.Now)
	ctx := context.Background()

	// sys_1 名下两个 Key（key_1/key_2）与一条内部通道行（key 为空）；sys_2 一行。
	seedOwnerJob(t, repo, ownerJobRecordOf("video_a1", "key_1", "sys_1"))
	seedOwnerJob(t, repo, ownerJobRecordOf("video_a2", "key_2", "sys_1"))
	seedOwnerJob(t, repo, ownerJobRecordOf("video_internal", "", "sys_1"))
	seedOwnerJob(t, repo, ownerJobRecordOf("video_b1", "key_3", "sys_2"))

	// 写入回读：owner 列随 Insert 落库、随查询水合。
	loaded, err := repo.GetByIDAndOwner(ctx, "video_a1", "sys_1")
	if err != nil {
		t.Fatalf("get by owner: %v", err)
	}
	if loaded.OwnerSystemAccountID != "sys_1" || loaded.APIKeyID != "key_1" {
		t.Fatalf("loaded = %#v", loaded)
	}

	// GetByIDAndOwner：跨属主与空 owner 一律未命中。
	if _, err := repo.GetByIDAndOwner(ctx, "video_a1", "sys_2"); !errors.Is(err, ErrMediaJobNotFound) {
		t.Fatalf("cross owner err = %v, want ErrMediaJobNotFound", err)
	}
	if _, err := repo.GetByIDAndOwner(ctx, "video_a1", ""); !errors.Is(err, ErrMediaJobNotFound) {
		t.Fatalf("empty owner err = %v, want ErrMediaJobNotFound", err)
	}

	// 外部 Key 级隔离保持：同一系统账户下两个 Key 的任务互不可见
	//（GetByIDAndAPIKey 仍按 api_key_id 过滤，语义与归属列引入前一致）。
	if _, err := repo.GetByIDAndAPIKey(ctx, "video_a1", "key_1"); err != nil {
		t.Fatalf("own key get: %v", err)
	}
	if _, err := repo.GetByIDAndAPIKey(ctx, "video_a2", "key_1"); !errors.Is(err, ErrMediaJobNotFound) {
		t.Fatalf("same-owner other key err = %v, want ErrMediaJobNotFound（Key 级隔离被破坏）", err)
	}
	// 内部通道行（api_key_id 空串）只按 owner 可见，任何 Key 都取不到。
	if _, err := repo.GetByIDAndAPIKey(ctx, "video_internal", "key_1"); !errors.Is(err, ErrMediaJobNotFound) {
		t.Fatalf("internal row via key err = %v, want ErrMediaJobNotFound", err)
	}

	// 列表分流：ListByOwner 聚合同属主全部 Key 的行（3 行），Key 级列表各行其是。
	byOwner, err := repo.ListByOwner(ctx, "sys_1", 10, 0)
	if err != nil || len(byOwner) != 3 {
		t.Fatalf("list by owner: %v %d, want 3", err, len(byOwner))
	}
	byKey, err := repo.ListByAPIKey(ctx, "key_1", 10, 0)
	if err != nil || len(byKey) != 1 || byKey[0].ID != "video_a1" {
		t.Fatalf("list by key: %v %#v, want video_a1 单行", err, byKey)
	}
	byOwnerKind, err := repo.ListByOwnerAndKind(ctx, "sys_1", JobKindVideo, 10, 0)
	if err != nil || len(byOwnerKind) != 3 {
		t.Fatalf("list by owner kind: %v %d, want 3", err, len(byOwnerKind))
	}
	if miss, err := repo.ListByOwnerAndKind(ctx, "sys_1", JobKindAudioTranscription, 10, 0); err != nil || len(miss) != 0 {
		t.Fatalf("list by owner wrong kind: %v %d, want 0", err, len(miss))
	}

	// owner 过滤不经 api_keys join：删光 api_keys 行后，GetByIDAndOwner /
	// GetByOwnerAndID / ListByOwner / 管理面 owner 过滤全部仍按 owner 列命中。
	if _, err := db.Exec(`DELETE FROM api_keys`); err != nil {
		t.Fatalf("delete api_keys: %v", err)
	}
	if _, err := repo.GetByIDAndOwner(ctx, "video_a1", "sys_1"); err != nil {
		t.Fatalf("get by owner after api_keys purge: %v", err)
	}
	if _, err := repo.GetByOwnerAndID(ctx, "video_a1", "sys_1"); err != nil {
		t.Fatalf("user-face get by owner after api_keys purge: %v", err)
	}
	byOwner, err = repo.ListByOwner(ctx, "sys_1", 10, 0)
	if err != nil || len(byOwner) != 3 {
		t.Fatalf("list by owner after api_keys purge: %v %d, want 3", err, len(byOwner))
	}
	rows, total, err := repo.ListManagement(ctx, MediaJobListQuery{OwnerSystemAccountID: "sys_1", Limit: 10})
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("management owner filter after api_keys purge: %v total=%d rows=%d, want 3/3", err, total, len(rows))
	}
	// 管理面 APIKeyID 过滤保留（按 Key 筛历史行），与 owner 过滤并存。
	if _, keyTotal, err := repo.ListManagement(ctx, MediaJobListQuery{APIKeyID: "key_1", Limit: 10}); err != nil || keyTotal != 1 {
		t.Fatalf("management api key filter: %v total=%d, want 1", err, keyTotal)
	}
}
