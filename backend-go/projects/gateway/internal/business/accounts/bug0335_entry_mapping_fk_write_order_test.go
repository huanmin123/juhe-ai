package accounts

// BUG-0335 同型回归：writeRelationsPatch 的 SupportedModels 分支写序必须先
// 维护 account_entry_mappings 的复合外键不变量，再替换
// account_supported_models。fresh DDL（maintenance SQLite 形态与 PG 同构）
// 声明复合外键 (account_id, target_model_id) →
// account_supported_models(account_id, model_id)（设计 §5.1，无级联动作）；
// 修复前该分支先全删支持模型行，仍指向被替换模型行的存量入口映射即刻悬空，
// DELETE 被 23503 拦截（与生产写面 BUG-0335 完全同型，见
// docs/bug/问题-0335-入口映射外键写序致编辑账户保存500.md）。本文件用
// maintenance fresh business DDL + `PRAGMA foreign_keys = ON`（internal/accounts
// bug0335 回归同款建库方式）在 SQLite 上对复合外键真实生效的写序做回归验证。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"

	_ "modernc.org/sqlite"
)

// bug0335FreshStore 建单连接内存 SQLite：fresh business DDL + 外键 PRAGMA
// （外键约束必须真实启用，否则回归退化为纯语句序列断言）。
func bug0335FreshStore(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:bug0335-relpatch-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("fresh business DDL ensure: %v", err)
	}
	svc, err := New(db, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return svc, db
}

// bug0335SeedAccount 建 vendor + gpt api_key 账户行（owner 即 system_account_id，
// 本包 resolveSupportedModelID 的 owner scope 取该列）。
func bug0335SeedAccount(t *testing.T, db *sql.DB, accountID, owner, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
		VALUES (?, 'gpt', 'GPT', ?, ?)`, "ven-"+owner, now, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		created_at, updated_at)
		VALUES (?, ?, 'gpt', 'gpt', 'openai', 'v1', ?, 'api_key', 'active', 'sealed', 'chat', ?, ?)`,
		accountID, owner, accountID, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

// bug0335SeedCatalogModel 建一个 builtin active 目录模型（modes 与 §5.3 文本
// 联动校验的夹具交集对齐）。
func bug0335SeedCatalogModel(t *testing.T, db *sql.DB, id, upstreamModel, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES (?, 'gpt', ?, ?, 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`,
		id, upstreamModel, upstreamModel, now, now); err != nil {
		t.Fatalf("seed model %s: %v", id, err)
	}
}

// bug0335SeedSupportedModels 直插账户支持模型行。
func bug0335SeedSupportedModels(t *testing.T, db *sql.DB, accountID string, modelIDs []string, now string) {
	t.Helper()
	for _, modelID := range modelIDs {
		if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, model_id, created_at)
			VALUES (?, ?, ?)`, accountID, modelID, now); err != nil {
			t.Fatalf("seed supported model %s: %v", modelID, err)
		}
	}
}

// bug0335SeedConnection 预置账户 enabled 连接与 endpoint modes（入口映射复合
// 外键 (account_id, target_connection_id) 要求连接行同账户；writeEntryMapping
// 的 §5.3 联动校验需要连接 modes）。
func bug0335SeedConnection(t *testing.T, db *sql.DB, accountID, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code,
		protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, 'gpt_openai_v1', 'openai_v1', 'https://api.openai.com/v1', 1, ?, ?)`,
		"conn-"+accountID, accountID, now, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	for _, mode := range []string{"chat_json", "chat_sse"} {
		if _, err := db.Exec(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode)
			VALUES (?, ?)`, "conn-"+accountID, mode); err != nil {
			t.Fatalf("seed endpoint mode %s: %v", mode, err)
		}
	}
}

// bug0335SeedEntryMapping 直插一条入口映射行（target 指向给定支持模型行）。
func bug0335SeedEntryMapping(t *testing.T, db *sql.DB, accountID, mappingID, targetModelID, now string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_entry_mappings (id, account_id, source_model,
		source_endpoint_family, target_connection_id, target_model_id, target_endpoint_family,
		enabled, created_at, updated_at)
		VALUES (?, ?, ?, 'chat_completions', ?, ?, 'chat_completions', 1, ?, ?)`,
		mappingID, accountID, "src-"+mappingID, "conn-"+accountID, targetModelID, now, now); err != nil {
		t.Fatalf("seed entry mapping %s: %v", mappingID, err)
	}
}

// bug0335CountMappingsByTarget 统计账户内指向某支持模型行的入口映射数。
func bug0335CountMappingsByTarget(t *testing.T, db *sql.DB, accountID, targetModelID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_entry_mappings
		WHERE account_id = ? AND target_model_id = ?`, accountID, targetModelID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestBug0335RelationsPatchBothSetsCleansDanglingEntryMappings 是生产 500 的
// 同型复现场景（同 PATCH 同时改两个集合）：支持模型集合 {m1, m2} + 指向 m1
// 与 m2 的入口映射各一条，PATCH 同时把支持模型替换为 {m2, m3}、入口映射整
// 集合替换为一条指向 m3 的映射。修复前支持模型分支第一步全删 DELETE 即被复合
// 外键拦截（BUG-0335 同型 23503）；修复后成功，悬空映射被对称清理，两集合整
// 替换收敛为新集合。
func TestBug0335RelationsPatchBothSetsCleansDanglingEntryMappings(t *testing.T) {
	svc, db := bug0335FreshStore(t)
	ctx := context.Background()
	now := "2026-10-23T00:00:00.000Z"
	const accountID = "acc-bug0335a"
	const owner = "owner-bug0335a"

	bug0335SeedAccount(t, db, accountID, owner, now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335a-m1", "gpt-33-m1", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335a-m2", "gpt-33-m2", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335a-m3", "gpt-33-m3", now)
	bug0335SeedSupportedModels(t, db, accountID, []string{"mod-bug0335a-m1", "mod-bug0335a-m2"}, now)
	bug0335SeedConnection(t, db, accountID, now)
	bug0335SeedEntryMapping(t, db, accountID, "em-bug0335a-m1", "mod-bug0335a-m1", now)
	bug0335SeedEntryMapping(t, db, accountID, "em-bug0335a-m2", "mod-bug0335a-m2", now)

	// 回归前提自证：复合外键在测试库真实生效——修复前的旧写序第一步（未处理
	// 入口映射先全删支持模型集合）必须被外键拒绝（SQLite
	// "FOREIGN KEY constraint failed"，PG 即 23503/SQLSTATE 约束违反）。
	if _, err := db.Exec(`DELETE FROM account_supported_models WHERE account_id = ?`, accountID); err == nil {
		t.Fatal("悬空入口映射下的支持模型全删未被外键拦截：测试库 PRAGMA foreign_keys 未生效，回归前提不成立")
	}

	var revision int64
	if err := db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, accountID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	models := []string{"gpt-33-m2", "gpt-33-m3"}
	mappings := []ModelMapping{{SourceModel: "src-m3", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-33-m3", UpstreamEndpointFamily: "chat_completions", TargetConnectionPresetCode: "gpt_openai_v1", Enabled: true}}
	patched, err := svc.Patch(ctx, owner, accountID, Patch{
		ExpectedConfigRevision: revision,
		SupportedModels:        &models,
		ModelMappings:          &mappings,
	})
	if err != nil {
		t.Fatalf("同 PATCH 替换两集合应先清悬空映射再收敛：%v", err)
	}
	if strings.Join(patched.SupportedModels, ",") != "gpt-33-m2,gpt-33-m3" {
		t.Fatalf("支持模型集合应为 [gpt-33-m2 gpt-33-m3]：%v", patched.SupportedModels)
	}
	if m1 := bug0335CountMappingsByTarget(t, db, accountID, "mod-bug0335a-m1"); m1 != 0 {
		t.Fatalf("指向被替换行 m1 的旧映射应被清理：m1=%d", m1)
	}
	if len(patched.ModelMappings) != 1 || patched.ModelMappings[0].UpstreamModel != "gpt-33-m3" || patched.ModelMappings[0].TargetConnectionPresetCode != "gpt_openai_v1" {
		t.Fatalf("入口映射应整集合收敛为指向 m3 的新映射：%+v", patched.ModelMappings)
	}
}

// TestBug0335RelationsPatchModelsOnlyKeepsSurvivorMappings 是子场景 B：PATCH
// 只改支持模型、未带 modelMappings。对称清理只移除指向被替换行（m1）的悬空
// 映射；指向幸存行（m2）的映射全程不被删除重写，原样保留（含 created_at）。
func TestBug0335RelationsPatchModelsOnlyKeepsSurvivorMappings(t *testing.T) {
	svc, db := bug0335FreshStore(t)
	ctx := context.Background()
	now := "2026-10-23T00:00:00.000Z"
	const accountID = "acc-bug0335b"
	const owner = "owner-bug0335b"

	bug0335SeedAccount(t, db, accountID, owner, now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335b-m1", "gpt-33-m1", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335b-m2", "gpt-33-m2", now)
	bug0335SeedCatalogModel(t, db, "mod-bug0335b-m3", "gpt-33-m3", now)
	bug0335SeedSupportedModels(t, db, accountID, []string{"mod-bug0335b-m1", "mod-bug0335b-m2"}, now)
	bug0335SeedConnection(t, db, accountID, now)
	bug0335SeedEntryMapping(t, db, accountID, "em-bug0335b-m1", "mod-bug0335b-m1", now)
	bug0335SeedEntryMapping(t, db, accountID, "em-bug0335b-m2", "mod-bug0335b-m2", now)
	var survivorCreatedAt string
	if err := db.QueryRow(`SELECT created_at FROM account_entry_mappings WHERE id = 'em-bug0335b-m2'`).Scan(&survivorCreatedAt); err != nil {
		t.Fatal(err)
	}

	var revision int64
	if err := db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, accountID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	models := []string{"gpt-33-m2", "gpt-33-m3"}
	patched, err := svc.Patch(ctx, owner, accountID, Patch{
		ExpectedConfigRevision: revision,
		SupportedModels:        &models,
	})
	if err != nil {
		t.Fatalf("只改支持模型的 PATCH 应按目标集合对称清理后成功：%v", err)
	}
	if strings.Join(patched.SupportedModels, ",") != "gpt-33-m2,gpt-33-m3" {
		t.Fatalf("支持模型集合应为 [gpt-33-m2 gpt-33-m3]：%v", patched.SupportedModels)
	}
	if m1, m2 := bug0335CountMappingsByTarget(t, db, accountID, "mod-bug0335b-m1"), bug0335CountMappingsByTarget(t, db, accountID, "mod-bug0335b-m2"); m1 != 0 || m2 != 1 {
		t.Fatalf("指向被替换行 m1 的映射应被对称清理、指向幸存行 m2 的映射应保留：m1=%d m2=%d", m1, m2)
	}
	var survivorAfter string
	if err := db.QueryRow(`SELECT created_at FROM account_entry_mappings WHERE id = 'em-bug0335b-m2'`).Scan(&survivorAfter); err != nil {
		t.Fatal(err)
	}
	if survivorAfter != survivorCreatedAt {
		t.Fatalf("幸存映射行应原样保留（不被删除重写）：created_at %s -> %s", survivorCreatedAt, survivorAfter)
	}
}
