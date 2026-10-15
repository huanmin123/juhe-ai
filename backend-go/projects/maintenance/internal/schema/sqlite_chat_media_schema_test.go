package schema

// M7 chat 媒体资产约束迁移的 SQLite 覆盖（问答音视频工具设计 §3）：
// chat_assets 词表/ready/preview 三条 CHECK 的重建守卫 + 媒体工具四列的
// 幂等补齐。模式沿 TestEnsureSQLiteBusinessScheduleIntervalCheckMigration。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// legacyChatAssetsMediaDDL 是 M7 之前的 chat_assets 完整形状（图像词表 +
// 宽高/预览必备；列集与现役 DDL 一致，仅三条 CHECK 为旧约束）。
const legacyChatAssetsMediaDDL = `CREATE TABLE chat_assets (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      source_kind TEXT NOT NULL DEFAULT 'user_upload',
      original_filename TEXT NOT NULL,
      original_mime_type TEXT NOT NULL,
      original_width INTEGER,
      original_height INTEGER,
      original_bytes INTEGER NOT NULL,
      original_sha256 TEXT NOT NULL,
      processed_mime_type TEXT,
      processed_width INTEGER,
      processed_height INTEGER,
      processed_bytes INTEGER,
      processed_sha256 TEXT,
      storage_key TEXT,
      preview_mime_type TEXT,
      preview_width INTEGER,
      preview_height INTEGER,
      preview_bytes INTEGER,
      preview_sha256 TEXT,
      preview_storage_key TEXT,
      processing_status TEXT NOT NULL DEFAULT 'pending',
      processing_error_code TEXT,
      observation_status TEXT NOT NULL DEFAULT 'not_requested',
      observation_json TEXT,
      observation_revision INTEGER NOT NULL DEFAULT 0,
      observation_claim_id TEXT,
      observation_claimed_at TEXT,
      quota_bytes INTEGER NOT NULL,
      turn_id TEXT,
      message_id TEXT,
      committed_at TEXT,
      cleanup_status TEXT NOT NULL DEFAULT 'active',
      cleanup_claim_id TEXT,
      cleanup_attempt_count INTEGER NOT NULL DEFAULT 0,
      cleanup_claimed_at TEXT,
      cleanup_retry_at TEXT,
      cleanup_error_code TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      UNIQUE (id, conversation_id),
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp')),
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL),
      CHECK (
        processing_status != 'ready'
        OR (
          processed_mime_type IS NOT NULL
          AND processed_width IS NOT NULL
          AND processed_height IS NOT NULL
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
        )
      )
    )`

// legacyChatConversationsPreMediaDDL 是 M7 之前的 chat_conversations 完整
// 形状（无媒体四列；后续 CREATE INDEX 引用的列全部在场）。
const legacyChatConversationsPreMediaDDL = `CREATE TABLE chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      bind_account_id TEXT,
      bind_account_name_snapshot TEXT,
      archived INTEGER NOT NULL DEFAULT 0,
      search_account_id TEXT,
      search_model_id TEXT,
      image_account_id TEXT,
      title TEXT NOT NULL DEFAULT '新对话',
      title_source_message_id TEXT,
      is_pinned INTEGER NOT NULL DEFAULT 0,
      last_model TEXT,
      default_image_model TEXT NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no INTEGER NOT NULL DEFAULT 1,
      user_turn_count INTEGER NOT NULL DEFAULT 0,
      message_revision INTEGER NOT NULL DEFAULT 0,
      active_turn_id TEXT,
      active_started_at TEXT,
      context_revision INTEGER NOT NULL DEFAULT 0,
      active_checkpoint_id TEXT,
      compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
      context_state TEXT NOT NULL DEFAULT 'ready',
      active_context_tokens INTEGER,
      effective_context_limit_tokens INTEGER,
      context_usage_estimated INTEGER NOT NULL DEFAULT 1,
      context_claim_id TEXT,
      context_claim_revision INTEGER,
      context_claim_through_sequence INTEGER,
      context_claimed_at TEXT,
      context_retry_at TEXT,
      context_attempt_count INTEGER NOT NULL DEFAULT 0,
      context_error_code TEXT,
      context_progress_sequence INTEGER NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at TEXT,
      last_message_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (next_sequence_no >= 1),
      CHECK (user_turn_count >= 0),
      CHECK (message_revision >= 0),
      CHECK (is_pinned IN (0, 1)),
      CHECK (archived IN (0, 1)),
      CHECK (context_state IN ('ready', 'compact_pending', 'compacting', 'compact_failed'))
    )`

// insertMediaTestAsset 直插一行资产（dimension>0 时带宽高；previewKey 非空
// 时带预览键；媒体行两者皆空）。
func insertMediaTestAsset(t *testing.T, db *sql.DB, id, processedMime string, dimension int, previewKey string) {
	t.Helper()
	var dimensionArg any
	if dimension > 0 {
		dimensionArg = dimension
	}
	var previewArgs []any
	if previewKey != "" {
		previewArgs = []any{"image/webp", dimensionArg, dimensionArg, 10, "0000000000000000000000000000000000000000000000000000000000000000", previewKey}
	} else {
		previewArgs = []any{nil, nil, nil, nil, nil, nil}
	}
	_, err := db.Exec(`INSERT INTO chat_assets (id, system_account_id, conversation_id, source_kind, original_filename,
      original_mime_type, original_bytes, original_sha256, processed_mime_type, processed_width, processed_height,
      processed_bytes, processed_sha256, storage_key, preview_mime_type, preview_width, preview_height, preview_bytes,
      preview_sha256, preview_storage_key, processing_status, observation_status,
      quota_bytes, cleanup_status, cleanup_attempt_count, created_at, updated_at, expires_at)
      VALUES (?, 'sys-1', 'conv-1', 'assistant_generated', 'a.bin', 'application/octet-stream', 10,
      '0000000000000000000000000000000000000000000000000000000000000000',
      ?, ?, ?, 10, '0000000000000000000000000000000000000000000000000000000000000000', 'k', ?, ?, ?, ?, ?, ?,
      'ready', 'not_requested', 10,
      'active', 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		append([]any{id, processedMime, dimensionArg, dimensionArg}, previewArgs...)...)
	if err != nil {
		t.Fatalf("insert asset %s: %v", id, err)
	}
}

// TestEnsureSQLiteChatAssetsMediaCheckMigration 覆盖 M7 chat_assets 媒体约束
// 重建：存量行保留、媒体行（无宽高无预览）合法、词表外 MIME 仍拒绝、重复
// ensure 幂等；新库直接声明新约束；媒体工具四列在既有库上补齐。
func TestEnsureSQLiteChatAssetsMediaCheckMigration(t *testing.T) {
	tableDDL := func(t *testing.T, db *sql.DB, table string) string {
		t.Helper()
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatalf("read %s DDL: %v", table, err)
		}
		return ddl
	}

	t.Run("legacy table is rebuilt with the widened media checks", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "schema-test-chat-assets-media-legacy")
		ctx := context.Background()
		if _, err := db.Exec(legacyChatAssetsMediaDDL); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
		insertMediaTestAsset(t, db, "asset-legacy-image", "image/webp", 64, "preview-key")

		if _, err := EnsureSQLiteChat(ctx, db); err != nil {
			t.Fatalf("EnsureSQLiteChat over legacy chat_assets: %v", err)
		}

		ddl := tableDDL(t, db, "chat_assets")
		if !strings.Contains(ddl, "'audio/mpeg'") || !strings.Contains(ddl, "'video/mp4'") {
			t.Fatalf("rebuilt table must carry the media MIME word list:\n%s", ddl)
		}
		var legacyCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM chat_assets WHERE id = 'asset-legacy-image'`).Scan(&legacyCount); err != nil || legacyCount != 1 {
			t.Fatalf("legacy row must survive the rebuild: count=%d err=%v", legacyCount, err)
		}
		// 媒体行：无宽高、无预览，assistant_generated + ready 合法。
		insertMediaTestAsset(t, db, "asset-media-video", "video/mp4", 0, "")
		insertMediaTestAsset(t, db, "asset-media-audio", "audio/mpeg", 0, "")
		// 词表外 MIME 仍被拒绝。
		if _, err := db.Exec(`INSERT INTO chat_assets (id, system_account_id, conversation_id, source_kind, original_filename,
      original_mime_type, original_bytes, original_sha256, processed_mime_type, processing_status, observation_status,
      quota_bytes, cleanup_status, cleanup_attempt_count, created_at, updated_at, expires_at)
      VALUES ('asset-bad-mime', 'sys-1', 'conv-1', 'user_upload', 'a.bin', 'application/octet-stream', 10,
      '0000000000000000000000000000000000000000000000000000000000000000', 'application/octet-stream', 'ready', 'not_requested',
      10, 'active', 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err == nil {
			t.Fatal("词表外 MIME 应仍被拒绝")
		}
		// 重复 ensure 幂等（不重复重建，DDL 稳定）。
		if _, err := EnsureSQLiteChat(ctx, db); err != nil {
			t.Fatalf("second EnsureSQLiteChat: %v", err)
		}
		if ddlAgain := tableDDL(t, db, "chat_assets"); ddlAgain != ddl {
			t.Fatal("重复 ensure 不应再次重建 chat_assets")
		}
	})

	t.Run("fresh database already declares the widened checks", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "schema-test-chat-assets-media-fresh")
		if _, err := EnsureSQLiteChat(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteChat fresh: %v", err)
		}
		ddl := tableDDL(t, db, "chat_assets")
		if !strings.Contains(ddl, "'audio/mpeg'") || !strings.Contains(ddl, "'video/webm'") {
			t.Fatalf("fresh DDL must declare the media word list:\n%s", ddl)
		}
		insertMediaTestAsset(t, db, "asset-media-fresh", "video/mp4", 0, "")
	})

	t.Run("chat media tool columns are delivered to legacy tables", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "schema-test-chat-media-columns-legacy")
		if _, err := db.Exec(legacyChatConversationsPreMediaDDL); err != nil {
			t.Fatalf("seed legacy conversations DDL: %v", err)
		}
		if _, err := db.Exec(`CREATE TABLE chat_user_tool_preferences (
      system_account_id TEXT PRIMARY KEY, search_account_id TEXT, search_model_id TEXT,
      image_account_id TEXT, default_image_model TEXT, updated_at TEXT NOT NULL
    )`); err != nil {
			t.Fatalf("seed legacy preferences DDL: %v", err)
		}
		if _, err := EnsureSQLiteChat(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteChat over legacy columns: %v", err)
		}
		for _, target := range []struct{ table, column string }{
			{"chat_conversations", "video_account_id"},
			{"chat_conversations", "default_video_model"},
			{"chat_conversations", "audio_account_id"},
			{"chat_conversations", "default_audio_model"},
			{"chat_user_tool_preferences", "video_account_id"},
			{"chat_user_tool_preferences", "default_video_model"},
			{"chat_user_tool_preferences", "audio_account_id"},
			{"chat_user_tool_preferences", "default_audio_model"},
		} {
			columns, err := sqliteTableColumnSet(context.Background(), db, target.table)
			if err != nil {
				t.Fatalf("table_info %s: %v", target.table, err)
			}
			if !columns[target.column] {
				t.Fatalf("%s.%s 未由 ensure 补齐", target.table, target.column)
			}
		}
	})
}
