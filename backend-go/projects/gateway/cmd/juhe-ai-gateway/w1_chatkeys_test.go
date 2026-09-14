package main

// w1: chain_chat_keys.go 收割——在最小自建 SQLite schema 上跑聊天 API Key
// 供应器：默认策略路由组列举（hybrid 跳过）、默认名生成去重、Find 的解密
// 与过期门、GPT 策略查询的当前行为（行为存疑：别名错位，见报告）。

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	_ "modernc.org/sqlite"
)

func w1ChatKeysDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/chatkeys.sqlite3")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, provider_code TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL, purpose TEXT, key_secret_encrypted TEXT, expires_at TEXT, route_strategy_id TEXT, description TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT, is_default INTEGER, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func w1SeedDefaultGroup(t *testing.T, db *sql.DB, id, owner, providerCode string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, is_default, enabled, created_at) VALUES (?, ?, ?, ?, 1, 1, '2026-01-01T00:00:00Z')`,
		id, owner, "分组-"+id, providerCode)
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func TestW1ChatAPIKeyDefaultRouteStrategyGroups(t *testing.T) {
	db := w1ChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "chat-keys-secret")
	// 无默认分组：报错（Node ensureDefaultRouteStrategyGroups 为空）。
	if err := provider.ensureDefaultRouteStrategies("sys_1", "2026-01-01T00:00:00Z"); err == nil || !strings.Contains(err.Error(), "默认分组") {
		t.Fatalf("no groups err = %v", err)
	}
	// 默认分组列举：hybrid 跳过、SQL NULL 名称走默认回退。
	w1SeedDefaultGroup(t, db, "grp_openai", "sys_1", "openai")
	w1SeedDefaultGroup(t, db, "grp_hybrid", "sys_1", "hybrid")
	if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, is_default, enabled, created_at) VALUES ('grp_null', 'sys_1', NULL, 'openai', 1, 1, '2026-01-01T01:00:00Z')`); err != nil {
		t.Fatalf("seed null-name group: %v", err)
	}
	// 其他租户的默认分组不得串号。
	w1SeedDefaultGroup(t, db, "grp_other", "sys_2", "openai")
	groups, err := provider.defaultRouteStrategyGroups("sys_1")
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	if groups[0].id != "grp_openai" || groups[0].nameNull {
		t.Fatalf("first = %+v", groups[0])
	}
	if groups[1].id != "grp_null" || !groups[1].nameNull {
		t.Fatalf("second = %+v", groups[1])
	}
	// 组名投影：X分组 → X路由；空 → 默认路由；无后缀原样。
	if got := chainDefaultRouteStrategyNameForGroup("默认分组"); got != "默认路由" {
		t.Fatalf("name = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("   "); got != "默认路由" {
		t.Fatalf("blank name = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("生产分组"); got != "生产路由" {
		t.Fatalf("suffix name = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("主力"); got != "主力" {
		t.Fatalf("plain name = %q", got)
	}
	// 默认名去重：未占用 → 原名；占用 → 序号递增。
	if got := chainNextDefaultNameFromExisting(nil, "AI 对话 API Key"); got != "AI 对话 API Key" {
		t.Fatalf("fresh name = %q", got)
	}
	if got := chainNextDefaultNameFromExisting([]string{"AI 对话 API Key", " AI 对话 API Key 2 ", ""}, "AI 对话 API Key"); got != "AI 对话 API Key 3" {
		t.Fatalf("dedupe name = %q", got)
	}
	// 现有名读取（api_keys 单表查询，不经过有缺陷的别名连接）。
	if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, name, status) VALUES ('key_a', 'sys_1', 'AI 对话 API Key', 'active')`); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	name, err := provider.nextDefaultApiKeyName("sys_1", "AI 对话 API Key")
	if err != nil || name != "AI 对话 API Key 2" {
		t.Fatalf("next name = %q, %v", name, err)
	}
}

func TestW1FindChatAPIKey(t *testing.T) {
	db := w1ChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "chat-keys-secret")
	plainKey := apikeys.NewAPIKey()
	sealed, err := apikeys.EncryptJSON("chat-keys-secret", map[string]string{"key": plainKey})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = db.Exec(`INSERT INTO api_keys (id, system_account_id, name, status, purpose, key_secret_encrypted) VALUES ('key_1', 'sys_1', '对话', 'active', 'chat', ?)`, sealed)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 命中：解密出明文 key 与行身份。
	record, err := provider.FindChatAPIKey("key_1", "sys_1")
	if err != nil || record == nil {
		t.Fatalf("find = %v, %v", record, err)
	}
	if record.ID != "key_1" || record.Name != "对话" || record.Secret != plainKey || record.Status != "active" {
		t.Fatalf("record = %+v", record)
	}
	// 未知 id / 租户错配：nil。
	if found, err := provider.FindChatAPIKey("key_missing", "sys_1"); err != nil || found != nil {
		t.Fatalf("missing = %v, %v", found, err)
	}
	if found, err := provider.FindChatAPIKey("key_1", "sys_9"); err != nil || found != nil {
		t.Fatalf("tenant mismatch = %v, %v", found, err)
	}
	// 非 ACTIVE：nil。
	if _, err := db.Exec(`UPDATE api_keys SET status = 'disabled' WHERE id = 'key_1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if found, err := provider.FindChatAPIKey("key_1", "sys_1"); err != nil || found != nil {
		t.Fatalf("disabled = %v, %v", found, err)
	}
	// 过期门：expires_at 过去 → nil；未来 → 命中。
	if _, err := db.Exec(`UPDATE api_keys SET status = 'active', expires_at = '2000-01-01T00:00:00Z' WHERE id = 'key_1'`); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if found, err := provider.FindChatAPIKey("key_1", "sys_1"); err != nil || found != nil {
		t.Fatalf("expired = %v, %v", found, err)
	}
	if _, err := db.Exec(`UPDATE api_keys SET expires_at = '2030-01-01T00:00:00Z' WHERE id = 'key_1'`); err != nil {
		t.Fatalf("future: %v", err)
	}
	if found, err := provider.FindChatAPIKey("key_1", "sys_1"); err != nil || found == nil {
		t.Fatalf("future = %v, %v", found, err)
	}
	// 密文损坏：解密失败报错；密文 key 为空：报错。
	if _, err := db.Exec(`UPDATE api_keys SET expires_at = NULL, key_secret_encrypted = 'not-json' WHERE id = 'key_1'`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := provider.FindChatAPIKey("key_1", "sys_1"); err == nil || !strings.Contains(err.Error(), "密钥数据无效") {
		t.Fatalf("corrupt err = %v", err)
	}
	emptySealed, err := apikeys.EncryptJSON("chat-keys-secret", map[string]string{"key": ""})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if _, err := db.Exec(`UPDATE api_keys SET key_secret_encrypted = ? WHERE id = 'key_1'`, emptySealed); err != nil {
		t.Fatalf("seed empty: %v", err)
	}
	if _, err := provider.FindChatAPIKey("key_1", "sys_1"); err == nil {
		t.Fatal("空 key 必须报错")
	}
	// chatApiKeyIdForSystemAccount：purpose='chat' 的现存键。
	if _, err := db.Exec(`UPDATE api_keys SET purpose = 'chat', key_secret_encrypted = ? WHERE id = 'key_1'`, sealed); err != nil {
		t.Fatalf("purpose: %v", err)
	}
	keyID, err := provider.chatApiKeyIdForSystemAccount("sys_1")
	if err != nil || keyID != "key_1" {
		t.Fatalf("existing id = %q, %v", keyID, err)
	}
	if keyID, err := provider.chatApiKeyIdForSystemAccount("sys_none"); err != nil || keyID != "" {
		t.Fatalf("none id = %q, %v", keyID, err)
	}
	// 重复键冲突分类器：错误消息特征匹配。
	if isDuplicateChatAPIKeyError(nil) || isDuplicateAPIKeyNameError(nil) || isDuplicateRouteStrategyNameError(nil) {
		t.Fatal("nil 错误不得判冲突")
	}
	if !isDuplicateChatAPIKeyError(errors.New("UNIQUE constraint failed: idx_api_keys_chat_purpose_unique")) {
		t.Fatal("chat 冲突必须命中")
	}
	if !isDuplicateAPIKeyNameError(errors.New("UNIQUE constraint failed: api_keys.system_account_id, api_keys.name")) {
		t.Fatal("name 冲突必须命中")
	}
	if !isDuplicateRouteStrategyNameError(errors.New("IDX idx_route_strategies_owner_name_unique")) {
		t.Fatal("strategy 冲突必须命中")
	}
}

func TestW1ChatAPIKeyGptStrategyCurrentBehavior(t *testing.T) {
	db := w1ChatKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "chat-keys-secret")
	w1SeedDefaultGroup(t, db, "grp_gpt", "sys_1", "gpt")
	if _, err := db.Exec(`INSERT INTO route_strategies (id, system_account_id, name, status, is_default) VALUES ('rs_gpt', 'sys_1', 'GPT 默认', 'active', 1)`); err != nil {
		t.Fatalf("seed strategy: %v", err)
	}
	// 行为存疑：defaultGptRouteStrategyForSystemAccount 的别名连接把
	// route_strategies 自连接为 route_strategy_groups，引用了不存在的
	// route_strategy_id 列——生产 schema 上该查询必然报错（疑似生产
	// 问题 1）。按现状断言：EnsureChatAPIKey 以错误收场，而不是建键。
	if _, err := provider.EnsureChatAPIKey("sys_1"); err == nil {
		t.Fatal("当前实现应因策略查询失败而报错（行为存疑）")
	}
}
