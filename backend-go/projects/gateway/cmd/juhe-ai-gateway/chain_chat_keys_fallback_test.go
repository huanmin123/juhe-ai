package main

// Chat API key default route strategy description fallback (chain_chat_keys.go
// C3): the default route strategy description mirrors the Node
// `group.name ?? '默认分组'` (route-strategy.repository.ts) — only a SQL NULL
// group name falls back, an empty string stays verbatim.

import (
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"database/sql"
)

func newChatKeysFallbackDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chat-keys.sqlite3"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE groups (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT,
			provider_code TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
			is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategies (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, description TEXT,
			mode TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0,
			config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategy_groups (
			id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
			group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER,
			status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	return db
}

func seedChatKeysFallbackGroup(t *testing.T, db *sql.DB, id string, name any) {
	t.Helper()
	if name == nil {
		if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
			VALUES (?, 'sys_owner', NULL, 'openai', 1, 1, '2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00.000Z')`, id); err != nil {
			t.Fatalf("seed NULL-name group: %v", err)
		}
		return
	}
	if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES (?, 'sys_owner', ?, 'openai', 1, 1, '2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00.000Z')`, id, name); err != nil {
		t.Fatalf("seed group %v: %v", name, err)
	}
}

func TestChatAPIKeyProviderDefaultStrategyDescriptionGroupNameFallback(t *testing.T) {
	cases := []struct {
		name         string
		groupName    any
		wantContains string
	}{
		{"null group name", nil, "系统默认普通路由，绑定默认分组。"},
		{"named group", "测试分组", "系统默认普通路由，绑定测试分组。"},
		{"empty group name stays verbatim", "", "系统默认普通路由，绑定。"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := newChatKeysFallbackDB(t)
			seedChatKeysFallbackGroup(t, db, "group_default", testCase.groupName)
			provider := newChatAPIKeyProvider(db, false, "chat-keys-test-secret")
			if err := provider.ensureDefaultRouteStrategies("sys_owner", "2026-09-04T00:00:00.000Z"); err != nil {
				t.Fatalf("ensure default route strategies: %v", err)
			}
			var description sql.NullString
			if err := db.QueryRow(`SELECT description FROM route_strategies WHERE system_account_id = 'sys_owner' LIMIT 1`).Scan(&description); err != nil {
				t.Fatalf("read strategy description: %v", err)
			}
			if !strings.Contains(description.String, testCase.wantContains) {
				t.Fatalf("description=%q want %q", description.String, testCase.wantContains)
			}
		})
	}
}
