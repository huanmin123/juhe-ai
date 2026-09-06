// Test fixtures for the aipublic slice: the SQLite schema (the union of the
// tables the mounted stores + the aipublic-local queries touch) and the
// recording operation-log sink.
package aipublic

import (
	"net/http"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func kernelForTest() *kernel.Kernel {
	return kernel.New(kernel.Options{CompressionDisabled: true})
}

// recordingAIPublicSink captures the account write logs for assertions.
type recordingAIPublicSink struct {
	mutex   sync.Mutex
	Entries []authsys.OperationLogEntry
}

func (s *recordingAIPublicSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Entries = append(s.Entries, entry)
}

// aipublicSchema mirrors the maintenance business schema subset this slice
// touches: the external source/token auth tables, the system account store,
// and the group/route-strategy/api-key/account families.
var aipublicSchema = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS external_integration_sources (id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL, scopes_json TEXT NOT NULL, rate_limits_json TEXT, expires_at TEXT, notes TEXT, last_used_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS external_integration_source_tokens (id TEXT PRIMARY KEY, source_ref_id TEXT NOT NULL, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, token_secret_encrypted TEXT, token_prefix TEXT NOT NULL, token_suffix TEXT NOT NULL, status TEXT NOT NULL, scopes_json TEXT NOT NULL, expires_at TEXT, last_used_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, revoked_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, parent_code TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, base_url TEXT NOT NULL, default_health_check_model TEXT NOT NULL, account_types_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, test_status TEXT NOT NULL DEFAULT 'unknown', latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		route_strategy_id TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL,
		key_suffix TEXT NOT NULL,
		key_secret_encrypted TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		is_default INTEGER NOT NULL DEFAULT 0,
		purpose TEXT NOT NULL DEFAULT 'general',
		expires_at TEXT,
		quota_limits_json TEXT,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		last_used_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
	`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		source_type TEXT NOT NULL,
		source_id TEXT NOT NULL,
		window_hours INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`,
	`CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
		api_key_id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_attempt_at TEXT,
		last_blocked_reason TEXT,
		last_error_message TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 1,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
		system_account_id TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL,
		protocol_code TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending_test',
		credentials_encrypted TEXT NOT NULL,
		credential_fingerprint TEXT,
		credential_mask TEXT NOT NULL DEFAULT '',
		oauth_access_token_expires_at TEXT,
		oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
		proxy_profile_id TEXT,
		concurrency_limit INTEGER NOT NULL DEFAULT 5000,
		priority INTEGER NOT NULL DEFAULT 0,
		super_priority_enabled INTEGER NOT NULL DEFAULT 0,
		fallback_enabled INTEGER NOT NULL DEFAULT 0,
		client_compatibility TEXT NOT NULL DEFAULT 'openai_standard',
		schedulable INTEGER NOT NULL DEFAULT 1,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		notes TEXT,
		account_expires_at TEXT,
		last_used_at TEXT,
		cooldown_until TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		last_error_trace_id TEXT,
		health_check_model TEXT NOT NULL DEFAULT '',
		health_check_endpoint_mode TEXT NOT NULL DEFAULT 'chat_json',
		health_check_failure_count INTEGER NOT NULL DEFAULT 0,
		health_check_failure_started_at TEXT,
		cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
		cooldown_retest_observation_started_at TEXT,
		cooldown_retest_generation TEXT,
		cooldown_retest_last_at TEXT,
		cooldown_retest_last_status_code INTEGER,
		last_health_check_at TEXT,
		last_health_success_at TEXT,
		last_health_check_status_code INTEGER,
		last_health_check_error_code TEXT,
		last_health_check_error_message TEXT,
		last_health_check_trace_id TEXT,
		temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1,
		next_health_check_at TEXT,
		balance_query_enabled INTEGER NOT NULL DEFAULT 0,
		balance_query_next_refresh_at TEXT,
		balance_query_config_json TEXT NOT NULL DEFAULT '{}',
		authorization_instance_source_account_id TEXT,
		authorization_instance_authorization_id TEXT,
		deleted_at TEXT,
		deleted_by TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL`,
	`CREATE TABLE IF NOT EXISTS group_accounts (
		system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL,
		account_authorization_id TEXT, local_priority INTEGER NOT NULL DEFAULT 0,
		local_super_priority_enabled INTEGER NOT NULL DEFAULT 0, local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (group_id, account_id)
	)`,
	`CREATE TABLE IF NOT EXISTS account_supported_models (
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, model)
	)`,
	`CREATE TABLE IF NOT EXISTS account_model_mappings (
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, source_model TEXT NOT NULL,
		source_endpoint_family TEXT NOT NULL, upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (account_id, source_model, source_endpoint_family)
	)`,
	`CREATE TABLE IF NOT EXISTS account_tags (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique ON account_tags(system_account_id, name)`,
	`CREATE TABLE IF NOT EXISTS account_tag_bindings (
		account_id TEXT NOT NULL, tag_id TEXT NOT NULL, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, tag_id)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_terms (
		account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, term TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, term)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_documents (
		account_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, normalized_name TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	// Health-input tombstone tables the delete path writes in the same
	// transaction (accounts.Delete enqueueDeletedAccountHealthTombstones;
	// Node account-delete-cleanup.repository.ts:259-275).
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL CHECK (current_version >= 1), reserved_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (event_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, input_version INTEGER NOT NULL CHECK (input_version >= 1), event_kind TEXT NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')), reason TEXT NOT NULL, config_revision INTEGER NOT NULL CHECK (config_revision >= 1), dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1), status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')), claim_token TEXT, claimed_until TEXT, attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0), available_at TEXT NOT NULL, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE (account_id, input_version))`,
	// Authorization chain tables the delete path revokes in-transaction
	// (accounts.Delete revokeAccountAuthorizationsForDeletedResource; Node
	// account-delete-cleanup.repository.ts:145-148).
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_lock_states (
		account_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
		lock_state TEXT NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
		lock_death_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
		lock_retry_interval_seconds INTEGER NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
		incident_id TEXT,
		generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
		incident_started_at TEXT,
		deadline_at TEXT,
		original_status TEXT,
		provenance TEXT,
		next_retry_at_ms INTEGER,
		lease_id TEXT,
		lease_until_ms INTEGER,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_circuit_outbox (
		event_id TEXT PRIMARY KEY,
		projection_key TEXT NOT NULL,
		dedupe_key TEXT NOT NULL,
		event_type TEXT NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
		account_id TEXT NOT NULL,
		account_runtime_key TEXT NOT NULL,
		circuit_scope_key TEXT,
		incident_id TEXT,
		transition_id TEXT NOT NULL,
		dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
		generation INTEGER,
		ledger_revision INTEGER,
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
		available_at_ms INTEGER NOT NULL,
		claim_token TEXT,
		claimed_by TEXT,
		claim_until_ms INTEGER,
		attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
		last_error_class TEXT,
		acknowledged_at_ms INTEGER,
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL,
		FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
		UNIQUE (projection_key, dedupe_key)
	)`,
}
