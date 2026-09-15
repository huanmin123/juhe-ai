package main

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

func newW1SChatKeysDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, provider_code TEXT NOT NULL, enabled INTEGER DEFAULT 1, is_default INTEGER DEFAULT 0, created_at TEXT, updated_at TEXT)",
		"CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, description TEXT, mode TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0, config_json TEXT, created_at TEXT, updated_at TEXT)",
		"CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER, weight INTEGER, status TEXT DEFAULT 'active', created_at TEXT, updated_at TEXT)",
		"CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT, name TEXT NOT NULL, description TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT, key_secret_encrypted TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0, purpose TEXT, expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT, availability_schedule_next_check_at TEXT, created_at TEXT, updated_at TEXT, UNIQUE(system_account_id, name))",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_chat_purpose_unique ON api_keys(system_account_id, purpose) WHERE purpose = 'chat'",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	return db
}

func TestW1SChatKeysTableBindDialect(t *testing.T) {
	db := newW1SChatKeysDB(t)
	sp := newChatAPIKeyProvider(db, false, "secret")
	if got := sp.table("api_keys"); got != "api_keys" {
		t.Fatalf("sqlite table = %q", got)
	}
	pg := newChatAPIKeyProvider(db, true, "secret")
	if got := pg.table("route_strategies"); got != "juhe_business.route_strategies" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pg.bind("WHERE x = ? AND y = ?"); got != "WHERE x = $1 AND y = $2" {
		t.Fatalf("pg bind = %q", got)
	}
}

func TestW1SChainNextDefaultNameFromExisting(t *testing.T) {
	if got := chainNextDefaultNameFromExisting(nil, "AI chat"); got != "AI chat" {
		t.Fatalf("nil names = %q", got)
	}
	if got := chainNextDefaultNameFromExisting([]string{"AI chat"}, "AI chat"); got != "AI chat 2" {
		t.Fatalf("exists = %q", got)
	}
}

func TestW1SChainDefaultRouteStrategyNameForGroup(t *testing.T) {
	if got := chainDefaultRouteStrategyNameForGroup(""); got != "默认路由" {
		t.Fatalf("empty = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("测试分组"); got != "测试路由" {
		t.Fatalf("group suffix = %q", got)
	}
}

func TestW1SIsDuplicateAPIKeyNameError(t *testing.T) {
	if isDuplicateAPIKeyNameError(nil) {
		t.Fatal("nil must be false")
	}
	if !isDuplicateAPIKeyNameError(fmt.Errorf("UNIQUE constraint failed: api_keys.system_account_id, api_keys.name")) {
		t.Fatal("unique constraint must be true")
	}
}

func TestW1SIsDuplicateChatAPIKeyError(t *testing.T) {
	if isDuplicateChatAPIKeyError(nil) {
		t.Fatal("nil must be false")
	}
	if !isDuplicateChatAPIKeyError(fmt.Errorf("idx_api_keys_chat_purpose_unique")) {
		t.Fatal("chat unique must be true")
	}
}

func TestW1SIsDuplicateRouteStrategyNameError(t *testing.T) {
	if isDuplicateRouteStrategyNameError(nil) {
		t.Fatal("nil must be false")
	}
	if !isDuplicateRouteStrategyNameError(fmt.Errorf("UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name")) {
		t.Fatal("rs unique must be true")
	}
}

func TestW1SFindChatAPIKeyFound(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	key := apikeys.NewAPIKey()
	sealed, _ := apikeys.EncryptJSON("secret", map[string]string{"key": key})
	db.Exec("INSERT INTO api_keys (id, system_account_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, purpose, created_at, updated_at) VALUES (?,?,?,?,?,?,?, 'active', 'chat', 'now', 'now')",
		"k1", "owner", "chat", apikeys.HashSecret(key), key[:8], key[len(key)-8:], sealed)
	rec, _ := provider.FindChatAPIKey("k1", "owner")
	if rec == nil || rec.ID != "k1" || rec.Secret != key {
		t.Fatalf("record = %v", rec)
	}
}

func TestW1SFindChatAPIKeyNotFound(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	rec, _ := provider.FindChatAPIKey("missing", "owner")
	if rec != nil {
		t.Fatalf("not found = %v", rec)
	}
}

func TestW1SFindChatAPIKeyExpired(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	key := apikeys.NewAPIKey()
	sealed, _ := apikeys.EncryptJSON("secret", map[string]string{"key": key})
	db.Exec("INSERT INTO api_keys (id, system_account_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, purpose, expires_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?, 'active', 'chat', '2025-01-01', 'now', 'now')",
		"k1", "owner", "exp", apikeys.HashSecret(key), key[:8], key[len(key)-8:], sealed)
	rec, _ := provider.FindChatAPIKey("k1", "owner")
	if rec != nil {
		t.Fatalf("expired = %v", rec)
	}
}

func TestW1SFindChatAPIKeyInactive(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	key := apikeys.NewAPIKey()
	sealed, _ := apikeys.EncryptJSON("secret", map[string]string{"key": key})
	db.Exec("INSERT INTO api_keys (id, system_account_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, purpose, created_at, updated_at) VALUES (?,?,?,?,?,?,?, 'inactive', 'chat', 'now', 'now')",
		"k1", "owner", "off", apikeys.HashSecret(key), key[:8], key[len(key)-8:], sealed)
	rec, _ := provider.FindChatAPIKey("k1", "owner")
	if rec != nil {
		t.Fatalf("inactive = %v", rec)
	}
}

func TestW1SChatApiKeyIdForSystemAccount(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	id, _ := provider.chatApiKeyIdForSystemAccount("owner")
	if id != "" {
		t.Fatalf("empty = %q", id)
	}
	key := apikeys.NewAPIKey()
	sealed, _ := apikeys.EncryptJSON("secret", map[string]string{"key": key})
	db.Exec("INSERT INTO api_keys (id, system_account_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, purpose, created_at, updated_at) VALUES (?,?,?,?,?,?,?, 'active', 'chat', 'now', 'now')",
		"k1", "owner", "chat", apikeys.HashSecret(key), key[:8], key[len(key)-8:], sealed)
	id, _ = provider.chatApiKeyIdForSystemAccount("owner")
	if id != "k1" {
		t.Fatalf("id = %q", id)
	}
}

func TestW1SDefaultRouteStrategyGroupsHybridFiltered(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	db.Exec("INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at) VALUES (?,?,?,?,1,1,'now','now')", "g1", "owner", "grp", "openai")
	db.Exec("INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at) VALUES (?,?,?,?,1,1,'now','now')", "g2", "owner", "hgrp", "hybrid")
	groups, err := provider.defaultRouteStrategyGroups("owner")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, g := range groups {
		if g.id == "g2" {
			t.Fatal("hybrid must be filtered")
		}
	}
	if len(groups) != 1 || groups[0].id != "g1" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestW1SDefaultRouteStrategyGroupsNullName(t *testing.T) {
	db := newW1SChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "secret")
	db.Exec("INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at) VALUES (?,?,?,?,1,1,'now','now')", "g1", "owner", nil, "openai")
	groups, _ := provider.defaultRouteStrategyGroups("owner")
	if len(groups) != 1 || groups[0].id != "g1" || !groups[0].nameNull {
		t.Fatalf("groups = %v", groups)
	}
}
