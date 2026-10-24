package settings

// pgkit_test.go 是 settings 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役；本切片只迁测试，
// 不删除任何生产 sqlite 臂）：pgtestkit 每测试独立库 + 生产 store 的 PG 臂
// （New Postgres + juhe_business schema 限定 + $n 占位符 + boolean 绑定，
// 与 cmd/juhe-ai-gateway/compose.go 的 businessSettings 生产装配同一形态）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：settings_test.go /
//     w7a_extra_test.go 建库用例与 w9d_extra_test.go 的 TestW9DPutGlobal
//     InsertConflict 走本文件夹具；测试体断言逐字节保持；夹具手写 seed/校验
//     SQL 经 fixtureSQL 对齐生产 PG 臂（juhe_business 限定 + $n），seed 的
//     catalog_visible 按 PG boolean 形态写 TRUE（列类型差异，见
//     settings.go boolBind 同因）。
//   - 组B（SQLite-only 生产臂为被测对象）：TestStoreBoolBindDialect 的
//     SQLite 绑定分支、scripted w9d driver 对 SQLite 臂 SQL 形态的断言、
//     w7a 方言臂的 &Store{mode: SQLite} 纯断言——按 D17 不提前迁移也不提前
//     删除，随生产 sqlite 臂删除批次同批退役。
//   - scripted 假驱动（w9dScript driver 族）不依赖真库，保持原状。
//
// 夹具 schema 与 maintenance 生产 DDL（pg_schema_business_tables.go）精确
// 对齐：global_settings / system_settings / vendors / models 为生产全列 +
// 全部 CHECK；按 groups/statreads 夹具先例放宽一处：不建 FOREIGN KEY
// （system_accounts 夹具不在本包列集内，seed 行不满足生产外键链）。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// settingsPGFixtureSchema 列集/类型/约束照抄 maintenance 生产 DDL
// （pg_schema_business_tables.go），仅放宽 FOREIGN KEY（见文件头注释）。
const settingsPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.global_settings (
      key text PRIMARY KEY,
      value_json text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.vendors (
      id text PRIMARY KEY,
      code text NOT NULL UNIQUE,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      default_supported_models_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.models (
      id text PRIMARY KEY,
      vendor_code text,
      public_model text NOT NULL,
      upstream_model text NOT NULL,
      scope text NOT NULL,
      system_account_id text,
      status text NOT NULL DEFAULT 'active',
      catalog_visible boolean NOT NULL DEFAULT true,
      catalog_order integer,
      release_date text,
      shutdown_date text,
      supported_endpoint_modes_json text NOT NULL DEFAULT '[]',
      supported_service_tiers_json text NOT NULL DEFAULT '[]',
      supported_reasoning_efforts_json text NOT NULL DEFAULT '[]',
      default_reasoning_effort text,
      codex_supported_reasoning_levels_json text NOT NULL DEFAULT '[]',
      codex_default_reasoning_level text,
      codex_multi_agent_version text,
      context_window_tokens integer,
      max_input_tokens integer,
      max_output_tokens integer,
      max_tokens integer,
      supports_prompt_caching boolean NOT NULL DEFAULT false,
      source text,
      pricing_notes text,
      capability_notes text,
      notes text,
      created_by text,
      updated_by text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      input_usd_per_1m double precision,
      output_usd_per_1m double precision,
      cached_input_usd_per_1m double precision,
      cache_write_usd_per_1m double precision,
      cache_write_1h_usd_per_1m double precision,
      cache_storage_usd_per_1m_per_hour double precision,
      service_tier_prices_json text NOT NULL DEFAULT '{}',
      long_context_input_token_threshold integer,
      long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false,
      long_context_input_cost_multiplier double precision,
      long_context_output_cost_multiplier double precision,
      image_input_usd_per_1m double precision,
      image_output_usd_per_1m double precision,
      audio_input_usd_per_1m double precision,
      audio_output_usd_per_1m double precision,
      output_usd_per_image double precision,
      currency text NOT NULL DEFAULT 'USD',
      CHECK (status IN ('draft', 'active', 'disabled')),
      CHECK (scope IN ('builtin', 'global', 'personal')),
      CHECK (jsonb_typeof(supported_endpoint_modes_json::jsonb) = 'array'),
      CHECK (jsonb_typeof(supported_service_tiers_json::jsonb) = 'array'),
      CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array'),
      CHECK (jsonb_typeof(codex_supported_reasoning_levels_json::jsonb) = 'array'),
      CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object'),
      CHECK (
        (scope = 'personal' AND system_account_id IS NOT NULL)
        OR (scope IN ('builtin', 'global') AND system_account_id IS NULL)
      )
    );
`

// fixtureTablePattern 是夹具手写 seed/校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_business. 限定同形式）。
var fixtureTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(global_settings|system_settings|models|vendors)\b`)

// fixtureBind 把夹具手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind
// 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func fixtureBind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// fixtureSQL 把夹具手写 SQL 对齐生产 PG 臂方言（juhe_business 限定 + $n）。
func fixtureSQL(query string) string {
	return fixtureBind(fixtureTablePattern.ReplaceAllString(query, "$1 juhe_business.$2"))
}

// newSettingsPGTestDatabase 创建 pgtestkit 隔离 PG 测试库并注入夹具 schema
// （DSN 供 closed-pool 错误臂二次开池）。
func newSettingsPGTestDatabase(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, settingsPGFixtureSchema)
		return err
	})
}

// newSettingsPGDatabase 是不需要 DSN 的组A测试入口。
func newSettingsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return newSettingsPGTestDatabase(t).DB
}
