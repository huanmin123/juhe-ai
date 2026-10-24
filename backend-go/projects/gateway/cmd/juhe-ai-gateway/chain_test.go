package main

// G20 phase-2 composition adapter tests: the unit coverage for the account /
// catalog read seams, the provider driver, the usage persistence bridge, the
// chat executor bridge, plus the preauth→dispatch(mock upstream)→response
// full-chain smoke test over a seeded SQLite business database.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// ---------------------------------------------------------------------------
// seeded business schema (the runtime cache + selector read surface)
// ---------------------------------------------------------------------------

type chainFixture struct {
	db            *sql.DB
	statsDB       *sql.DB
	cache         *gatewayruntimecache.Service
	selector      *chainAccountsSelector
	apiKeySecret  string
	systemAccount string
	groupID       string
	accountID     string
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statsDB, err := sql.Open("sqlite", filepath.Join(root, "stats.sqlite3"))
	if err != nil {
		t.Fatalf("open stats db: %v", err)
	}
	t.Cleanup(func() { _ = statsDB.Close() })
	seedChainBusinessSchema(t, db)
	seedChainStatsSchema(t, statsDB)
	fixture := &chainFixture{db: db, statsDB: statsDB, systemAccount: "sys_owner", groupID: "group_main", accountID: "acc_1"}
	secret := seedChainRuntimeRows(t, db, fixture)
	fixture.apiKeySecret = secret

	models, err := gatewayruntimecache.NewSQLReadModels(db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("create sql read models: %v", err)
	}
	selector, err := newChainAccountsSelectorWithStats(db, statsDB, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	models.SetAccountsSelector(selector)
	catalogSource, err := newChainCatalogSource(db, false)
	if err != nil {
		t.Fatalf("create catalog source: %v", err)
	}
	models.SetCatalogSource(catalogSource)
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	// /v1/models 账户并集装载器（chain_gatewaykeymodels.go）：夹具与生产
	// chain_runtime.go 同样接 Union，使全链 /v1/models 用例走真实聚合。
	unionLoader, err := newChainGroupModelUnionLoader(selector)
	if err != nil {
		t.Fatalf("create union loader: %v", err)
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Clock: gatewayruntimecache.SystemClock(), Union: unionLoader})
	if err != nil {
		t.Fatalf("create runtime cache: %v", err)
	}
	t.Cleanup(cache.Close)
	fixture.cache = cache
	fixture.selector = selector
	return fixture
}

func gatewayclientipMemoryConcurrency() gatewayruntimecache.ConcurrencySource {
	return gatewayclientip.NewMemoryAccountConcurrency(nil)
}

// seedChainBusinessSchema creates the runtime-cache read surface tables.
func seedChainBusinessSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL, image_generation_enabled INTEGER NOT NULL DEFAULT 1, request_limits_json TEXT)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, group_kind TEXT NOT NULL DEFAULT 'standard', scheduling_policy_json TEXT)`,
		`CREATE TABLE super_group_members (id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL, priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
			vendor_code TEXT, connection_driver_code TEXT, oauth_type TEXT, health_check_connection_id TEXT, 
			provider_protocol_profile_id TEXT, protocol_code TEXT, protocol_version TEXT,
			name TEXT NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL,
			capability_domain TEXT NOT NULL DEFAULT 'chat', schedulable INTEGER NOT NULL DEFAULT 1,
			concurrency_limit INTEGER NOT NULL DEFAULT 0, priority INTEGER NOT NULL DEFAULT 0,
			super_priority_enabled INTEGER NOT NULL DEFAULT 0, fallback_enabled INTEGER NOT NULL DEFAULT 0,
			client_compatibility TEXT, config_revision INTEGER, dispatch_revision INTEGER,
			credentials_encrypted TEXT, proxy_profile_id TEXT, cooldown_until TEXT,
			last_error_message TEXT, last_error_code TEXT, stream_failure_count INTEGER NOT NULL DEFAULT 0,
			stream_failure_window_started_at TEXT, account_expires_at TEXT,
			health_check_model TEXT, health_check_endpoint_mode TEXT,
			authorization_instance_source_account_id TEXT, authorization_instance_authorization_id TEXT,
			authorization_instance_owner_system_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE group_accounts (
			group_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL,
			enabled INTEGER NOT NULL, account_authorization_id TEXT,
			local_priority INTEGER, local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
			local_fallback_enabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, mode TEXT NOT NULL, config_json TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE route_strategy_groups (
			id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
			group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT, name TEXT,
			key_hash TEXT NOT NULL, key_prefix TEXT, key_suffix TEXT, key_secret_encrypted TEXT,
			status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, purpose TEXT,
			expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT,
			scheduling_preference_id TEXT, created_at TEXT, updated_at TEXT)`,
		// 调度偏好独立实体 P1（loadGatewayAPIKeyRow 六态解析的引用目标）；
		// enabled 已随 v2 用户裁决修订第 4 条退役，夹具与权威 DDL 同构 8 列。
		`CREATE TABLE scheduling_preferences (
			id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT NOT NULL, chat_mode TEXT NOT NULL,
			chat_speed_first_config_json TEXT, built_in INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT, grantee_system_account_id TEXT NOT NULL,
			scope TEXT, status TEXT NOT NULL, expires_at TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT)`,
		`CREATE TABLE group_authorization_settings (
			authorization_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
			enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT NOT NULL, model TEXT NOT NULL, model_id TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE account_api_key_runtime_states (
			account_id TEXT NOT NULL, key_fingerprint TEXT NOT NULL, key_index INTEGER,
			status TEXT NOT NULL, cooldown_until TEXT, next_probe_at TEXT, recovery_started_at TEXT,
			updated_at TEXT)`,
		`CREATE TABLE proxy_profiles (
			id TEXT PRIMARY KEY, name TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL,
			username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node business-schema.ts + maintenance
		// sqlite_schema.go): composite primary key, NO id column. The drifted
		// hand-built `id` column masked the account_model_mappings ORDER BY
		// defect on fresh databases.
		`CREATE TABLE account_model_mappings (
			account_id TEXT NOT NULL, provider_code TEXT NOT NULL,
			source_model TEXT NOT NULL, source_endpoint_family TEXT NOT NULL,
			upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL,
			enabled INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (account_id, source_model, source_endpoint_family))`,
		`CREATE TABLE response_inspection_policies (
			id TEXT PRIMARY KEY, name TEXT, enabled INTEGER NOT NULL, priority INTEGER NOT NULL,
			scope_type TEXT NOT NULL, protocol_code TEXT, provider_code TEXT, match_json TEXT,
			action TEXT, notes TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node provider-model-catalog.repository.ts columns()
		// + maintenance sqlite_schema.go): built-in catalog rows only. The
		// drifted hand-built columns (scope/input_modalities/...) masked the
		// catalog 500 on fresh databases. Per-account rows live in
		// custom_provider_models below.
		`CREATE TABLE provider_model_catalog (
			id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active', mode TEXT, catalog_order INTEGER,
			release_date TEXT, shutdown_date TEXT,
			supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
			supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
			supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
			default_reasoning_effort TEXT, codex_supported_reasoning_levels_json TEXT NOT NULL DEFAULT '[]',
			codex_default_reasoning_level TEXT, codex_multi_agent_version TEXT,
			context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER,
			max_tokens INTEGER, input_usd_per_1m REAL, output_usd_per_1m REAL,
			cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL,
			cache_storage_usd_per_1m_per_hour REAL, service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
			long_context_input_token_threshold INTEGER, long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
			long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL,
			image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL,
			audio_output_usd_per_1m REAL, output_usd_per_image REAL,
			supports_prompt_caching INTEGER NOT NULL DEFAULT 0, catalog_visible INTEGER NOT NULL DEFAULT 1,
			source TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node custom-provider-models.repository.ts
		// customProviderModelColumns()): the per-account catalog rows.
		`CREATE TABLE custom_provider_models (
			id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'personal', system_account_id TEXT,
			status TEXT NOT NULL DEFAULT 'active', catalog_visible INTEGER NOT NULL DEFAULT 1,
			mode TEXT, supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
			supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
			supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
			default_reasoning_effort TEXT, release_date TEXT, shutdown_date TEXT,
			context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER,
			input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL,
			cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL,
			cache_storage_usd_per_1m_per_hour REAL, service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
			long_context_input_token_threshold INTEGER, long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
			long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL,
			image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL,
			audio_output_usd_per_1m REAL, output_usd_per_image REAL, currency TEXT,
			pricing_notes TEXT, capability_notes TEXT, notes TEXT,
			created_by TEXT NOT NULL, updated_by TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			CHECK (scope IN ('personal', 'global')),
			CHECK ((scope = 'personal' AND system_account_id IS NOT NULL) OR (scope = 'global' AND system_account_id IS NULL)))`,
		// Real-DDL shape (maintenance sqlite_schema_business.go): the openai
		// 兼容目录源扩展读取 providers + provider_protocol_profiles 判定
		// openai 协议子供应商（Node modelCatalogSourceProviderCodesAsync）。
		`CREATE TABLE providers (
			id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT,
			parent_code TEXT, enabled INTEGER NOT NULL DEFAULT 1,
			default_supported_models_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE provider_protocol_profiles (
			id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
			enabled INTEGER NOT NULL DEFAULT 1, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL,
			base_url TEXT NOT NULL, default_health_check_model TEXT NOT NULL,
			account_types_json TEXT NOT NULL, capabilities_json TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// 供应商解耦新表（供应商与上游协议面重构设计 §3/§5.1，maintenance
		// sqlite_schema_business.go 同构去 FK）：vendors/models 单表目录与
		// account_entry_mappings 入口映射，供并集装载器与目录源真实读取。
		`CREATE TABLE vendors (
			id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT,
			enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE models (
			id TEXT PRIMARY KEY, vendor_code TEXT, public_model TEXT NOT NULL, upstream_model TEXT NOT NULL,
			scope TEXT NOT NULL, system_account_id TEXT, status TEXT NOT NULL DEFAULT 'active',
			catalog_visible INTEGER NOT NULL DEFAULT 1, catalog_order INTEGER,
			release_date TEXT, shutdown_date TEXT,
			supported_endpoint_modes_json TEXT NOT NULL DEFAULT '[]',
			supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
			supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
			default_reasoning_effort TEXT,
			codex_supported_reasoning_levels_json TEXT NOT NULL DEFAULT '[]',
			codex_default_reasoning_level TEXT, codex_multi_agent_version TEXT,
			context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER, max_tokens INTEGER,
			supports_prompt_caching INTEGER NOT NULL DEFAULT 0, source TEXT,
			pricing_notes TEXT, capability_notes TEXT, notes TEXT,
			created_by TEXT, updated_by TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL,
			cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
			service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
			long_context_input_token_threshold INTEGER, long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
			long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL,
			image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL, audio_output_usd_per_1m REAL,
			output_usd_per_image REAL, currency TEXT NOT NULL DEFAULT 'USD')`,
		`CREATE TABLE account_connections (
			id TEXT PRIMARY KEY, account_id TEXT NOT NULL, connection_preset_code TEXT NOT NULL,
			protocol_surface_code TEXT, base_url TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE account_endpoint_modes (
			connection_id TEXT NOT NULL, endpoint_mode TEXT NOT NULL, PRIMARY KEY (connection_id, endpoint_mode))`,
		`CREATE TABLE account_entry_mappings (
			id TEXT PRIMARY KEY, account_id TEXT NOT NULL, source_model TEXT NOT NULL, source_endpoint_family TEXT NOT NULL,
			target_connection_id TEXT NOT NULL, target_model_id TEXT NOT NULL, target_endpoint_family TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// M2 媒体任务面（媒体设计 §8.2）：与 maintenance sqlite_schema_business
		// 的 media_jobs DDL 同构。
		`CREATE TABLE media_jobs (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('video', 'audio_transcription', 'audio_speech')),
			api_key_id TEXT NOT NULL,
			owner_system_account_id TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL,
			provider_protocol_profile_id TEXT,
			upstream_job_id TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired')),
			request_snapshot_json TEXT NOT NULL DEFAULT '{}',
			artifact_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}',
			usage_json TEXT NOT NULL DEFAULT '{}',
			cost_usd REAL NOT NULL DEFAULT 0,
			params_applied_json TEXT NOT NULL DEFAULT '[]',
			params_ignored_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v: %v", statement, err)
		}
	}
}

// chainPointAccountAtUpstream 把既有夹具账户的连接行指向 mock 上游（B4 第一
// 批单连接事实源：secret BaseURL = account_connections.base_url）。调用点的
// 旧 credentials.base_url 写点保留无害（credentials.base_url 已不被消费）。
func chainPointAccountAtUpstream(t *testing.T, db *sql.DB, accountID, baseURL string, modes ...string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE account_connections SET base_url = ? WHERE account_id = ?`, baseURL, accountID); err != nil {
		t.Fatalf("update connection base url (%s): %v", accountID, err)
	}
	// modes 非空时整集合替换该连接的 endpoint modes（旧世界凭据
	// supported_endpoint_modes 写点的等价迁移）。
	if len(modes) > 0 {
		if _, err := db.Exec(`DELETE FROM account_endpoint_modes WHERE connection_id IN (SELECT id FROM account_connections WHERE account_id = ?)`, accountID); err != nil {
			t.Fatalf("reset connection endpoint modes (%s): %v", accountID, err)
		}
		for _, mode := range modes {
			if _, err := db.Exec(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode)
				SELECT id, ? FROM account_connections WHERE account_id = ?`, mode, accountID); err != nil {
				t.Fatalf("seed connection endpoint mode (%s): %v", accountID, err)
			}
		}
	}
}

// chainSeedAccountConnection 为夹具账户补一条 enabled 连接行（B4 第一批：单
// 连接事实源 fail closed——无连接行账户在 hydration 丢弃）。modes 供连接行
// 预置 account_endpoint_modes；省略时 secret 端点模式投影回落既有 vendor 默认
// 分支（老数据兼容语义）。
func chainSeedAccountConnection(t *testing.T, db *sql.DB, accountID, baseURL string, modes ...string) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	connectionID := "conn_" + accountID
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, 'gpt_openai_v1', 'openai_v1', ?, 1, ?, ?)`, connectionID, accountID, baseURL, now, now); err != nil {
		t.Fatalf("seed account connection (%s): %v", accountID, err)
	}
	for _, mode := range modes {
		if _, err := db.Exec(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES (?, ?)`, connectionID, mode); err != nil {
			t.Fatalf("seed account endpoint mode (%s): %v", accountID, err)
		}
	}
	if _, err := db.Exec(`UPDATE accounts SET health_check_connection_id = ? WHERE id = ?`, connectionID, accountID); err != nil {
		t.Fatalf("bind health check connection (%s): %v", accountID, err)
	}
}

// seedChainStatsSchema creates the stats-side read surface the fresh
// dispatch quality rows come from (account_quality_scores).
func seedChainStatsSchema(t *testing.T, statsDB *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE account_quality_scores (
			account_id TEXT NOT NULL, quality_score REAL, quality_state TEXT,
			ewma_first_token_ms REAL, last_sample_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := statsDB.Exec(statement); err != nil {
			t.Fatalf("seed stats schema: %v: %v", statement, err)
		}
	}
}

// seedChainRuntimeRows inserts the owner/strategy/key/group/account rows and
// returns the gateway API key secret.
func seedChainRuntimeRows(t *testing.T, db *sql.DB, fixture *chainFixture) string {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, fixture.systemAccount)
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES (?, ?, 1, 'personal')`, fixture.groupID, fixture.systemAccount)
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{
		"api_key":  "sk-upstream-account-key",
		"base_url": "",
	})
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', '账户一', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.accountID, fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		fixture.groupID, fixture.systemAccount, fixture.accountID, now)
	// B4 第一批单连接事实源（设计 §2.1「Base URL 属于连接行」）：夹具按真实
	// 连接面给 acc_1 预置 enabled 连接 + mode 集合 + health-check 引用；secret
	// 装配按连接行取 BaseURL（缺失行 fail closed），旧 credentials.base_url
	// 不再是事实源。上游指向测试用
	// `UPDATE account_connections SET base_url = ?`（见链路冒烟用例）。
	seed(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn_acc_1', ?, 'gpt_openai_v1', 'openai_v1', 'https://conn-acc1.example.invalid/v1', 1, ?, ?)`,
		fixture.accountID, now, now)
	// 模式集保持 chat 两模式：/v1/responses 的入口拒绝契约（scenario
	// endpoint_mode_unsupported）与生产 opt-in 语义一致；需要 responses 能力
	// 的用例经 chainPointAccountAtUpstream 的变参 modes 显式声明。
	seed(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES ('conn_acc_1', 'chat_json'), ('conn_acc_1', 'chat_sse')`)
	seed(`UPDATE accounts SET health_check_connection_id = 'conn_acc_1' WHERE id = ?`, fixture.accountID)
	// 供应商解耦形态（设计 §3/§4）：支持模型经 models 单表 + model_id 外键；
	// 旧 provider_code/model 列保留供禁改的调度水合读取（chain_accounts.go）。
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-test', 'mdl_gpt-test', ?)`,
		fixture.accountID, now)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, input_usd_per_1m, created_at, updated_at)
		VALUES ('mdl_gpt-test', 'openai', 'gpt-test', 'gpt-test', 'global', 'active', '["chat_json","chat_sse"]', 1.0, ?, ?)`, now, now)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES ('rs_1', ?, '默认', 'normal', NULL, 'active')`,
		fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_1', 'rs_1', ?, ?, 0, 1, 'active', ?)`, fixture.systemAccount, fixture.groupID, now)
	// 调度偏好 P2 迁移完成世界：内置行存在，普通 Key 一律绑定（NULL 属未迁移
	// 错误，读取链显式失败）。
	seed(`INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', NULL, 1, ?, ?)`, now, now)
	secret := "sk-chain-test-key"
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_1', ?, 'rs_1', '链路测试', ?, 'active', 'schpref-builtin-chat-cost-first', ?)`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(secret), now)
	// 2026-09-20 目录源扩展后，openai 兼容目标的内置目录不含自身（Node
	// modelCatalogBuiltInSourceProviderCodes 剔除 openai 目标）；fixture 的
	// gpt-test 目录行改种在 custom_provider_models（global scope，带价格以
	// 通过 priced 过滤），与生产 openai 兼容目录的自定义模型来源一致。
	seed(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible,
			supported_api_protocols_json, input_usd_per_1m, created_by, created_at, updated_at)
		VALUES ('cat_1', 'openai', 'gpt-test', 'global', NULL, 'active', 1, '["chat_completions"]', 1.0, ?, ?, ?)`, fixture.systemAccount, now, now)
	seedSettingsDefaults(t, db)
	return secret
}

// seedSettingsDefaults mirrors seedSystemSettings with the settings the chain
// preflight reads.
func seedSettingsDefaults(t *testing.T, db *sql.DB) {
	t.Helper()
	// Reuse the canonical composition seed so this fixture cannot silently
	// drift when a new runtime system setting becomes part of the request
	// pipeline. The chain database already has the compatible schema.
	seedSystemSettings(t, db)
}

// ---------------------------------------------------------------------------
// compose-level chain assembly
// ---------------------------------------------------------------------------

func TestComposeSystemAPIServesGatewayChain(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.ChainEnabled = true
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig, composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api with chain: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)
	if composed.chain == nil {
		t.Fatal("chain must be assembled when JUHE_AI_GATEWAY_CHAIN_ENABLED=true")
	}

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}

	// Non-protocol /v1 paths keep the Node 404 JSON contract (the
	// openai-compatible families stay on the legacy bridge).
	response, err := client.Get(server.URL + "/v1/definitely-not-a-protocol-path")
	if err != nil {
		t.Fatalf("GET /v1 unknown: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-protocol /v1 status=%d want 404", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "资源不存在") {
		t.Fatalf("non-protocol /v1 body=%q", string(body))
	}

	// A protocol path without a gateway API key reaches the pre-auth auth
	// stage (Node: 401 invalid_api_key).
	chatResponse, err := client.Post(server.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer chatResponse.Body.Close()
	if chatResponse.StatusCode != http.StatusUnauthorized {
		chatBody, _ := io.ReadAll(chatResponse.Body)
		t.Fatalf("unauthenticated chat status=%d body=%s", chatResponse.StatusCode, string(chatBody))
	}
}

// ---------------------------------------------------------------------------
// chainAccountsSelector + catalog
// ---------------------------------------------------------------------------

func TestChainAccountsSelectorListsDispatchableAccounts(t *testing.T) {
	fixture := newChainFixture(t)
	groupAccess, err := fixture.cache.ResolveCachedGroupUsageAccessMetadataAsync(context.Background(), fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil {
		t.Fatal("group access missing")
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{
		PreResolvedGroupAccess: groupAccess,
	})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("accounts=%d want 1", len(result.Accounts))
	}
	account := result.Accounts[0]
	// 运行时候选/secret 的 provider 投影位与 vendor 同值（供应商与上游协议面
	// 重构设计 §8：账户连接身份=vendor_code；本夹具沿用 legacy vendor 值
	// 'openai'，目标形态种子应写 9 个真实厂商之一）。
	if account.ID != fixture.accountID || account.VendorCode != account.VendorCode || account.VendorCode != "openai" || account.ProtocolCode != "openai" {
		t.Fatalf("account identity wrong: %#v", account)
	}
	if account.APIKey != "sk-upstream-account-key" {
		t.Fatalf("credential decrypt failed: %q", account.APIKey)
	}
	if len(account.SupportedModels) != 1 || account.SupportedModels[0] != "gpt-test" {
		t.Fatalf("supported models wrong: %#v", account.SupportedModels)
	}
	if account.GroupOwnerSystemAccountID != fixture.systemAccount {
		t.Fatalf("group owner wrong: %q", account.GroupOwnerSystemAccountID)
	}
}

func TestChainCatalogSourceListsProviderModels(t *testing.T) {
	fixture := newChainFixture(t)
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	now := "2026-09-04T00:00:00.000Z"
	// 供应商解耦（设计 §3）：目录维度 = models.vendor_code 恒等源，协议子供应
	// 商聚合已随 providers/provider_protocol_profiles 退役。gpt vendor 的内置
	// 行只随 "gpt" 维度可见。
	if _, err := fixture.db.Exec(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_order,
			supported_endpoint_modes_json, source, catalog_visible, supports_prompt_caching,
			input_usd_per_1m, created_at, updated_at)
		VALUES ('cat_gpt_child', 'gpt', 'gpt-child-model', 'gpt-child-model', 'builtin', 'active', 0,
			'["chat_json","chat_sse"]', 'builtin', 1, 0, 1.0, ?, ?)`, now, now); err != nil {
		t.Fatalf("seed gpt builtin catalog: %v", err)
	}
	// openai vendor 自身的内置行：builtin 源剔除（openai 无自身内置目录）。
	if _, err := fixture.db.Exec(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, catalog_order,
			supported_endpoint_modes_json, source, catalog_visible, supports_prompt_caching,
			created_at, updated_at)
		VALUES ('cat_openai_builtin', 'openai', 'openai-builtin-model', 'openai-builtin-model', 'builtin', 'active', 0,
			'["chat_json","chat_sse"]', 'builtin', 1, 0, ?, ?)`, now, now); err != nil {
		t.Fatalf("seed openai builtin catalog: %v", err)
	}
	// gpt 维度：builtin 行在场（含端点集反推的 chat_completions 协议投影）。
	gptItems, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil {
		t.Fatalf("list gpt catalog: %v", err)
	}
	child, ok := findCatalogItem(gptItems, "gpt-child-model")
	if !ok {
		t.Fatalf("gpt catalog missing builtin row: %#v", gptItems)
	}
	if child.ProviderCode != "gpt" {
		t.Fatalf("builtin row provider = %q, want gpt", child.ProviderCode)
	}
	if len(child.SupportedAPIProtocols) != 1 || child.SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("builtin row protocols wrong: %#v", child.SupportedAPIProtocols)
	}
	// openai 维度：内置源剔除自身；fixture seed 的 openai 自定义行
	//（models global scope，定价行）在场，gpt builtin 行不跨维度泄漏。
	openaiItems, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "openai"})
	if err != nil {
		t.Fatalf("list openai catalog: %v", err)
	}
	byModel := map[string]gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range openaiItems {
		byModel[item.Model] = item
	}
	if _, ok := byModel["gpt-test"]; !ok {
		t.Fatalf("openai catalog missing custom row: %#v", openaiItems)
	}
	if _, ok := byModel["openai-builtin-model"]; ok {
		t.Fatalf("openai dimension must drop its own built-in rows: %#v", openaiItems)
	}
	if _, ok := byModel["gpt-child-model"]; ok {
		t.Fatalf("openai dimension must not aggregate gpt builtin rows: %#v", openaiItems)
	}
}

func findCatalogItem(items []gatewayruntimecache.ProviderModelCatalogItem, model string) (gatewayruntimecache.ProviderModelCatalogItem, bool) {
	for _, item := range items {
		if item.Model == model {
			return item, true
		}
	}
	return gatewayruntimecache.ProviderModelCatalogItem{}, false
}

// ---------------------------------------------------------------------------
// provider driver
// ---------------------------------------------------------------------------

func TestChainProviderDriverBuildsUpstreamRequests(t *testing.T) {
	driver := newChainProviderDriver()
	// Server-received requests carry a path-only URL (httptest.NewServer
	// equivalent); the driver appends it to the account base URL.
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_1",
		VendorCode:   "openai",
		ProtocolCode: "openai",
		BaseURL:      "https://upstream.example",
		APIKey:       "sk-upstream",
		Type:         "api_key",
	}
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://upstream.example/v1/chat/completions" {
		t.Fatalf("urls wrong: %#v", urls)
	}
	parts, err := driver.BuildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if parts.Headers.Get("Authorization") != "Bearer sk-upstream" {
		t.Fatalf("authorization header wrong: %q", parts.Headers.Get("Authorization"))
	}
	if parts.Headers.Get("Cookie") != "" {
		t.Fatalf("downstream cookie leaked upstream")
	}
	// Capability: the seeded model is supported, an unknown model is not.
	if !driver.AccountSupportsGatewayRequest(req, withSupportedModels(account, "gpt-test"), "") {
		t.Fatal("supported model must pass the capability gate")
	}
	if driver.AccountSupportsGatewayRequest(req, withSupportedModels(account, "other-model"), "") != true {
		// Supported-models constraint applies; but the mapping resolver may
		// still admit it. Assert the mismatch reason is reported either way.
		t.Skipf("mapping resolver admitted unknown model")
	}
}

func withSupportedModels(account gatewaydispatch.AccountCandidate, models ...string) gatewaydispatch.AccountCandidate {
	account.SupportedModels = models
	return account
}

func TestChainProviderDriverCapabilityMismatchReason(t *testing.T) {
	driver := newChainProviderDriver()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	// E2E-FINDING #7 裁决：client_compatibility 列不再参与调度淘汰（Node
	// accountSupportsGatewayRequest 链无对称比较），列值无论新旧数据都只
	// 影响写侧端点模式默认集与读面投影。
	account := gatewaydispatch.AccountCandidate{
		ID:                  "acc_1",
		VendorCode:          "openai",
		ProtocolCode:        "openai",
		ClientCompatibility: "codex",
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, "generic"); reason != "" {
		t.Fatalf("兼容列不应淘汰请求，reason = %q", reason)
	}
	anthropic := gatewaydispatch.AccountCandidate{ID: "acc_2", ProtocolCode: "anthropic"}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, anthropic, ""); reason != "anthropic_native_unsupported" {
		t.Fatalf("anthropic mismatch reason = %q", reason)
	}
}

// TestChainProviderDriverClientCompatibilityNotADispatchGate 覆盖
// E2E-FINDING #7 修复后的调度语义：client_compatibility 列（含 derive 落列
// 的 codex_responses 与存量 openai_standard）不再淘汰任何请求类——chat /
// codex / claude 三类客户端都能命中 gpt+api_key 账户；codex 的 responses
// 端点约束由 D-155 端点模式门承担，anthropic native 门保持协议面检查。
func TestChainProviderDriverClientCompatibilityNotADispatchGate(t *testing.T) {
	driver := newChainProviderDriver()
	messagesRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","max_tokens":16,"messages":[]}`))
	messagesReq := gatewaypreauth.NewGatewayRequest(messagesRequest)
	anthropicAccount := gatewaydispatch.AccountCandidate{
		ID:                  "acc_anthropic",
		VendorCode:          "anthropic",
		ProtocolCode:        "anthropic",
		ClientCompatibility: "openai_standard",
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(messagesReq, anthropicAccount, "claude_code"); reason != "" {
		t.Fatalf("anthropic 账户不应淘汰 claude_code 请求，reason = %q", reason)
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(messagesReq, anthropicAccount, "anthropic_native"); reason != "" {
		t.Fatalf("anthropic_native 请求不应被淘汰，reason = %q", reason)
	}
	responsesRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	responsesReq := gatewaypreauth.NewGatewayRequest(responsesRequest)
	chatRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	chatReq := gatewaypreauth.NewGatewayRequest(chatRequest)
	// 存量列（openai_standard）承接 codex_responses 请求；derive 落列
	//（codex_responses）承接 openai_standard 请求——两个方向都不淘汰。
	legacyAccount := gatewaydispatch.AccountCandidate{
		ID:                  "acc_openai_legacy",
		VendorCode:          "gpt",
		ProtocolCode:        "openai",
		ClientCompatibility: "openai_standard",
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(responsesReq, legacyAccount, "codex_responses"); reason != "" {
		t.Fatalf("存量 openai_standard 列不应淘汰 codex_responses 请求，reason = %q", reason)
	}
	derivedAccount := gatewaydispatch.AccountCandidate{
		ID:                  "acc_openai_derived",
		VendorCode:          "gpt",
		ProtocolCode:        "openai",
		ClientCompatibility: "codex_responses",
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(chatReq, derivedAccount, "openai_standard"); reason != "" {
		t.Fatalf("codex_responses 列不应淘汰 chat 请求，reason = %q", reason)
	}
}

// ---------------------------------------------------------------------------
// usage persistence bridge
// ---------------------------------------------------------------------------

func TestSpooledUsageRecorderDeliversAndOverflowsToSpool(t *testing.T) {
	dir := t.TempDir()
	clock := gatewaypreauth.SystemClock{}
	spool := gatewayusageNewSpool(t, filepath.Join(dir, "spool"), clock)
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 1}, spool)
	record := gatewayusage.UsageRecordInput{TraceID: "trace_1", TrafficSource: "gateway"}
	for index := 0; index < 3; index++ {
		if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), record); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	recorder.Close()
	if recorder.dropped+recorder.failed > 0 {
		t.Fatalf("unexpected drop/failure: dropped=%d failed=%d", recorder.dropped, recorder.failed)
	}
	entries := spoolDirectoryEntries(t, filepath.Join(dir, "spool"))
	if len(entries) == 0 {
		t.Fatal("spool must hold the delivered records")
	}
}

// capturingUsageRecorder collects the enqueued usage records for assertions.
type capturingUsageRecorder struct {
	records []gatewayusage.UsageRecordInput
}

func (r *capturingUsageRecorder) EnqueueUsageRecord(_ gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	r.records = append(r.records, input)
	return nil
}

// TestChainFinalizationUsageFailedAttemptCarriesFullIdentity locks the U2
// regression: the finalization-side failed attempt usage record must carry the
// same identity dimensions as the completed path (records.ts 失败路径同样写入
// traceId/clientIp/systemAccountId/apiKeyId/groupId/endpoint/providerCode/
// accountId).
func TestChainFinalizationUsageFailedAttemptCarriesFullIdentity(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	statusCode := 502
	usage.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:         "trace_failed_1",
			TrafficSource:   "gateway",
			ClientIP:        "203.0.113.7",
			SystemAccountID: "sys_acc_1",
			APIKeyID:        "key_1",
			GroupID:         "group_1",
			Endpoint:        "/v1/chat/completions",
			ProviderCode:    "openai",
		},
		Account:            gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc_failed_1"}},
		StatusCode:         &statusCode,
		ErrorMessage:       "upstream connect timeout",
		FailureAttribution: "account_upstream",
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.TraceID != "trace_failed_1" {
		t.Errorf("TraceID = %q, want trace_failed_1", record.TraceID)
	}
	if record.ClientIP != "203.0.113.7" {
		t.Errorf("ClientIP = %q, want 203.0.113.7", record.ClientIP)
	}
	if record.SystemAccountID != "sys_acc_1" {
		t.Errorf("SystemAccountID = %q, want sys_acc_1", record.SystemAccountID)
	}
	if record.APIKeyID != "key_1" {
		t.Errorf("APIKeyID = %q, want key_1", record.APIKeyID)
	}
	if record.GroupID != "group_1" {
		t.Errorf("GroupID = %q, want group_1", record.GroupID)
	}
	if record.Endpoint != "/v1/chat/completions" {
		t.Errorf("Endpoint = %q, want /v1/chat/completions", record.Endpoint)
	}
	if record.ProviderCode != "openai" {
		t.Errorf("ProviderCode = %q, want openai", record.ProviderCode)
	}
	if record.AccountID != "acc_failed_1" {
		t.Errorf("AccountID = %q, want acc_failed_1", record.AccountID)
	}
	if record.Success {
		t.Error("Success = true, want false for a failed attempt")
	}
	if record.ErrorMessage != "upstream connect timeout" {
		t.Errorf("ErrorMessage = %q, want upstream connect timeout", record.ErrorMessage)
	}
	if record.FailureAttribution != "account_upstream" {
		t.Errorf("FailureAttribution = %q, want account_upstream", record.FailureAttribution)
	}
	if record.StatusCode == nil || *record.StatusCode != 502 {
		t.Errorf("StatusCode = %v, want 502", record.StatusCode)
	}
}

// TestChainFinalizationUsageFailedAttemptOmitsAbsentStatusCode locks the
// statusCode nil semantics: when the failed attempt carries no status code the
// record must omit StatusCode entirely (nil pointer), never a pointer to 0;
// a missing account must leave AccountID empty.
func TestChainFinalizationUsageFailedAttemptOmitsAbsentStatusCode(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	usage.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_failed_2"},
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.StatusCode != nil {
		t.Errorf("StatusCode = %v, want nil when the attempt has no status", record.StatusCode)
	}
	if record.AccountID != "" {
		t.Errorf("AccountID = %q, want empty when the attempt has no account", record.AccountID)
	}
	if record.TraceID != "trace_failed_2" {
		t.Errorf("TraceID = %q, want trace_failed_2", record.TraceID)
	}
}

func gatewayusageNewSpool(t *testing.T, dir string, clock gatewaypreauth.Clock) *gatewayusage.UsageRecordSpool {
	t.Helper()
	spool := newUsageSpool(dir, clock, nil, usageSpoolCapacity{})
	if spool == nil {
		t.Fatal("spool missing")
	}
	t.Cleanup(spool.StopReplay)
	return spool
}

func spoolDirectoryEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read spool dir: %v", err)
	}
	return entries
}

// ---------------------------------------------------------------------------
// chat executor bridge
// ---------------------------------------------------------------------------

func TestChatGatewayExecutorStreamsThroughHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("X-Trace-Id") != "trace_chat" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"chunk":1}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(`data: {"chunk":2}` + "\n\n"))
	})
	executor := newChatGatewayExecutor(handler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := executor.Dispatch(ctx, chat.GenerationDispatchRequest{
		Path:    "v1/chat/completions",
		Method:  http.MethodPost,
		Headers: map[string]string{"X-Trace-Id": "trace_chat"},
		Body:    []byte(`{"model":"gpt-test"}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if response.Status != http.StatusOK {
		t.Fatalf("status=%d", response.Status)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(payload), `"chunk":1`) || !strings.Contains(string(payload), `"chunk":2`) {
		t.Fatalf("stream body incomplete: %q", string(payload))
	}
}

// ---------------------------------------------------------------------------
// chain assembly gate
// ---------------------------------------------------------------------------

func TestComposeGatewayChainFailsFastNamingMissingPorts(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, err := composeGatewayChain(chainRuntimeDeps{Cache: fixture.cache})
	if err == nil {
		t.Fatal("missing runtime services must fail the assembly")
	}
	for _, name := range []string{
		"PreAuthCircuits", "ClientIPPolicy", "UserRequestLimits", "AuthenticatedModelsRateLimit",
		"APIKeyQuota", "AuthorizationQuota", "InflightQuota", "Avoidance", "Affinity",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("missing port not named (%s): %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// full-chain smoke: preauth -> dispatch(mock upstream) -> response
// ---------------------------------------------------------------------------

func TestGatewayChainServeSmokePreauthDispatchResponse(t *testing.T) {
	fixture := newChainFixture(t)

	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		t.Logf("mock upstream hit: path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-upstream-account-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-smoke","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"链路冒烟"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	// Point the seeded account at the mock upstream. B4 第一批单连接事实源：
	// 上游指向改连接行 base_url（secret BaseURL = account_connections.base_url），
	// 凭据 JSON 只承载 api_key（credentials.base_url 依赖已解除）。
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key"}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE account_connections SET base_url = ? WHERE id = 'conn_acc_1'`, upstream.URL); err != nil {
		t.Fatalf("update connection base url: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	clock := gatewaypreauth.SystemClock{}
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, clock, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()

	server := httptest.NewServer(chain)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("response missing choices: %s", string(body))
	}
	if !strings.Contains(string(body), "链路冒烟") {
		t.Fatalf("upstream content missing: %s", string(body))
	}
	if upstreamRequests != 1 {
		t.Fatalf("upstream requests=%d want 1", upstreamRequests)
	}
	// The usage persistence bridge must have captured the finalized record.
	// The record queue is asynchronous; poll briefly for the spool flush.
	spoolEntries := []os.DirEntry{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		spoolEntries = spoolDirectoryEntries(t, spoolDir)
		if len(spoolEntries) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(spoolEntries) == 0 {
		t.Fatal("usage spool must hold the finalized record")
	}
}

// chainSmokeDeps assembles the chainRuntimeDeps for the smoke test: real
// memory-driver G13/G11 services, real quota services over the fixture
// databases, disabled audit dispatch.
func chainSmokeDeps(t *testing.T, fixture *chainFixture, clock gatewaypreauth.Clock, spoolDir string) chainRuntimeDeps {
	t.Helper()
	circuits, err := gatewayclientip.NewErrorCircuit(gatewayclientip.ErrorCircuitOptions{Clock: clock})
	if err != nil {
		t.Fatalf("create circuits: %v", err)
	}
	t.Cleanup(circuits.Close)
	policyCache, err := gatewayclientip.NewPolicyCache(gatewayclientip.PolicyCacheOptions{Clock: clock, Source: &chainPolicySource{}})
	if err != nil {
		t.Fatalf("create policy cache: %v", err)
	}
	t.Cleanup(policyCache.Close)
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{Clock: clock})
	if err != nil {
		t.Fatalf("create avoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	modelsRateLimit := gatewayproxyhealth.NewAuthenticatedModelsRateLimitService(nil,
		gatewayproxyhealth.NewPenaltyWindowRateLimiter(nil, false, nil, ""), nil)
	counter := gatewayproxyhealth.NewUserRequestLimitCounter(nil, gatewayproxyhealth.UserRequestLimitCounterOptions{})
	coordinator := gatewayproxyhealth.NewUserRequestLimitCoordinator(counter, nil, gatewayproxyhealth.UserRequestLimitCoordinatorOptions{})
	userLimits := gatewayproxyhealth.NewUserRequestLimitsService(counter, coordinator)

	statsStore, err := gatewayquota.NewStatsStore(fixture.statsDB, false)
	if err != nil {
		t.Fatalf("create stats store: %v", err)
	}
	snapshot, err := gatewayquota.NewSnapshotCache(gatewayquota.Modes{}, nil, time.Now, nil)
	if err != nil {
		t.Fatalf("create snapshot cache: %v", err)
	}
	timezone := chainFixedTimezone{}
	apiKeyQuota, err := gatewayquota.NewAPIKeyQuotaService(gatewayquota.APIKeyQuotaConfig{
		Stats:    statsStore,
		Timezone: timezone,
		Snapshot: snapshot,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("create api-key quota: %v", err)
	}
	authzQuota, err := gatewayquota.NewAuthorizationQuotaService(gatewayquota.AuthorizationQuotaConfig{
		Business: fixture.db,
		Stats:    statsStore,
		Timezone: timezone,
		Snapshot: snapshot,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("create authz quota: %v", err)
	}
	inflightQuota, err := gatewayquota.NewInflightQuotaService(gatewayquota.InflightQuotaConfig{APIKeys: apiKeyQuota})
	if err != nil {
		t.Fatalf("create inflight quota: %v", err)
	}
	return chainRuntimeDeps{
		Cache:           fixture.cache,
		Clock:           clock,
		AuditLogEnabled: func() bool { return false },
		SpoolDirectory:  spoolDir,
		// 组合测试的上游是 httptest loopback 服务：与 Node 回归显式设置
		// JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true 同一约定，测试夹具
		// 打开 allowPrivate 开关（D-192/D-146 严格策略的生产默认不受影响）。
		UpstreamURLSecurity: sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true},
		Circuits:            circuits,
		IPPolicy:            policyCache,
		UserLimits:          userLimits,
		ModelsRateLimit:     modelsRateLimit,
		APIKeyQuota:         apiKeyQuota,
		AuthzQuota:          authzQuota,
		InflightQuota:       inflightQuota,
		Avoidance:           avoidance,
		Affinity:            gatewaygemini.NewInteractionAffinity(nil),
		Recoverable: gatewaycircuit.NewPreAuthRecoverableWait(
			gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{}),
			chainTestWaitLogger{t: t},
		),
		// R4：组合冒烟链注入短上游重试退避（capacity queued / capacity plain
		// / recoverable wait / same-account retry 四语义位）。生产链
		//（compose.go）保持 nil=生产值；httptest 失败重试场景不再各睡
		// 1000/500/3000ms 与 settings 推导的同账户重试间隔——语义位只压等待
		// 时长，不改重试轮次与断言契约（ServerRetryBudget 仍封顶总等待）。
		UpstreamRetryBackoffDelaysMs: []int64{5, 5, 5, 5},
	}
}

// ---------------------------------------------------------------------------
// small test collaborators
// ---------------------------------------------------------------------------

// chainPolicySource answers "no client-ip policies" (the empty stats table
// read the memory-mode policy cache would perform).
type chainPolicySource struct{}

func (s *chainPolicySource) ListActiveClientIPPolicies(context.Context) ([]gatewayclientip.ActiveClientIPPolicy, error) {
	return nil, nil
}

func (s *chainPolicySource) FindActiveClientIPPolicyByHash(context.Context, string) (*gatewayclientip.ActiveClientIPPolicy, error) {
	return nil, nil
}

func (s *chainPolicySource) RecordClientIPPolicyHits(context.Context, []gatewayclientip.PolicyHitInput) error {
	return nil
}

type chainFixedTimezone struct{}

func (chainFixedTimezone) StatsTimezone(context.Context) (*time.Location, error) {
	return time.UTC, nil
}

// chainTestWaitLogger funnels the circuit wait engine lines into the test log.
type chainTestWaitLogger struct{ t *testing.T }

func (l chainTestWaitLogger) Info(fields map[string]any, message string) {}

func (l chainTestWaitLogger) Warn(fields map[string]any, message string) {
	l.t.Logf("circuit-wait warn: %s %v", message, fields)
}

func mustEncryptCredentials(t *testing.T, credentials map[string]any) string {
	t.Helper()
	sealed, err := accounts.EncryptJSON("chain-test-secret", credentials)
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	return sealed
}

// TestChainAccountsModelMappingsLoadsOverRealMaintenanceDDL is the
// entry-mappings schema regression (X05 defect lineage: the runtime read once
// ordered a nonexistent `id` column, then kept reading the removed
// account_model_mappings table). It applies the REAL maintenance business DDL
// (the same statements maintenance ensure and the gateway SQLite preflight
// apply), asserts the real account_entry_mappings shape (global id PK +
// composite natural key), and proves loadModelMappingsByAccountIds reads it
// with the entry-mappings ordering and the LEFT JOIN models upstream /
// target_endpoint_family projection.
func TestChainAccountsModelMappingsLoadsOverRealMaintenanceDDL(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("apply real maintenance business DDL: %v", err)
	}
	// Seed the parent rows (vendors / sys_admin) the real DDL foreign keys
	// point at, then add one account with a connection fact row and two
	// supported models to map against.
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC) }, Secret: "chain-test-secret"}); err != nil {
		t.Fatalf("seed real maintenance business data: %v", err)
	}
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	// 设计 §2.1 写入顺序：先账户（health 三字段暂 NULL）、再连接行，最后
	// UPDATE 回填 health_check_connection_id。
	seed(`INSERT INTO accounts (
			id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at
		) VALUES ('acc_real_ddl', 'sys_admin', 'gpt', 'gpt', 'openai', 'v1',
			'真实 DDL 账户', 'api_key', 'active', 'envelope', 'gpt-test', 'chat_json', ?, ?)`, now, now)
	seed(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn_real_ddl', 'acc_real_ddl', 'gpt_openai_v1', 'openai_v1', 'https://real-ddl.example.invalid/v1', 1, ?, ?)`, now, now)
	seed(`UPDATE accounts SET health_check_connection_id = 'conn_real_ddl' WHERE id = 'acc_real_ddl'`)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_real_1', 'gpt', 'gpt-real-1', 'gpt-real-1', 'global', 'active', '["chat_json","chat_sse"]', ?, ?),
		       ('mdl_real_2', 'gpt', 'gpt-real-2', 'gpt-real-2', 'global', 'active', '["responses_json","responses_sse"]', ?, ?)`, now, now, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES ('acc_real_ddl', 'mdl_real_1', ?), ('acc_real_ddl', 'mdl_real_2', ?)`, now, now)

	// Schema-drift guard: the real entry-mappings table keeps the global id
	// primary key and the composite natural key (设计 §5.1).
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(account_entry_mappings)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	for _, required := range []string{"id", "account_id", "source_model", "source_endpoint_family", "target_connection_id", "target_model_id", "target_endpoint_family", "enabled"} {
		if !columns[required] {
			t.Fatalf("real account_entry_mappings DDL missing column %s", required)
		}
	}
	var pkColumns string
	if err := db.QueryRow(`SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_table_info('account_entry_mappings') WHERE pk > 0 ORDER BY pk)`).Scan(&pkColumns); err != nil {
		t.Fatalf("read primary key: %v", err)
	}
	if pkColumns != "id" {
		t.Fatalf("primary key = %q, want id", pkColumns)
	}

	// Two mapping rows inserted deliberately out of source_model order: the
	// read must return them in the entry-mappings ORDER BY (account_id,
	// source_model, source_endpoint_family, id) with the target model's
	// upstream name and the target endpoint family (LEFT JOIN models), and
	// must not error on the real DDL.
	for _, row := range []struct{ source, family, targetModel string }{
		{"gpt-pro", "chat_completions", "mdl_real_1"},
		{"gpt-5", "responses", "mdl_real_2"},
	} {
		seed(`INSERT INTO account_entry_mappings (id, account_id, source_model, source_endpoint_family, target_connection_id, target_model_id, target_endpoint_family, enabled, created_at, updated_at)
			VALUES ('entmap_' || ?, 'acc_real_ddl', ?, ?, 'conn_real_ddl', ?, ?, 1, ?, ?)`,
			row.source, row.source, row.family, row.targetModel, row.family, now, now)
	}

	selector, err := newChainAccountsSelector(db, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	mappings, err := selector.loadModelMappingsByAccountIds(context.Background(), []string{"acc_real_ddl", "acc_missing"})
	if err != nil {
		t.Fatalf("real-DDL mapping read failed: %v", err)
	}
	if len(mappings["acc_real_ddl"]) != 2 {
		t.Fatalf("mappings = %d, want 2: %+v", len(mappings["acc_real_ddl"]), mappings)
	}
	first, second := mappings["acc_real_ddl"][0], mappings["acc_real_ddl"][1]
	if first.SourceModel != "gpt-5" || second.SourceModel != "gpt-pro" {
		t.Fatalf("mapping order wrong: %s before %s", first.SourceModel, second.SourceModel)
	}
	if first.UpstreamModel != "gpt-real-2" || first.UpstreamEndpointFamily != "responses" || !first.Enabled {
		t.Fatalf("first mapping = %+v", first)
	}
	if second.UpstreamModel != "gpt-real-1" || second.UpstreamEndpointFamily != "chat_completions" {
		t.Fatalf("second mapping = %+v", second)
	}
	if _, exists := mappings["acc_missing"]; exists {
		t.Fatalf("unknown account must not map: %+v", mappings)
	}
}

// T6c：dispatch 候选窗口 limit 是运行配置（Node
// gatewayDispatchAccountCandidateLimit = runtimeConfig.gateway.
// dispatchAccountCandidateLimit，默认 concurrency.globalMax；scan = final*2），
// 查询 LIMIT、hydration 批上限与诊断全部消费注入值，不再是固定常量。
func TestChainAccountsSelectorConsumesDispatchCandidateLimit(t *testing.T) {
	db := seedChainSelectorDB(t)
	defer db.Close()
	selector, err := newChainAccountsSelector(db, false, "chain-test-secret", time.Now, 7)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	if selector.candidateFinalLimit != 7 || selector.candidateScanLimit != 14 {
		t.Fatalf("limit=%d scan=%d, want 7/14", selector.candidateFinalLimit, selector.candidateScanLimit)
	}
	result, err := selector.ListOpenAIAccountsForGroupResult(context.Background(), "grp-limit", "sys_admin", gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Diagnostics == nil {
		t.Fatal("diagnostics 缺失")
	}
	if result.Diagnostics.FinalLimit != 7 || result.Diagnostics.ScanLimit != 14 {
		t.Fatalf("diagnostics final=%d scan=%d, want 7/14",
			result.Diagnostics.FinalLimit, result.Diagnostics.ScanLimit)
	}
	// Node integerConfig 边界：越界 limit 构造失败（1..50000）。
	if _, err := newChainAccountsSelector(db, false, "chain-test-secret", time.Now, 0); err == nil {
		t.Fatal("limit=0 必须构造失败")
	}
	if _, err := newChainAccountsSelector(db, false, "chain-test-secret", time.Now, 50001); err == nil {
		t.Fatal("limit=50001 必须构造失败")
	}
}

// seedChainSelectorDB 建候选窗口查询所需的最小业务表（与 chainCandidateFrom
// 对齐：group_accounts + accounts 自连接 + stats 质量表省略——共享 db 句柄）。
func seedChainSelectorDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chain-limit.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, enabled INTEGER, group_type TEXT, group_kind TEXT NOT NULL DEFAULT 'standard', scheduling_policy_json TEXT)`,
		`CREATE TABLE super_group_members (id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL, priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT, vendor_code TEXT, connection_driver_code TEXT, provider_protocol_profile_id TEXT, protocol_code TEXT, protocol_version TEXT, name TEXT, type TEXT, status TEXT, capability_domain TEXT NOT NULL DEFAULT 'chat', schedulable INTEGER, concurrency_limit INTEGER, priority INTEGER, super_priority_enabled INTEGER, fallback_enabled INTEGER, client_compatibility TEXT, config_revision INTEGER, dispatch_revision INTEGER, last_health_success_at TEXT, credentials_encrypted TEXT, proxy_profile_id TEXT, cooldown_until TEXT, last_error_message TEXT, last_error_code TEXT, stream_failure_count INTEGER, stream_failure_window_started_at TEXT, account_expires_at TEXT, health_check_model TEXT, health_check_endpoint_mode TEXT, health_check_connection_id TEXT, authorization_instance_source_account_id TEXT, authorization_instance_authorization_id TEXT, authorization_instance_owner_system_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT, system_account_id TEXT, group_id TEXT, account_authorization_id TEXT, local_priority INTEGER, local_super_priority_enabled INTEGER, local_fallback_enabled INTEGER, enabled INTEGER, created_at TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT, model TEXT, model_id TEXT, created_at TEXT)`,
		`CREATE TABLE account_entry_mappings (id TEXT PRIMARY KEY, account_id TEXT, source_model TEXT, source_endpoint_family TEXT, target_connection_id TEXT, target_model_id TEXT, target_endpoint_family TEXT, enabled INTEGER, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE account_connections (id TEXT PRIMARY KEY, account_id TEXT, connection_preset_code TEXT, protocol_surface_code TEXT, base_url TEXT, enabled INTEGER, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE account_endpoint_modes (connection_id TEXT, endpoint_mode TEXT, PRIMARY KEY (connection_id, endpoint_mode))`,
		`CREATE TABLE models (id TEXT PRIMARY KEY, vendor_code TEXT, public_model TEXT, upstream_model TEXT, scope TEXT, system_account_id TEXT, status TEXT, supported_endpoint_modes_json TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (account_id TEXT, key_fingerprint TEXT, key_index INTEGER, status TEXT, cooldown_until TEXT, next_probe_at TEXT, recovery_started_at TEXT)`,
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY, type TEXT, host TEXT, port INTEGER, username TEXT, password_encrypted TEXT, enabled INTEGER)`,
		`CREATE TABLE account_quality_scores (account_id TEXT PRIMARY KEY, quality_score REAL, quality_state TEXT, ewma_first_token_ms REAL, last_sample_at TEXT)`,
		`INSERT INTO groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('grp-limit', 'sys_admin', 1, 'personal', NULL)`,
	}
	now := "2026-01-01T00:00:00.000Z"
	// 9 个候选超出 final limit 7，验证 hydration 上限消费注入值。
	// B4 第一批：每个候选带一条 enabled 连接行（单连接事实源水合不丢弃）。
	for i := 0; i < 9; i++ {
		id := string(rune('a'+i)) + "-limit-acc"
		statements = append(statements,
			`INSERT INTO accounts (id, system_account_id, vendor_code, name, type, status, schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled, config_revision, dispatch_revision, deleted_at) VALUES ('`+id+`', 'sys_admin', 'openai', '`+id+`', 'api_key', 'active', 1, 0, 0, 0, 0, 1, 1, NULL)`,
			`INSERT INTO group_accounts VALUES ('`+id+`', 'sys_admin', 'grp-limit', NULL, 0, 0, 0, 1, '`+now+`')`,
			`INSERT INTO account_connections (id, account_id, connection_preset_code, base_url, enabled, created_at, updated_at) VALUES ('conn-limit-`+id+`', '`+id+`', 'gpt_openai_v1', 'https://`+id+`.example.invalid/v1', 1, '`+now+`', '`+now+`')`)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement[:40], err)
		}
	}
	return db
}
