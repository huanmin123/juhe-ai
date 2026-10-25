package accounts

// BUG-0335 回归：replaceAccountSupportedModels 的写序必须先维护
// account_entry_mappings 的复合外键不变量，再替换 account_supported_models。
// fresh DDL（maintenance 生产形态，原 SQLite 形态的 PG 对应物）声明复合外键
// (account_id, target_model_id) → account_supported_models(account_id, model_id)
// （设计 §5.1，无级联动作）；修复前支持模型集合变化时先 DELETE
// account_supported_models，仍指向被替换模型行的入口映射悬空，DELETE 被
// 23503 拦截（生产编辑账户保存 500）。本文件用 maintenance fresh 生产 DDL
// （bootstrap.EnsurePostgres，PG 外键恒生效——原 SQLite 臂经
// `PRAGMA foreign_keys = ON` 启用）对复合外键真实生效的写序做回归验证。

import (
	"context"
	"database/sql"
	"testing"
)

// bug0335FreshStore 建 pgtestkit 隔离库上的生产 fresh DDL 连接（外键约束
// 恒生效，否则回归退化为纯语句序列断言）。
func bug0335FreshStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := newFreshPGDatabase(t)
	bug0335SeedOwner(t, db, "owner-bug0335")
	bug0335SeedOwner(t, db, "owner-bug0335b")
	store, err := NewStore(db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

// bug0335SeedOwner 落 fresh DDL 属主行（accounts.system_account_id 生产外键，
// 种子必须满足，避免与被测复合外键混淆）。
func bug0335SeedOwner(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, 'hash', $4, $5)`, owner, owner, owner, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("seed owner %s: %v", owner, err)
	}
}

// bug0335SeedAccount 建 vendor + gpt api_key 账户（owner 即 system_account_id）。
func bug0335SeedAccount(t *testing.T, db *sql.DB, accountID, owner, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
		VALUES ($1, 'gpt', 'GPT', $2, $3)`, "ven-"+owner, now, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		created_at, updated_at)
		VALUES ($1, $2, 'gpt', 'gpt', 'openai', 'v1', $3, 'api_key', 'active', 'sealed', 'chat', $4, $5)`,
		accountID, owner, accountID, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

// bug0335SeedCatalogModel 建一个 builtin active 目录模型，返回行 id。
func bug0335SeedCatalogModel(t *testing.T, db *sql.DB, id, upstreamModel, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES ($1, 'gpt', $2, $3, 'builtin', 'active', '["chat_json","chat_sse"]', $4, $5)`,
		id, upstreamModel, upstreamModel, now, now); err != nil {
		t.Fatalf("seed model %s: %v", id, err)
	}
}

// bug0335SeedEntryMapping 直插一条入口映射行（target 指向给定支持模型行）。
// 前置：账户已有 account_connections 行 'conn-' + accountID。
func bug0335SeedEntryMapping(t *testing.T, db *sql.DB, accountID, mappingID, targetModelID, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_entry_mappings (id, account_id, source_model,
		source_endpoint_family, target_connection_id, target_model_id, target_endpoint_family,
		enabled, created_at, updated_at)
		VALUES ($1, $2, $3, 'chat_completions', $4, $5, 'chat_completions', 1, $6, $7)`,
		mappingID, accountID, "src-"+mappingID, "conn-"+accountID, targetModelID, now, now); err != nil {
		t.Fatalf("seed entry mapping %s: %v", mappingID, err)
	}
}

// TestBug0335ReplaceSupportedModelsCleansDanglingEntryMappings 是生产 500 的
// 复现场景：支持模型集合 {m1, m2} + 指向 m1 与 m2 的入口映射各一条，替换为
// {m2, m3}（m3 为目录内有效模型）。修复前调用在支持模型 DELETE 处因复合外键
// 失败；修复后成功，且指向被替换行（m1）的映射被清理、指向幸存行（m2）的
// 映射保留。
func TestBug0335ReplaceSupportedModelsCleansDanglingEntryMappings(t *testing.T) {
	store, db := bug0335FreshStore(t)
	ctx := context.Background()
	now := "2026-10-23T00:00:00.000Z"

	bug0335SeedAccount(t, db, "acc-bug0335", "owner-bug0335", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335-m1", "gpt-33-m1", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335-m2", "gpt-33-m2", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335-m3", "gpt-33-m3", now)
	for _, modelID := range []string{"mod-bug0335-m1", "mod-bug0335-m2"} {
		if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, model_id, created_at)
			VALUES ('acc-bug0335', $1, $2)`, modelID, now); err != nil {
			t.Fatalf("seed supported model: %v", err)
		}
	}
	// 入口映射的复合外键 (account_id, target_connection_id) 要求连接行同账户。
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code,
		protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn-acc-bug0335', 'acc-bug0335', 'gpt_openai_v1', 'openai_v1',
		'https://api.openai.com/v1', 1, $1, $2)`, now, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	bug0335SeedEntryMapping(t, db, "acc-bug0335", "em-bug0335-m1", "mod-bug0335-m1", now)
	bug0335SeedEntryMapping(t, db, "acc-bug0335", "em-bug0335-m2", "mod-bug0335-m2", now)

	// 回归前提自证：复合外键在测试库真实生效——修复前的旧写序第一步（未处理
	// 入口映射先删支持模型集合）必须被外键拒绝（SQLite
	// "FOREIGN KEY constraint failed"，PG 即 23503/SQLSTATE 约束违反）。
	if _, err := db.Exec(`DELETE FROM account_supported_models WHERE account_id = 'acc-bug0335'`); err == nil {
		t.Fatal("悬空入口映射下的支持模型删除未被外键拦截：测试库外键未生效，回归前提不成立")
	}

	// 修复对象调用：同事务内先解析 next → 清理悬空映射 → 替换支持模型集合。
	if err := store.replaceAccountSupportedModels(ctx, db, "acc-bug0335", "gpt",
		[]string{"gpt-33-m2", "gpt-33-m3"}, now); err != nil {
		t.Fatalf("replaceAccountSupportedModels 应先清悬空映射再替换支持模型：%v", err)
	}

	var supported []string
	rows, err := db.Query(`SELECT models.upstream_model FROM account_supported_models
		INNER JOIN models ON models.id = account_supported_models.model_id
		WHERE account_supported_models.account_id = 'acc-bug0335' ORDER BY models.upstream_model`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		supported = append(supported, model)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(supported) != 2 || supported[0] != "gpt-33-m2" || supported[1] != "gpt-33-m3" {
		t.Fatalf("支持模型集合应为 [gpt-33-m2 gpt-33-m3]：%v", supported)
	}
	var m1Mappings, m2Mappings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = 'acc-bug0335'
		AND target_model_id = 'mod-bug0335-m1'`).Scan(&m1Mappings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = 'acc-bug0335'
		AND target_model_id = 'mod-bug0335-m2'`).Scan(&m2Mappings); err != nil {
		t.Fatal(err)
	}
	if m1Mappings != 0 || m2Mappings != 1 {
		t.Fatalf("指向被替换行 m1 的映射应清理、指向幸存行 m2 的映射应保留：m1=%d m2=%d", m1Mappings, m2Mappings)
	}
}

// TestBug0335ReplaceSupportedModelsEmptySetClearsAllEntryMappings 是子场景 B：
// next 支持模型集合为空（如前端提交空数组）时，清理 DELETE 退化为删除该账户
// 全部入口映射，随后支持模型集合清空，全程不触外键失败。
func TestBug0335ReplaceSupportedModelsEmptySetClearsAllEntryMappings(t *testing.T) {
	store, db := bug0335FreshStore(t)
	ctx := context.Background()
	now := "2026-10-23T00:00:00.000Z"

	bug0335SeedAccount(t, db, "acc-bug0335b", "owner-bug0335b", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335b-m1", "gpt-33-m1", now)
	if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES ('acc-bug0335b', 'mod-bug0335b-m1', $1)`, now); err != nil {
		t.Fatalf("seed supported model: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code,
		protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn-acc-bug0335b', 'acc-bug0335b', 'gpt_openai_v1', 'openai_v1',
		'https://api.openai.com/v1', 1, $1, $2)`, now, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	bug0335SeedEntryMapping(t, db, "acc-bug0335b", "em-bug0335b-m1", "mod-bug0335b-m1", now)

	if err := store.replaceAccountSupportedModels(ctx, db, "acc-bug0335b", "gpt", nil, now); err != nil {
		t.Fatalf("空集合替换应清理全部映射后成功：%v", err)
	}
	var supported, mappings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_supported_models WHERE account_id = 'acc-bug0335b'`).Scan(&supported); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = 'acc-bug0335b'`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if supported != 0 || mappings != 0 {
		t.Fatalf("空集合应清空支持模型与入口映射：supported=%d mappings=%d", supported, mappings)
	}
}
