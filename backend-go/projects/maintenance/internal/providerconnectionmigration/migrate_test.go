package providerconnectionmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite 内存/临时文件旧 schema fixture：迁移器自建旧表 DDL 造数（不依赖
// maintenance ensure），跑通 dry-run 全流程并断言 manifest 内容、计数、脱敏
// 与零写入。

const fixtureOldSchemaDDL = `
CREATE TABLE providers (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '', default_health_check_model TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE provider_protocol_profile_families (profile_id TEXT NOT NULL, family_code TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (profile_id, family_code));
CREATE TABLE accounts (id TEXT PRIMARY KEY, config_revision INTEGER NOT NULL DEFAULT 1, system_account_id TEXT NOT NULL, provider_code TEXT, provider_protocol_profile_id TEXT, name TEXT NOT NULL DEFAULT '', type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', credentials_encrypted TEXT NOT NULL DEFAULT '', credential_fingerprint TEXT, capability_domain TEXT NOT NULL DEFAULT 'chat', health_check_model TEXT, health_check_endpoint_mode TEXT, authorization_instance_source_account_id TEXT, authorization_instance_authorization_id TEXT, authorization_instance_owner_system_account_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL DEFAULT 'gpt', description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_accounts (group_id TEXT NOT NULL, account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (group_id, account_id));
CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE account_supported_models (account_id TEXT NOT NULL, provider_code TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (account_id, model));
CREATE TABLE account_model_mappings (account_id TEXT NOT NULL, provider_code TEXT NOT NULL, source_model TEXT NOT NULL, source_endpoint_family TEXT NOT NULL, upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (account_id, source_model, source_endpoint_family));
CREATE TABLE provider_model_catalog (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', mode TEXT, supported_api_protocols_json TEXT NOT NULL DEFAULT '[]', service_tier_prices_json TEXT NOT NULL DEFAULT '{}', input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL, long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL, image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL, output_usd_per_image REAL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE custom_provider_models (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'personal', system_account_id TEXT, status TEXT NOT NULL DEFAULT 'active', supported_api_protocols_json TEXT NOT NULL DEFAULT '[]', service_tier_prices_json TEXT NOT NULL DEFAULT '{}', currency TEXT NOT NULL DEFAULT 'USD', input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL, long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL, image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL, output_usd_per_image REAL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE usage_records (id TEXT PRIMARY KEY, model TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE audit_logs (id TEXT PRIMARY KEY, action TEXT NOT NULL, created_at TEXT NOT NULL);
`

func openFixtureDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开 fixture SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(fixtureOldSchemaDDL); err != nil {
		t.Fatalf("建旧 schema fixture 失败: %v", err)
	}
	return db, path
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture 写入失败 %s: %v", query, err)
	}
}

func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, def := range configSourceTables {
		var count int
		if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, def.name)).Scan(&count); err != nil {
			t.Fatalf("统计 %s 失败: %v", def.name, err)
		}
		counts[def.name] = count
	}
	return counts
}

// seedFixtureDB 写入覆盖全部规则的存量数据（预期计数见断言）。
func seedFixtureDB(t *testing.T, db *sql.DB) {
	t.Helper()
	now := "2026-10-01T00:00:00Z"
	for _, code := range []string{"gpt", "xai", "deepseek", "anthropic", "gemini", "glm", "minimax", "volcengine", "qwen", "openai", "hybrid"} {
		mustExec(t, db, `INSERT INTO providers (id, code, name, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, "prov-"+code, code, code, now, now)
	}
	for _, profile := range []struct{ id, provider, baseURL string }{
		{"profile_gpt_openai_v1", "gpt", "https://api.openai.com/v1"},
		{"profile_openai_openai_v1", "openai", "https://api.openai.com/v1"},
		{"profile_hybrid_openai_chat_v1", "hybrid", ""},
		{"profile_deepseek_openai_v1", "deepseek", "https://api.deepseek.com"},
		{"profile_gemini_native_v1beta", "gemini", "https://generativelanguage.googleapis.com"},
	} {
		mustExec(t, db, `INSERT INTO provider_protocol_profiles (id, provider_code, protocol_code, protocol_version, base_url, default_health_check_model, enabled, created_at, updated_at) VALUES (?, ?, 'openai', 'v1', ?, 'm1', 1, ?, ?)`,
			profile.id, profile.provider, profile.baseURL, now, now)
	}
	mustExec(t, db, `INSERT INTO provider_protocol_profile_families (profile_id, family_code, enabled, created_at, updated_at) VALUES ('profile_gpt_openai_v1', 'chat_completions', 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO provider_protocol_profile_families (profile_id, family_code, enabled, created_at, updated_at) VALUES ('profile_gpt_openai_v1', 'responses', 1, ?, ?)`, now, now)

	// 账户（9 行；预期 accounts_blocked = 5：未知/空白/NULL provider_code、
	// google_oauth 不可解析、非法 type）。
	insertAccount := func(id, provider, profile, accountType, domain string, revision int64, healthModel, healthMode string, extraAuth ...string) {
		authSource, authID, authOwner := "", "", ""
		if len(extraAuth) == 3 {
			authSource, authID, authOwner = extraAuth[0], extraAuth[1], extraAuth[2]
		}
		mustExec(t, db, `INSERT INTO accounts (id, config_revision, system_account_id, provider_code, provider_protocol_profile_id, name, type, status, credentials_encrypted, capability_domain, health_check_model, health_check_endpoint_mode, authorization_instance_source_account_id, authorization_instance_authorization_id, authorization_instance_owner_system_account_id, created_at, updated_at)
			VALUES (?, ?, 'sys_admin', ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, revision, provider, profile, id, accountType, "v1:SECRET-IV:SECRET-TAG:SECRET-CIPHER-"+id, domain, healthModel, healthMode, authSource, authID, authOwner, now, now)
	}
	insertAccount("acc-gpt", "gpt", "profile_gpt_openai_v1", "api_key", "chat", 3, "m1", "chat_json")
	insertAccount("acc-openai", "openai", "profile_openai_openai_v1", "api_key", "chat", 1, "shared", "chat_json")
	insertAccount("acc-hybrid", "hybrid", "profile_hybrid_openai_chat_v1", "api_key", "chat", 1, "", "")
	insertAccount("acc-badprov", "unknown_ai", "profile_gpt_openai_v1", "api_key", "chat", 1, "m1", "chat_json")
	insertAccount("acc-blank", " gpt ", "profile_gpt_openai_v1", "api_key", "chat", 1, "m1", "chat_json")
	mustExec(t, db, `INSERT INTO accounts (id, config_revision, system_account_id, provider_code, provider_protocol_profile_id, name, type, status, credentials_encrypted, capability_domain, created_at, updated_at) VALUES ('acc-nullprov', 1, 'sys_admin', NULL, NULL, 'n', 'api_key', 'active', 'v1:x', 'chat', ?, ?)`, now, now)
	insertAccount("acc-gemini-oauth", "gemini", "profile_gemini_native_v1beta", "google_oauth", "chat", 1, "", "")
	insertAccount("acc-badtype", "gpt", "profile_gpt_openai_v1", "session", "chat", 1, "m1", "chat_json")
	insertAccount("acc-authinst", "gpt", "profile_gpt_openai_v1", "api_key", "chat", 1, "", "", "acc-gpt", "auth-1", "sys_admin")

	// 模型目录（catalog 6 行 + custom 2 行；预期 models_blocked = 4）。
	insertCatalog := func(id, provider, model, protocols string, inputPrice any) {
		mustExec(t, db, `INSERT INTO provider_model_catalog (id, provider_code, model, status, mode, supported_api_protocols_json, service_tier_prices_json, input_usd_per_1m, created_at, updated_at) VALUES (?, ?, ?, 'active', 'chat', ?, '{}', ?, ?, ?)`,
			id, provider, model, protocols, inputPrice, now, now)
	}
	insertCatalog("cat-1", "gpt", "m1", `["chat_completions","responses"]`, 1.5)
	insertCatalog("cat-ds", "deepseek", "deepseek-chat", `["chat_completions"]`, 0.5)
	insertCatalog("cat-openai", "openai", "shared", `["chat_completions"]`, 2.0)
	insertCatalog("cat-unpriced", "gpt", "free-chat", `["chat_completions"]`, nil)
	insertCatalog("cat-weird", "gpt", "legacy", `["completions"]`, 1.0)
	insertCatalog("cat-dup", "qwen", "dup-model", `["video"]`, nil)
	mustExec(t, db, `INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, supported_api_protocols_json, service_tier_prices_json, currency, input_usd_per_1m, created_at, updated_at) VALUES ('cus-1', 'glm', 'mine', 'personal', 'sys_admin', 'active', '["chat_completions"]', '{}', 'CNY', 0.5, ?, ?)`, now, now)
	// 同一 (vendor, scope, owner, upstream_model) 身份在 custom 表内出现两行
	// → 规则 10 同身份冲突，两行都阻塞（catalog 恒 builtin、custom 至多
	// global/personal，跨表身份不会重复）。
	mustExec(t, db, `INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, supported_api_protocols_json, service_tier_prices_json, currency, created_at, updated_at) VALUES ('cus-dup', 'qwen', 'dup-model', 'personal', 'sys_admin', 'active', '["video"]', '{}', 'USD', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, supported_api_protocols_json, service_tier_prices_json, currency, created_at, updated_at) VALUES ('cus-dup2', 'qwen', 'dup-model', 'personal', 'sys_admin', 'active', '["video"]', '{}', 'USD', ?, ?)`, now, now)

	// 支持模型（5 行；预期 account_supported_models_blocked = 1 的 ghost）。
	for _, row := range [][3]string{
		{"acc-gpt", "gpt", "m1"},
		{"acc-openai", "openai", "shared"},
		{"acc-hybrid", "deepseek", "deepseek-chat"},
		{"acc-gpt", "glm", "ghost"},
		{"acc-badprov", "unknown_ai", "m"},
	} {
		mustExec(t, db, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, ?, ?, ?)`, row[0], row[1], row[2], now)
	}

	// 映射（3 行；预期 mappings_blocked = 1 的 ghost upstream）。
	mustExec(t, db, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('acc-openai', 'openai', 'shared', 'chat_completions', 'shared', 'chat_completions', 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('acc-hybrid', 'deepseek', 'client-a', 'chat_completions', 'deepseek-chat', 'chat_completions', 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('acc-hybrid', 'deepseek', 'client-b', 'chat_completions', 'ghost-model', 'chat_completions', 1, ?, ?)`, now, now)

	// 分组（5 行；预期 groups_blocked = 4：同名 2 + 多默认 2）。
	insertGroup := func(id, owner, name string, isDefault int) {
		mustExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at) VALUES (?, ?, ?, 'gpt', 1, ?, 'personal', ?, ?)`, id, owner, name, isDefault, now, now)
	}
	insertGroup("grp-clean", "sys_admin", "清洁组", 0)
	insertGroup("grp-dup-a", "sys_admin", "默认", 0)
	insertGroup("grp-dup-b", "sys_admin", "默认", 0)
	insertGroup("grp-def-a", "owner2", "A", 1)
	insertGroup("grp-def-b", "owner2", "B", 1)
	mustExec(t, db, `INSERT INTO group_accounts (group_id, account_id, system_account_id, enabled, created_at, updated_at) VALUES ('grp-clean', 'acc-gpt', 'sys_admin', 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO group_accounts (group_id, account_id, system_account_id, enabled, created_at, updated_at) VALUES ('grp-dup-a', 'acc-gpt', 'sys_admin', 1, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at) VALUES ('rsg-1', 'rs-1', 'sys_admin', 'grp-clean', 'active', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at) VALUES ('rsg-2', 'rs-1', 'sys_admin', 'grp-dup-a', 'active', ?, ?)`, now, now)

	// 历史行（usage 3 + audit 2；model_check_runs 表故意不建 → 缺表 note）。
	for index := 0; index < 3; index++ {
		mustExec(t, db, `INSERT INTO usage_records (id, model, created_at) VALUES (?, 'm1', ?)`, fmt.Sprintf("u-%d", index), now)
	}
	for index := 0; index < 2; index++ {
		mustExec(t, db, `INSERT INTO audit_logs (id, action, created_at) VALUES (?, 'request', ?)`, fmt.Sprintf("a-%d", index), now)
	}
}

func TestDryRunSQLiteFixtureFullFlow(t *testing.T) {
	db, path := openFixtureDB(t)
	seedFixtureDB(t, db)
	beforeCounts := tableCounts(t, db)

	fixedNow := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	manifest, err := DryRun(context.Background(), db, Options{Dialect: DialectSQLite, Now: fixedNow})
	if err != nil {
		t.Fatalf("dry-run 失败: %v", err)
	}

	if manifest.FormatVersion != FormatVersion || manifest.CreatedAtUTC != fixedNow.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("manifest 头不符: %+v", manifest)
	}
	if manifest.SourceSchemaVersion != SourceSchemaVersion || manifest.TargetSchemaVersion != TargetSchemaVersion {
		t.Fatalf("schema 版本不符: %+v", manifest)
	}
	if len(manifest.SourceSnapshotHash) != 64 {
		t.Fatalf("source_snapshot_hash 必须是 64 位十六进制: %q", manifest.SourceSnapshotHash)
	}
	// row counts 与 fixture 一致（含历史表清点）。
	wantCounts := map[string]int{
		"providers": 11, "provider_protocol_profiles": 5, "provider_protocol_profile_families": 2,
		"accounts": 9, "groups": 5, "group_accounts": 2, "route_strategy_groups": 2,
		"account_supported_models": 5, "account_model_mappings": 3,
		"provider_model_catalog": 6, "custom_provider_models": 3,
		"usage_records": 3, "audit_logs": 2,
	}
	for name, want := range wantCounts {
		if got := manifest.SourceTableRowCounts[name]; got != want {
			t.Fatalf("source_table_row_counts[%s] = %d, want %d", name, got, want)
		}
	}
	if manifest.SourceTableRowCounts["model_check_runs"] != 0 {
		t.Fatalf("缺失历史表按 0 行清点: %v", manifest.SourceTableRowCounts)
	}

	// blocked 计数。
	wantBlocked := BlockedCounts{
		AccountsBlocked:        5,
		ConnectionsBlocked:     0,
		ModelsBlocked:          4,
		SupportedModelsBlocked: 1,
		MappingsBlocked:        1,
		GroupsBlocked:          4,
		HealthChecksBlocked:    1,
		HistoryRowsUnmatched:   5,
		PublicModelConflicts:   1,
	}
	if manifest.BlockedCounts != wantBlocked {
		t.Fatalf("blocked counts 不符:\n got:  %+v\n want: %+v", manifest.BlockedCounts, wantBlocked)
	}

	// 关键行判定。
	assertRow := func(table, pk string, assert func(t *testing.T, row RowDisposition)) {
		t.Helper()
		for _, row := range manifest.Rows {
			if row.SourceTable == table && len(row.SourcePK) >= 1 && row.SourcePK[0] == pk {
				assert(t, row)
				return
			}
		}
		t.Fatalf("manifest 缺少 %s/%s", table, pk)
	}
	assertRow("accounts", "acc-gpt", func(t *testing.T, row RowDisposition) {
		if row.Disposition != DispositionMapped || row.Rule != 1 {
			t.Fatalf("acc-gpt 判定不符: %+v", row)
		}
		if row.TargetFields["vendor_code"] != "gpt" || row.TargetFields["connection_driver_code"] != "gpt" {
			t.Fatalf("acc-gpt vendor/driver 不符: %+v", row.TargetFields)
		}
		if row.TargetFields["source_config_revision"] != "3" || row.TargetFields["target_config_revision"] != "3" {
			t.Fatalf("规则 1b revision 记录不符: %+v", row.TargetFields)
		}
		if row.TargetFields["health_check_endpoint_mode"] != "chat_json" {
			t.Fatalf("acc-gpt health-check 未解析: %+v", row.TargetFields)
		}
	})
	assertRow("accounts", "acc-hybrid", func(t *testing.T, row RowDisposition) {
		if row.TargetFields["connection_driver_code"] != "generic_http" || row.TargetFields["vendor_code"] != "NULL" {
			t.Fatalf("hybrid → generic_http 不符: %+v", row.TargetFields)
		}
		if row.Rule != 3 {
			t.Fatalf("hybrid 规则号应为 3: %d", row.Rule)
		}
	})
	assertRow("accounts", "acc-authinst", func(t *testing.T, row RowDisposition) {
		if row.Rule != 9 || row.Disposition != DispositionMapped || len(row.TargetIDs) != 0 {
			t.Fatalf("授权实例判定不符: %+v", row)
		}
	})
	assertRow("accounts", "acc-blank", func(t *testing.T, row RowDisposition) {
		if row.Disposition != DispositionBlocked || row.Rule != 0 {
			t.Fatalf("空白 provider_code 应规则 0 阻塞: %+v", row)
		}
	})
	assertRow("providers", "prov-openai", func(t *testing.T, row RowDisposition) {
		if row.TargetTable != "" || row.Disposition != DispositionMapped {
			t.Fatalf("openai provider 行应按设计退役: %+v", row)
		}
	})
	assertRow("groups", "grp-dup-a", func(t *testing.T, row RowDisposition) {
		if row.Disposition != DispositionBlocked {
			t.Fatalf("同名组应阻塞: %+v", row)
		}
	})

	// 规则 11 聚合行 3 条 + 缺表 note。
	historyRows := 0
	for _, row := range manifest.Rows {
		if row.Rule == 11 {
			historyRows++
			if row.Disposition != DispositionBlocked {
				t.Fatalf("历史行应 blocked: %+v", row)
			}
		}
	}
	if historyRows != 3 {
		t.Fatalf("规则 11 聚合行应 3 条: %d", historyRows)
	}
	missingNoted := false
	for _, note := range manifest.Notes {
		if strings.Contains(note, "model_check_runs") {
			missingNoted = true
		}
	}
	if !missingNoted {
		t.Fatalf("缺表历史表应写 note: %v", manifest.Notes)
	}

	// public model 冲突对象字段。
	if len(manifest.PublicModelConflicts) != 1 {
		t.Fatalf("public model 冲突应 1 条: %+v", manifest.PublicModelConflicts)
	}
	conflict := manifest.PublicModelConflicts[0]
	if conflict.ResourceAccountID != "acc-openai" || conflict.WireModel != "shared" || len(conflict.DirectModelIDs) != 1 || len(conflict.MappingIDs) != 1 {
		t.Fatalf("冲突对象字段不符: %+v", conflict)
	}

	// 脱敏：manifest JSON 不得含明文凭据片段；fingerprint 为 SHA-256(ciphertext)。
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest 失败: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET-CIPHER") || strings.Contains(string(encoded), "SECRET-IV") {
		t.Fatalf("manifest 泄漏明文凭据: %s", encoded)
	}
	digest := sha256.Sum256([]byte("v1:SECRET-IV:SECRET-TAG:SECRET-CIPHER-acc-gpt"))
	wantFingerprint := hex.EncodeToString(digest[:])
	assertRow("accounts", "acc-gpt", func(t *testing.T, row RowDisposition) {
		if row.TargetFields["credential_fingerprint"] != wantFingerprint {
			t.Fatalf("credential fingerprint 口径不符: %q != %q", row.TargetFields["credential_fingerprint"], wantFingerprint)
		}
	})

	// snapshot hash 确定性：同库重跑 hash 逐字节相同。
	repeat, err := DryRun(context.Background(), db, Options{Dialect: DialectSQLite, Now: fixedNow})
	if err != nil {
		t.Fatalf("重跑 dry-run 失败: %v", err)
	}
	if repeat.SourceSnapshotHash != manifest.SourceSnapshotHash {
		t.Fatalf("同一快照 hash 必须稳定: %q vs %q", manifest.SourceSnapshotHash, repeat.SourceSnapshotHash)
	}

	// 零写入证据：fixture 表行数不变。
	afterCounts := tableCounts(t, db)
	for name, count := range beforeCounts {
		if afterCounts[name] != count {
			t.Fatalf("dry-run 修改了数据库（%s: %d → %d）", name, count, afterCounts[name])
		}
	}
	_ = path
}

func TestDryRunRejectsUnknownDialect(t *testing.T) {
	if _, err := DryRun(context.Background(), nil, Options{Dialect: "mysql"}); err == nil {
		t.Fatalf("未知方言必须拒绝")
	}
}
