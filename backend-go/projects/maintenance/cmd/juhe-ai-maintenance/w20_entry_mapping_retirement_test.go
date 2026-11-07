package main

// revision 20（PLAN-20261106T162344524Z 批次C）钉测：跨协议入口映射退役步骤
// retire-cross-protocol-entry-mappings 按序执行三阶段——
// normalize-legacy-entry-mapping-families（唯一键冲突 canonical 优先删除 +
// 两侧 legacy token 归一 + 未知 token 不改写）、
// retire-cross-protocol-entry-mappings（删除非合规行、保留 Gemini stream 归一
// pair 5）、retire-codex-context-schema（DROP juhe_codex_context）。
// Satisfied/Verify 与 Apply 同源；重复执行零写入。外部真库授权门由 pgtestkit
// 承担（未授权 SKIP）。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w20SeedMappingFixture 播一个账户 + 连接 + 支持模型与 11 条入口映射，覆盖：
// canonical 锚点、anthropic/gemini 唯一键冲突、两侧/单侧 legacy 归一、Gemini
// legacy pair（冲突与非冲突各一条）、跨协议删除行、同族保留行、未知 token 行。
func w20SeedMappingFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// accounts.vendor_code 外键指向 vendors.code，先播 gpt vendor。
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.vendors
		(id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('gpt', 'gpt', 'OpenAI', 1, '[]', $1, $1)
		ON CONFLICT (code) DO NOTHING`, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys-w20', 'sys-w20', 'W20 系统账户', 'user', 'active', 'hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, now); err != nil {
		t.Fatalf("seed system account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.accounts
		(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-w20', 'sys-w20', 'gpt', 'gpt', 'openai', 'v1', 'acc-w20', 'api_key', 'active', 'sealed', $1, $1)`, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.account_connections
		(id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn-acc-w20', 'acc-w20', 'gpt_openai_v1', 'openai_v1', 'https://w20.example.test/v1', 1, $1, $1)`, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.models
		(id, vendor_code, public_model, upstream_model, scope, status, created_at, updated_at)
		VALUES ('mdl-w20', 'gpt', 'w20-model', 'w20-model', 'builtin', 'active', $1, $1)`, now); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.account_supported_models
		(account_id, model_id, created_at) VALUES ('acc-w20', 'mdl-w20', $1)`, now); err != nil {
		t.Fatalf("seed supported model: %v", err)
	}
	for _, row := range []struct{ id, sourceModel, sourceFamily, targetFamily string }{
		{"map-w20-01", "m1", "messages", "messages"},
		{"map-w20-02", "m1", "anthropic_messages", "messages"},
		{"map-w20-03", "m2", "anthropic_messages", "anthropic_messages"},
		{"map-w20-04", "m3", "gemini_stream_generate", "gemini_generate_content"},
		{"map-w20-05", "m4", "responses", "chat_completions"},
		{"map-w20-06", "m5", "chat_completions", "chat_completions"},
		{"map-w20-07", "m6", "video_generation", "video_generation"},
		{"map-w20-08", "m7", "legacy_unknown_token", "legacy_unknown_token"},
		{"map-w20-09", "m8", "generate_content", "gemini_generate_content"},
		{"map-w20-10", "m9", "stream_generate_content", "generate_content"},
		{"map-w20-11", "m9", "gemini_stream_generate", "gemini_generate_content"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.account_entry_mappings
			(id, account_id, source_model, source_endpoint_family, target_connection_id, target_model_id, target_endpoint_family, enabled, created_at, updated_at)
			VALUES ($1, 'acc-w20', $2, $3, 'conn-acc-w20', 'mdl-w20', $4, 1, $5, $5)`,
			row.id, row.sourceModel, row.sourceFamily, row.targetFamily, now); err != nil {
			t.Fatalf("seed mapping %s: %v", row.id, err)
		}
	}
	// juhe_codex_context schema 的存在性由测试自播（IF NOT EXISTS）：不依赖
	// ensure 加法相是否仍预建（批次 D 删除 pg_schema_codex_context.go 后本测试
	// 仍覆盖 schema 删除路径）。
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_codex_context;
		CREATE TABLE IF NOT EXISTS juhe_codex_context.codex_context_sessions (
			id text PRIMARY KEY, system_account_id text NOT NULL, api_key_id text,
			group_id text NOT NULL, source_response_id text, latest_response_id text,
			latest_compact_id text, created_at text NOT NULL, updated_at text NOT NULL,
			last_used_at text NOT NULL, expires_at text NOT NULL)`); err != nil {
		t.Fatalf("seed codex context schema: %v", err)
	}
}

func w20Families(t *testing.T, db *sql.DB, id string) (sql.NullString, string, string) {
	t.Helper()
	var families sql.NullString
	var sourceFamily, targetFamily string
	if err := db.QueryRowContext(context.Background(),
		`SELECT id, source_endpoint_family, target_endpoint_family FROM juhe_business.account_entry_mappings WHERE id = $1`, id).
		Scan(&families, &sourceFamily, &targetFamily); err != nil {
		if err == sql.ErrNoRows {
			return sql.NullString{}, "", ""
		}
		t.Fatalf("读映射 %s: %v", id, err)
	}
	return families, sourceFamily, targetFamily
}

func TestW20EntryMappingRetirementStep(t *testing.T) {
	td := pgtestkit.NewTestDatabase(t)
	ctx := context.Background()
	if _, err := schema.EnsurePostgres(ctx, td.DB); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	w20SeedMappingFixture(t, td.DB)

	steps := upgradeSteps(td.DSN, "w20-retirement-secret")
	var step *upgradepipeline.Step
	for index := range steps {
		if steps[index].ID == "retire-cross-protocol-entry-mappings" {
			step = &steps[index]
			break
		}
	}
	if step == nil {
		t.Fatal("注册表缺少 retire-cross-protocol-entry-mappings 步骤")
	}
	if step.Revision != 20 {
		t.Fatalf("步骤 revision = %d, want 20", step.Revision)
	}
	if !step.Destructive {
		t.Fatal("步骤应标记 destructive（expand 相不执行）")
	}
	env := upgradepipeline.Env{PrimaryDSN: td.DSN, Secret: "w20-retirement-secret"}

	// 与库内事实同源的 schema/表计数（detail 断言对齐用，不钉死 ensure 预建值）。
	var codexTablesPre, codexIndexesPre int
	if err := td.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'juhe_codex_context'`).Scan(&codexTablesPre); err != nil {
		t.Fatal(err)
	}
	if err := td.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_index x JOIN pg_class tbl ON tbl.oid = x.indrelid
		  JOIN pg_namespace n ON n.oid = tbl.relnamespace
		 WHERE n.nspname = 'juhe_codex_context' AND NOT x.indisprimary`).Scan(&codexIndexesPre); err != nil {
		t.Fatal(err)
	}
	if codexTablesPre < 1 {
		t.Fatal("前置条件失败：juhe_codex_context 应存在（ensure 预建 + 测试自播）")
	}

	// Apply 前：三阶段完成态谓词均未满足。
	if satisfied, err := step.Satisfied(ctx, env); err != nil || satisfied {
		t.Fatalf("Apply 前 Satisfied = %v / %v，want false", satisfied, err)
	}

	detail, err := step.Apply(ctx, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	normalize := detail["normalize-legacy-entry-mapping-families"].(map[string]any)
	retire := detail["retire-cross-protocol-entry-mappings"].(map[string]any)
	codex := detail["retire-codex-context-schema"].(map[string]any)

	// 阶段 0：唯一键冲突 canonical 优先删除（map-w20-02 anthropic、map-w20-11
	// gemini），两侧/单侧 legacy 归一（03/04/09），Gemini legacy pair 保留数只
	// 统计未被冲突删除的行（map-w20-04）。
	if got := normalize["deletedConflictRows"].(int); got != 2 {
		t.Fatalf("deletedConflictRows = %d, want 2", got)
	}
	if got := normalize["updatedRows"].(int); got != 3 {
		t.Fatalf("updatedRows = %d, want 3", got)
	}
	if got := normalize["geminiLegacyPairKept"].(int); got != 1 {
		t.Fatalf("geminiLegacyPairKept = %d, want 1", got)
	}
	if got := normalize["conflictsByAlias"].(map[string]int); got["anthropic_messages"] != 1 || got["gemini_stream_generate"] != 1 {
		t.Fatalf("conflictsByAlias = %v, want anthropic_messages:1 gemini_stream_generate:1", got)
	}
	if got := normalize["normalizedSourceByAlias"].(map[string]int); got["anthropic_messages"] != 1 || got["gemini_stream_generate"] != 1 {
		t.Fatalf("normalizedSourceByAlias = %v", got)
	}
	if got := normalize["normalizedTargetByAlias"].(map[string]int); got["anthropic_messages"] != 1 || got["gemini_generate_content"] != 2 {
		t.Fatalf("normalizedTargetByAlias = %v, want anthropic_messages:1 gemini_generate_content:2", got)
	}
	if got := normalize["unknownTokens"].(map[string]int); got["legacy_unknown_token"] != 2 {
		t.Fatalf("unknownTokens = %v, want legacy_unknown_token:2（两侧各一次）", got)
	}

	// 阶段 1：跨协议行删除，Gemini stream 归一 pair 保留。
	if got := retire["deletedRows"].(int); got != 1 {
		t.Fatalf("retire deletedRows = %d, want 1", got)
	}
	if got := retire["byPair"].(map[string]int); got["responses->chat_completions"] != 1 {
		t.Fatalf("retire byPair = %v, want responses->chat_completions:1", got)
	}

	// 阶段 2：schema 删除，计数与库内前置事实一致。
	if got := codex["schemaExistsBefore"].(bool); !got {
		t.Fatal("schemaExistsBefore = false, want true")
	}
	if got := codex["dropped"].(bool); !got {
		t.Fatal("dropped = false, want true")
	}
	if got := codex["tables"].(int); got != codexTablesPre {
		t.Fatalf("codex tables = %d, want %d（与库内前置事实一致）", got, codexTablesPre)
	}
	if got := codex["indexes"].(int); got != codexIndexesPre {
		t.Fatalf("codex non-primary indexes = %d, want %d（与库内前置事实一致）", got, codexIndexesPre)
	}

	if err := step.Verify(ctx, env); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if satisfied, err := step.Satisfied(ctx, env); err != nil || !satisfied {
		t.Fatalf("Apply 后 Satisfied = %v / %v，want true", satisfied, err)
	}

	// 库态断言：冲突行删除；归一行落现行词表；跨协议行删除；pair 5（legacy 与
	// canonical 两种拼法）保留；同族与未知 token 行不动（未知 token 不改写）。
	if families, _, _ := w20Families(t, td.DB, "map-w20-02"); families.Valid {
		t.Fatal("map-w20-02（anthropic_messages 唯一键冲突）应被删除")
	}
	if families, _, _ := w20Families(t, td.DB, "map-w20-11"); families.Valid {
		t.Fatal("map-w20-11（gemini_stream_generate 唯一键冲突）应被删除")
	}
	if families, _, _ := w20Families(t, td.DB, "map-w20-05"); families.Valid {
		t.Fatal("map-w20-05（responses -> chat_completions）应被删除")
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-03"); sourceFamily != "messages" || targetFamily != "messages" {
		t.Fatalf("map-w20-03 = (%s, %s), want (messages, messages)", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-04"); sourceFamily != "stream_generate_content" || targetFamily != "generate_content" {
		t.Fatalf("map-w20-04 = (%s, %s), want (stream_generate_content, generate_content)", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-09"); sourceFamily != "generate_content" || targetFamily != "generate_content" {
		t.Fatalf("map-w20-09 = (%s, %s), want (generate_content, generate_content)", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-01"); sourceFamily != "messages" || targetFamily != "messages" {
		t.Fatalf("map-w20-01 = (%s, %s), want 原样保留", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-06"); sourceFamily != "chat_completions" || targetFamily != "chat_completions" {
		t.Fatalf("map-w20-06 = (%s, %s), want 原样保留", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-07"); sourceFamily != "video_generation" || targetFamily != "video_generation" {
		t.Fatalf("map-w20-07 = (%s, %s), want 原样保留", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-08"); sourceFamily != "legacy_unknown_token" || targetFamily != "legacy_unknown_token" {
		t.Fatalf("map-w20-08 = (%s, %s), want 未知 token 原样保留（不静默改写）", sourceFamily, targetFamily)
	}
	if _, sourceFamily, targetFamily := w20Families(t, td.DB, "map-w20-10"); sourceFamily != "stream_generate_content" || targetFamily != "generate_content" {
		t.Fatalf("map-w20-10 = (%s, %s), want canonical pair 5 原样保留", sourceFamily, targetFamily)
	}

	var codexAfter int
	if err := td.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'juhe_codex_context'`).Scan(&codexAfter); err != nil {
		t.Fatal(err)
	}
	if codexAfter != 0 {
		t.Fatalf("juhe_codex_context schema 仍存在（%d）", codexAfter)
	}

	// 幂等：重跑零写入（schema 已缺席 → 不再 DROP）。
	again, err := step.Apply(ctx, env)
	if err != nil {
		t.Fatalf("重跑 Apply: %v", err)
	}
	againNormalize := again["normalize-legacy-entry-mapping-families"].(map[string]any)
	againRetire := again["retire-cross-protocol-entry-mappings"].(map[string]any)
	againCodex := again["retire-codex-context-schema"].(map[string]any)
	if got := againNormalize["deletedConflictRows"].(int); got != 0 {
		t.Fatalf("重跑 deletedConflictRows = %d, want 0", got)
	}
	if got := againNormalize["updatedRows"].(int); got != 0 {
		t.Fatalf("重跑 updatedRows = %d, want 0", got)
	}
	if got := againRetire["deletedRows"].(int); got != 0 {
		t.Fatalf("重跑 retire deletedRows = %d, want 0", got)
	}
	if got := againCodex["schemaExistsBefore"].(bool); got {
		t.Fatal("重跑 schemaExistsBefore = true, want false")
	}
	if got := againCodex["dropped"].(bool); got {
		t.Fatal("重跑 dropped = true, want false")
	}
}
