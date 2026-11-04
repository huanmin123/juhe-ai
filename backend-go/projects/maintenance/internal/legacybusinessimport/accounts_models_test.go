package legacybusinessimport

// BUG-0344 根治回归：回迁 CLI 不得再制造「可绑定但不可计价」的 personal 模型行。
//
// 旧实现 `createPersonalModel` 直接落 `active` 且不写价格，随后无条件绑进
// `account_supported_models`；这些行在网关计价目录的 priced 过滤里被剔除
// （目录 miss），成功流量 `cost_usd` 恒 NULL。现约定：补建行落 `draft`、不写
// 价格、不绑定；目录中已存在但不可绑定的行（无价 personal 等）同样不绑定。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// openModelImportFixtureDB 建一对 source/target 夹具库：source 只有旧库的账户
// 支持模型列集，target 有 models / account_supported_models 与 createPersonalModel
// 写入所需的全部列。
func openModelImportFixtureDB(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	source := pgtestkit.New(t, func(_ context.Context, conn *sql.DB) error {
		_, err := conn.Exec(`CREATE SCHEMA juhe_business;
			CREATE TABLE juhe_business.account_supported_models (
				account_id text NOT NULL,
				provider_code text NOT NULL,
				model text NOT NULL,
				created_at text NOT NULL
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
			)`)
		return err
	})
	return source, target
}

func TestImportSupportedModelsSkipsUnpricedAndCreatesDraft(t *testing.T) {
	source, target := openModelImportFixtureDB(t)
	ctx := context.Background()

	// 旧库：account-a 同时绑定了一个有价 builtin 模型与一个目录外自定义名；
	// account-b 只绑定了目录外自定义名。
	for _, row := range []struct{ account, provider, model string }{
		{"account-a", "gpt", "gpt-4o-mini"},
		{"account-a", "gpt", "relay-custom-model"},
		{"account-b", "gpt", "relay-custom-model"},
	} {
		if _, err := source.ExecContext(ctx, `INSERT INTO juhe_business.account_supported_models
			(account_id, provider_code, model, created_at) VALUES ($1,$2,$3,'2026-01-01T00:00:00Z')`,
			row.account, row.provider, row.model); err != nil {
			t.Fatal(err)
		}
	}
	// 目标目录：gpt-4o-mini 是有价 builtin；另有一条无价 personal 行（模拟历史
	// 回迁产物），它的同名账户绑定也不得重新绑定。
	if _, err := target.ExecContext(ctx, `INSERT INTO juhe_business.models
		(id, vendor_code, public_model, upstream_model, scope, status, input_usd_per_1m, output_usd_per_1m, created_at, updated_at)
		VALUES ('mdl-gpt-4o-mini','gpt','gpt-4o-mini','gpt-4o-mini','builtin','active',1,2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, `INSERT INTO juhe_business.models
		(id, vendor_code, public_model, upstream_model, scope, system_account_id, status, created_at, updated_at)
		VALUES ('mdl-legacy-unpriced','gpt','legacy-unpriced','legacy-unpriced','personal','owner-b','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, `INSERT INTO juhe_business.account_supported_models
		(account_id, provider_code, model, created_at) VALUES ('account-b','gpt','legacy-unpriced','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	accounts := map[string]struct{}{"account-a": {}, "account-b": {}}
	owners := map[string]string{"account-a": "owner-a", "account-b": "owner-b"}
	report := &Report{Tables: map[string]int{}, Notes: []string{}}
	if err := importSupportedModels(ctx, source, tx, accounts, owners, report); err != nil {
		t.Fatal(err)
	}

	// 有价 builtin 正常绑定。
	var bound int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.account_supported_models
		WHERE account_id = 'account-a' AND model_id = 'mdl-gpt-4o-mini'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("有价 builtin 应绑定，count = %d", bound)
	}
	// 目录外自定义名：补建为 draft（无价），且不绑定。
	var status string
	var priced bool
	if err := tx.QueryRowContext(ctx, `SELECT status,
			(input_usd_per_1m IS NOT NULL OR output_usd_per_1m IS NOT NULL OR
			 COALESCE(service_tier_prices_json,'{}') NOT IN ('{}','null'))
		FROM juhe_business.models WHERE vendor_code = 'gpt' AND upstream_model = 'relay-custom-model'`).
		Scan(&status, &priced); err != nil {
		t.Fatalf("补建行应存在: %v", err)
	}
	if status != "draft" || priced {
		t.Fatalf("补建行必须 draft 且无价，got status=%q priced=%v", status, priced)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.account_supported_models
		WHERE model_id IN (SELECT id FROM juhe_business.models WHERE upstream_model = 'relay-custom-model')`).
		Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 0 {
		t.Fatalf("draft 补建行不得被绑定，count = %d", bound)
	}
	// 已存在的无价 personal 行不得被重新绑定。
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.account_supported_models
		WHERE model_id = 'mdl-legacy-unpriced'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 0 {
		t.Fatalf("无价 personal 行不得被绑定，count = %d", bound)
	}
	// 报告计数：1 条成功绑定、1 个 draft 补建、3 条因无价跳过（两条自定义名
	// 绑定 + 一条历史无价行），account-b 最终零支持模型。
	if report.Tables["account_supported_models"] != 1 {
		t.Fatalf("account_supported_models = %d, want 1", report.Tables["account_supported_models"])
	}
	if report.Tables["models_created"] != 1 {
		t.Fatalf("models_created = %d, want 1", report.Tables["models_created"])
	}
	if report.Tables["account_supported_models_skipped_unpriced"] != 3 {
		t.Fatalf("skipped_unpriced = %d, want 3", report.Tables["account_supported_models_skipped_unpriced"])
	}
	if report.Tables["accounts_without_supported_models"] != 1 {
		t.Fatalf("accounts_without_supported_models = %d, want 1", report.Tables["accounts_without_supported_models"])
	}
}
