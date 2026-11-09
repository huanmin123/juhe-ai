package playground

// PostgreSQL 暂存后端集成测试（2026-11-09：产物由本地文件改为 PG bytea）。
// 外部依赖未授权时 pgtestkit 自动 SKIP；授权后覆盖覆盖替换、归属隔离、媒体
// 读取与 24h 懒清理。

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
			created_at bigint NOT NULL,
			PRIMARY KEY (system_account_id, id))`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_playground_artifacts_created_at ON juhe_business.playground_artifacts (created_at)`)
		return err
	})
}

func TestPGArtifactStoreReplaceListReadAndCleanup(t *testing.T) {
	store := NewPGArtifactStore(newPlaygroundArtifactsPG(t))
	if !store.available() {
		t.Fatal("PG store 应 available")
	}

	first, err := store.replaceImages("sys_user_1", []decodedImage{{Data: []byte("png-bytes"), MIME: "image/png"}})
	if err != nil || len(first) != 1 {
		t.Fatalf("replaceImages = %v err=%v", first, err)
	}
	listing, err := store.list("sys_user_1")
	if err != nil || len(listing.Images) != 1 || listing.Images[0].ID != first[0].ID {
		t.Fatalf("list = %+v err=%v", listing, err)
	}
	content, err := store.openArtifact("sys_user_1", first[0].ID)
	if err != nil || string(content.Data) != "png-bytes" || content.MIME != "image/png" {
		t.Fatalf("openArtifact = %+v err=%v", content, err)
	}
	if _, err := store.openArtifact("sys_user_2", first[0].ID); !errors.Is(err, errArtifactNotFound) {
		t.Fatalf("跨用户读取必须 not found，得到 %v", err)
	}

	// 覆盖替换：旧图消失，只剩新批。
	second, err := store.replaceImages("sys_user_1", []decodedImage{{Data: []byte("png-2"), MIME: "image/png"}})
	if err != nil || len(second) != 1 {
		t.Fatalf("second replaceImages = %v err=%v", second, err)
	}
	listing, err = store.list("sys_user_1")
	if err != nil || len(listing.Images) != 1 || listing.Images[0].ID != second[0].ID {
		t.Fatalf("覆盖替换后 list = %+v err=%v", listing, err)
	}
	if _, err := store.openArtifact("sys_user_1", first[0].ID); !errors.Is(err, errArtifactNotFound) {
		t.Fatalf("旧图必须被删除，得到 %v", err)
	}

	speechID, err := store.replaceSpeech("sys_user_1", []byte("wav-bytes"), "wav")
	if err != nil || speechID == "" {
		t.Fatalf("replaceSpeech id=%q err=%v", speechID, err)
	}
	if err := store.replaceVideoTask("sys_user_1", videoTaskRef{JobID: "job_v1", AccountID: "acc_1", ModelID: "m1", Status: "queued", CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	listing, err = store.list("sys_user_1")
	if err != nil || listing.Speech == nil || listing.Speech.MimeType != "audio/wav" || listing.Video == nil || listing.Video.JobID != "job_v1" {
		t.Fatalf("speech/video 清单 = %+v err=%v", listing, err)
	}

	// 24h 懒清理：注入一条过期行，清理后仅过期行消失。
	oldID := "old-artifact-1"
	if _, err := store.(*pgArtifactStore).db.Exec(`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,created_at) VALUES ($1,$2,'image','image/png',$3,$4)`,
		"sys_user_1", oldID, []byte("old"), time.Now().Add(-25*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	store.cleanupExpired()
	if _, err := store.openArtifact("sys_user_1", oldID); !errors.Is(err, errArtifactNotFound) {
		t.Fatalf("过期行必须被清理，得到 %v", err)
	}
	if _, err := store.openArtifact("sys_user_1", second[0].ID); err != nil {
		t.Fatalf("新产物不得被清理: %v", err)
	}
}
