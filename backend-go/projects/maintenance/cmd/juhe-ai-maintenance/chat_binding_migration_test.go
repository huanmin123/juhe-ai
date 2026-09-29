package main

// 本文件覆盖 --migrate-chat-account-only-binding 的进程内结果函数与 main()
// 派发分支（AI 问答会话账户唯一绑定设计 §4/§10）：用法错误出口、互斥分支、
// 以及对临时 SQLite 文件的真实迁移 + 幂等重放。os.Exit 出口已由 runMaintenance
// 收敛为返回码（测试单进程纪律，docs/develop/后端测试分层规则.md）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

func TestChatAccountOnlyBindingMigrationResultUsageErrors(t *testing.T) {
	cases := []struct {
		name     string
		driver   string
		chatPath string
		dsn      string
		wantCode int
	}{
		{name: "缺 driver", driver: "", chatPath: "x.sqlite3", dsn: "", wantCode: 2},
		{name: "非法 driver", driver: "mysql", chatPath: "x.sqlite3", dsn: "", wantCode: 2},
		{name: "sqlite 缺路径", driver: "sqlite", chatPath: "", dsn: "", wantCode: 2},
		{name: "sqlite 误带 dsn", driver: "sqlite", chatPath: "x.sqlite3", dsn: "postgres://u:p@h/db", wantCode: 2},
		{name: "postgres 缺 dsn", driver: "postgres", chatPath: "", dsn: "", wantCode: 2},
		{name: "postgres 误带 chat 路径", driver: "postgres", chatPath: "x.sqlite3", dsn: "postgres://u:p@h/db", wantCode: 2},
		{name: "postgres 非 PG URL", driver: "postgres", chatPath: "", dsn: "sqlite://business.sqlite3", wantCode: 2},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if code := chatAccountOnlyBindingMigrationResult(item.driver, item.chatPath, item.dsn); code != item.wantCode {
				t.Fatalf("exit code = %d, want %d", code, item.wantCode)
			}
		})
	}
}

func TestChatAccountOnlyBindingMigrationMainDispatchMutex(t *testing.T) {
	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-ensure-schema"}); code != 2 {
		t.Fatalf("mutex with ensure-schema exit code = %d, want 2", code)
	}
	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-version"}); code != 2 {
		t.Fatalf("mutex with version exit code = %d, want 2", code)
	}
}

// TestChatAccountOnlyBindingMigrationSQLiteFile 端到端跑临时 SQLite 文件：
// 新形状建库后模拟回滚形状（ALTER 加回三列，即回滚 SQL 产生的形态）→ CLI 迁移
// 0 → 幂等重放 0 → 删除列断言。全列 DDL 的表重建路径由 internal/
// chatbindingmigration 包测试覆盖（含 CHECK 阻断 DROP COLUMN 的原初形态）。
func TestChatAccountOnlyBindingMigrationSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.sqlite3")
	db := openChatBindingTestSQLite(t, path)
	if _, err := schema.EnsureSQLiteChat(context.Background(), db); err != nil {
		t.Fatalf("ensure chat schema: %v", err)
	}
	// 模拟回滚形态：加回三列并写入 group 绑定（NOT NULL DEFAULT 满足 SQLite
	// ADD COLUMN 约束，与回滚 SQL 产出的形状一致）。
	for _, statement := range []string{
		`ALTER TABLE chat_conversations ADD COLUMN bind_mode TEXT NOT NULL DEFAULT 'api_key'`,
		`ALTER TABLE chat_conversations ADD COLUMN bind_group_id TEXT`,
		`ALTER TABLE chat_conversations ADD COLUMN bind_group_name_snapshot TEXT`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("simulate rollback shape: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, bind_mode, bind_group_id, bind_group_name_snapshot, last_message_at, created_at, updated_at)
		VALUES ('conv_legacy', 'owner-1', '密钥', 'group', 'group-a', '分组 A', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	db.Close()

	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-driver", "sqlite", "-chat-sqlite-path", path}); code != 0 {
		t.Fatalf("first migration exit code = %d, want 0", code)
	}
	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-driver", "sqlite", "-chat-sqlite-path", path}); code != 0 {
		t.Fatalf("idempotent rerun exit code = %d, want 0", code)
	}

	db = openChatBindingTestSQLite(t, path)
	defer db.Close()
	for _, column := range []string{"bind_mode", "bind_group_id", "bind_group_name_snapshot"} {
		rows, err := db.Query("PRAGMA table_info(chat_conversations)")
		if err != nil {
			t.Fatalf("pragma: %v", err)
		}
		found := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, declaredType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			if name == column {
				found = true
			}
		}
		rows.Close()
		if found {
			t.Fatalf("chat_conversations still has legacy column %s", column)
		}
	}
	var archived int
	if err := db.QueryRow(`SELECT archived FROM chat_conversations WHERE id = 'conv_legacy'`).Scan(&archived); err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if archived != 1 {
		t.Fatalf("legacy row archived = %d, want 1", archived)
	}
	var backupCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_conversations_bind_legacy_backup`).Scan(&backupCount); err != nil {
		t.Fatalf("read backup count: %v", err)
	}
	if backupCount != 1 {
		t.Fatalf("backup rows = %d, want 1", backupCount)
	}
}

// openChatBindingTestSQLite 打开临时文件库（与迁移命令同一连接契约：单连接 +
// busy_timeout），供测试直接灌数与断言；调用方负责 Close（中途重开同一文件）。
func openChatBindingTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	return db
}
