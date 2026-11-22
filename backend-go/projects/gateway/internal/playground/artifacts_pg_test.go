package playground

// PostgreSQL 暂存后端集成测试（2026-11-09：产物由本地文件改为 PG bytea；
// v1.2：job_id 列 + 图像产物写入面）。外部依赖未授权时 pgtestkit 自动 SKIP；
// 授权后覆盖图像产物写入、归属隔离、媒体读取与 24h 懒清理（孤儿参考图口径）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func newPlaygroundArtifactsPG(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS juhe_business.playground_artifacts (
			system_account_id text NOT NULL,
			id text NOT NULL,
			kind text NOT NULL,
			mime_type text NOT NULL DEFAULT '',
			data bytea NOT NULL,
			job_id text NOT NULL DEFAULT '',
			created_at bigint NOT NULL,
			PRIMARY KEY (system_account_id, id))`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_playground_artifacts_created_at ON juhe_business.playground_artifacts (created_at)`)
		return err
	})
}

func TestPGArtifactStoreSaveListReadAndCleanup(t *testing.T) {
	store := NewPGArtifactStore(newPlaygroundArtifactsPG(t))
	if !store.available() {
		t.Fatal("PG store 应 available")
	}

	// 图像产物写入（参考图孤儿态 + 结果图绑定任务）。
	referenceID, err := store.saveImageReference("sys_user_1", decodedImage{Data: []byte("ref-png"), MIME: "image/png"})
	if err != nil || referenceID == "" {
		t.Fatalf("saveImageReference = %q err=%v", referenceID, err)
	}
	resultID, err := store.saveImageResult("sys_user_1", "job_1", decodedImage{Data: []byte("result-png"), MIME: "image/png"})
	if err != nil || resultID == "" {
		t.Fatalf("saveImageResult = %q err=%v", resultID, err)
	}
	var jobID string
	if err := store.(*pgArtifactStore).db.QueryRow(`SELECT job_id FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", resultID).Scan(&jobID); err != nil || jobID != "job_1" {
		t.Fatalf("结果图 job_id = %q err=%v", jobID, err)
	}
	if err := store.(*pgArtifactStore).db.QueryRow(`SELECT job_id FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", referenceID).Scan(&jobID); err != nil || jobID != "" {
		t.Fatalf("参考图孤儿态 job_id = %q err=%v", jobID, err)
	}

	// 语音/视频（v1.0 口径不变）。
	speechID, err := store.replaceSpeech("sys_user_1", []byte("wav-bytes"), "wav")
	if err != nil || speechID == "" {
		t.Fatalf("replaceSpeech id=%q err=%v", speechID, err)
	}
	if err := store.replaceVideoTask("sys_user_1", videoTaskRef{JobID: "job_v1", AccountID: "acc_1", ModelID: "m1", Status: "queued", CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	listing, err := store.list("sys_user_1")
	if err != nil || listing.Speech == nil || listing.Speech.MimeType != "audio/wav" || listing.Video == nil || listing.Video.JobID != "job_v1" {
		t.Fatalf("speech/video 清单 = %+v err=%v", listing, err)
	}

	// content 读取：图像产物（结果图/参考图）与语音可取；跨用户 404。
	content, err := store.openArtifact("sys_user_1", resultID)
	if err != nil || string(content.Data) != "result-png" || content.MIME != "image/png" {
		t.Fatalf("openArtifact(result) = %+v err=%v", content, err)
	}
	if _, err := store.openArtifact("sys_user_1", referenceID); err != nil {
		t.Fatalf("参考图 content 读取失败: %v", err)
	}
	if _, err := store.openArtifact("sys_user_2", resultID); !errors.Is(err, errArtifactNotFound) {
		t.Fatalf("跨用户读取必须 not found，得到 %v", err)
	}

	// 24h 懒清理（v1.2 口径）：孤儿参考图与语音过期行删除；绑定任务的图像
	// 产物不在懒清理范围（jobs 定时任务按 7d 保留期删）。
	cutoff := time.Now().Add(-25 * time.Hour).UnixMilli()
	type staleRow struct {
		id            string
		kind          string
		mime          string
		data          []byte
		jobID         string
		expectCleanup bool
	}
	staleRows := []staleRow{
		{id: "stale-orphan-ref", kind: "image_reference", mime: "image/png", data: []byte("old-ref"), jobID: "", expectCleanup: true},
		{id: "stale-speech", kind: "speech", mime: "audio/wav", data: []byte("old-wav"), jobID: "", expectCleanup: true},
		{id: "stale-bound-result", kind: "image_result", mime: "image/png", data: []byte("old-result"), jobID: "job_old", expectCleanup: false},
	}
	for _, row := range staleRows {
		if _, err := store.(*pgArtifactStore).db.Exec(`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,job_id,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			"sys_user_1", row.id, row.kind, row.mime, row.data, row.jobID, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	store.cleanupExpired()
	for _, row := range staleRows {
		_, err := store.openArtifact("sys_user_1", row.id)
		if row.expectCleanup && !errors.Is(err, errArtifactNotFound) {
			t.Fatalf("过期孤儿行 %s 必须被懒清理，得到 %v", row.id, err)
		}
		if !row.expectCleanup && err != nil {
			t.Fatalf("绑定任务的产物 %s 不得被懒清理（jobs 任务职责）: %v", row.id, err)
		}
	}
	if _, err := store.openArtifact("sys_user_1", resultID); err != nil {
		t.Fatalf("新鲜产物不得被清理: %v", err)
	}
}
