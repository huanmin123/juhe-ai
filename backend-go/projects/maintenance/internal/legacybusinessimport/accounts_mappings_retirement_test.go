package legacybusinessimport

// PLAN-20261106T162344524Z 批次C 回归：回迁链不得重建跨协议模型映射。
// importMappings 在解析模型之前先按反向别名表归一两侧 family token，再按协议
// 等价判定（同族或 Gemini stream 归一 pair）校验：非合规行显式拒绝（跳过 +
// 逐条中文原因进报告，不产生模型补建/绑定副作用），合规行以归一后的现行
// token 落库。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// openMappingRetirementFixtureDB 建一对 source/target 夹具库：source 只有旧库
// 的 account_model_mappings 列集；target 有 models / account_supported_models /
// account_entry_mappings 最小列集。
func openMappingRetirementFixtureDB(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	source := pgtestkit.New(t, func(_ context.Context, conn *sql.DB) error {
		_, err := conn.Exec(`CREATE SCHEMA juhe_business;
			CREATE TABLE juhe_business.account_model_mappings (
				account_id text NOT NULL,
				provider_code text NOT NULL,
				source_model text NOT NULL,
				source_endpoint_family text NOT NULL,
				upstream_model text NOT NULL,
				upstream_endpoint_family text NOT NULL,
				enabled integer NOT NULL DEFAULT 1,
				created_at text NOT NULL,
				updated_at text NOT NULL
			)`)
		return err
	})
	target := pgtestkit.New(t, func(_ context.Context, conn *sql.DB) error {
		_, err := conn.Exec(`CREATE SCHEMA juhe_business;
			CREATE TABLE juhe_business.models (
				id text PRIMARY KEY,
				vendor_code text,
				public_model text NOT NULL,
				upstream_model text NOT NULL,
				scope text NOT NULL,
				system_account_id text,
				status text NOT NULL,
				catalog_visible boolean NOT NULL DEFAULT true,
				supported_endpoint_modes_json text NOT NULL DEFAULT '[]',
				supported_service_tiers_json text NOT NULL DEFAULT '[]',
				supported_reasoning_efforts_json text NOT NULL DEFAULT '[]',
				codex_supported_reasoning_levels_json text NOT NULL DEFAULT '[]',
				service_tier_prices_json text NOT NULL DEFAULT '{}',
				long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false,
				supports_prompt_caching boolean NOT NULL DEFAULT false,
				currency text NOT NULL DEFAULT 'USD',
				input_usd_per_1m double precision,
				output_usd_per_1m double precision,
				cached_input_usd_per_1m double precision,
				cache_write_usd_per_1m double precision,
				cache_write_1h_usd_per_1m double precision,
				cache_storage_usd_per_1m_per_hour double precision,
				image_input_usd_per_1m double precision,
				image_output_usd_per_1m double precision,
				audio_input_usd_per_1m double precision,
				audio_output_usd_per_1m double precision,
				output_usd_per_image double precision,
				created_at text NOT NULL,
				updated_at text NOT NULL
			);
			CREATE TABLE juhe_business.account_supported_models (
				account_id text NOT NULL,
				model_id text NOT NULL,
				created_at text NOT NULL,
				PRIMARY KEY (account_id, model_id)
			);
			CREATE TABLE juhe_business.account_entry_mappings (
				id text PRIMARY KEY,
				account_id text NOT NULL,
				source_model text NOT NULL,
				source_endpoint_family text NOT NULL,
				target_connection_id text NOT NULL,
				target_model_id text NOT NULL,
				target_endpoint_family text NOT NULL,
				enabled integer NOT NULL DEFAULT 1,
				created_at text NOT NULL,
				updated_at text NOT NULL,
				UNIQUE (account_id, source_model, source_endpoint_family)
			)`)
		return err
	})
	return source, target
}

func TestImportMappingsRejectsCrossProtocolAndNormalizesFamilies(t *testing.T) {
	source, target := openMappingRetirementFixtureDB(t)
	ctx := context.Background()

	// 目录：只有一个有价 builtin 模型；被拒绝行的上游模型故意用目录外名字，
	// 证明拒绝发生在模型解析之前、不产生补建副作用。
	if _, err := target.ExecContext(ctx, `INSERT INTO juhe_business.models
		(id, vendor_code, public_model, upstream_model, scope, status, input_usd_per_1m, output_usd_per_1m, created_at, updated_at)
		VALUES ('mdl-gpt-4o-mini','gpt','gpt-4o-mini','gpt-4o-mini','builtin','active',1,2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// 旧库映射：1 同族合规、2 Gemini legacy pair（归一后保留）、3 跨协议拒绝、
	// 4 anthropic legacy 同族（归一后合规）、5 跨协议且上游为目录外模型（拒绝
	// 且不得补建 personal 模型）。
	for _, row := range []struct{ sourceModel, sourceFamily, upstreamModel, upstreamFamily string }{
		{"alias-responses", "responses", "gpt-4o-mini", "responses"},
		{"alias-gemini-legacy", "gemini_stream_generate", "gpt-4o-mini", "gemini_generate_content"},
		{"alias-cross-protocol", "responses", "gpt-4o-mini", "chat_completions"},
		{"alias-anthropic-legacy", "anthropic_messages", "gpt-4o-mini", "anthropic_messages"},
		{"alias-cross-ghost", "chat_completions", "ghost-upstream-model", "responses"},
	} {
		if _, err := source.ExecContext(ctx, `INSERT INTO juhe_business.account_model_mappings
			(account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
			VALUES ('account-a','gpt',$1,$2,$3,$4,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
			row.sourceModel, row.sourceFamily, row.upstreamModel, row.upstreamFamily); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	report := &Report{Tables: map[string]int{}, Notes: []string{}}
	accounts := map[string]struct{}{"account-a": {}}
	owners := map[string]string{"account-a": "owner-a"}
	if err := importMappings(ctx, source, tx, accounts, owners, map[string]string{"account-a": "gpt"}, report); err != nil {
		t.Fatal(err)
	}

	// 合规 3 条落库；2 条跨协议拒绝。
	if report.Tables["account_entry_mappings"] != 3 {
		t.Fatalf("account_entry_mappings = %d, want 3", report.Tables["account_entry_mappings"])
	}
	if report.Tables["account_entry_mappings_rejected_cross_protocol"] != 2 {
		t.Fatalf("rejected_cross_protocol = %d, want 2", report.Tables["account_entry_mappings_rejected_cross_protocol"])
	}

	// 落库行的 family 以归一后的现行 token 持久化。
	stored := map[string][2]string{}
	rows, err := tx.QueryContext(ctx,
		`SELECT source_model, source_endpoint_family, target_endpoint_family FROM juhe_business.account_entry_mappings ORDER BY source_model`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sourceModel, sourceFamily, targetFamily string
		if err := rows.Scan(&sourceModel, &sourceFamily, &targetFamily); err != nil {
			t.Fatal(err)
		}
		stored[sourceModel] = [2]string{sourceFamily, targetFamily}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"alias-responses":        {"responses", "responses"},
		"alias-gemini-legacy":    {"stream_generate_content", "generate_content"},
		"alias-anthropic-legacy": {"messages", "messages"},
	}
	if len(stored) != len(want) {
		t.Fatalf("落座行 = %v, want %v", stored, want)
	}
	for sourceModel, families := range want {
		got, ok := stored[sourceModel]
		if !ok {
			t.Fatalf("合规映射 %s 未落库", sourceModel)
		}
		if got != families {
			t.Fatalf("%s family = %v, want %v（须以归一后现行 token 落库）", sourceModel, got, families)
		}
	}

	// 拒绝行零副作用：目录外上游模型不得补建 personal 行、不得登记支持模型。
	var created int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.models WHERE upstream_model = 'ghost-upstream-model'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("被拒绝映射的上游模型不得补建，found %d", created)
	}

	// 报告逐条给出中文原因。
	joined := strings.Join(report.Notes, "\n")
	for _, phrase := range []string{"拒绝回迁", "跨协议模型映射已退役", "客户端协议与上游协议必须一致"} {
		if !strings.Contains(joined, phrase) {
			t.Fatalf("拒绝原因缺少 %q：\n%s", phrase, joined)
		}
	}
	if got := strings.Count(joined, "拒绝回迁账户 account-a"); got != 2 {
		t.Fatalf("逐条拒绝原因应有 2 条（每行一条），got %d", got)
	}
}
