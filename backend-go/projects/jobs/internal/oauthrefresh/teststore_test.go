package oauthrefresh

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newTestStore builds a PG-backed Store (PLAN-20261024T051925945Z 测试基建迁移：
// oauthrefresh 切片) over a pgtestkit isolated database. The minimal business
// schema mirrors the production PG DDL in maintenance
// pg_schema_business_tables.go (juhe_business schema, column names/types/
// nullability/defaults byte-aligned; CHECK/FK/UNIQUE 不复刻——circuitstore
// 先例同风格). Store 装配走生产 PG 臂（StorePostgres：juhe_business schema
// 限定 + ?→$n bind 重写），与生产 jobs 进程同一 SQL 路径。所有时间列在生产
// PG 中就是 text（isoMillis 字符串语义），断言逐字节保持，无时钟重钉。
func newTestStore(t *testing.T) (*Store, *sql.DB, func()) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		_, err := db.Exec(oauthRefreshFixtureSchema)
		return err
	})
	store, err := OpenStore(db, cryptoTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { _ = db.Close() }
	t.Cleanup(cleanup)
	return store, db, cleanup
}

const oauthRefreshFixtureSchema = `CREATE TABLE juhe_business.accounts (
	id text PRIMARY KEY,
	config_revision integer NOT NULL DEFAULT 1,
	dispatch_revision bigint NOT NULL DEFAULT 1,
	circuit_projection_revision bigint NOT NULL DEFAULT 0,
	system_account_id text NOT NULL,
	vendor_code text,
	connection_driver_code text NOT NULL,
	oauth_type text,
	protocol_code text NOT NULL,
	protocol_version text NOT NULL,
	name text NOT NULL,
	type text NOT NULL,
	status text NOT NULL DEFAULT 'pending_test',
	credentials_encrypted text NOT NULL,
	credential_fingerprint text,
	credential_mask text NOT NULL DEFAULT '',
	oauth_access_token_expires_at text,
	oauth_refresh_token_present integer NOT NULL DEFAULT 0,
	proxy_profile_id text,
	concurrency_limit integer NOT NULL DEFAULT 5000,
	priority integer NOT NULL DEFAULT 0,
	super_priority_enabled integer NOT NULL DEFAULT 0,
	fallback_enabled integer NOT NULL DEFAULT 0,
	schedulable integer NOT NULL DEFAULT 1,
	availability_schedule_json text,
	availability_schedule_next_check_at text,
	notes text,
	account_expires_at text,
	last_used_at text,
	cooldown_until text,
	last_error_code text,
	last_error_message text,
	last_error_trace_id text,
	cooldown_retest_failure_count integer NOT NULL DEFAULT 0,
	cooldown_retest_observation_started_at text,
	cooldown_retest_generation text,
	cooldown_retest_last_at text,
	cooldown_retest_last_status_code integer,
	temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1,
	expedited_recovery_enabled integer NOT NULL DEFAULT 0,
	capability_domain text NOT NULL DEFAULT 'chat',
	health_check_connection_id text,
	health_check_model text,
	health_check_endpoint_mode text,
	last_health_check_at text,
	next_health_check_at text,
	last_health_success_at text,
	health_check_failure_count integer NOT NULL DEFAULT 0,
	health_check_failure_started_at text,
	last_health_check_status_code integer,
	last_health_check_error_code text,
	last_health_check_error_message text,
	last_health_check_trace_id text,
	stream_failure_count integer NOT NULL DEFAULT 0,
	stream_failure_window_started_at text,
	balance_query_enabled integer NOT NULL DEFAULT 0,
	balance_query_config_json text NOT NULL DEFAULT '{}',
	balance_query_next_refresh_at text,
	authorization_instance_source_account_id text,
	authorization_instance_authorization_id text,
	authorization_instance_owner_system_account_id text,
	deleted_at text,
	deleted_by text,
	created_at text NOT NULL,
	updated_at text NOT NULL
);

CREATE TABLE juhe_business.api_keys (
	id text PRIMARY KEY,
	system_account_id text NOT NULL,
	route_strategy_id text NOT NULL,
	name text NOT NULL,
	description text,
	 key_hash text NOT NULL,
	key_prefix text NOT NULL,
	key_suffix text NOT NULL,
	key_secret_encrypted text NOT NULL,
	status text NOT NULL DEFAULT 'active',
	is_default integer NOT NULL DEFAULT 0,
	      purpose text NOT NULL DEFAULT 'general',
	expires_at text,
	quota_limits_json text,
	availability_schedule_json text,
	availability_schedule_next_check_at text,
	last_used_at text,
	created_at text NOT NULL,
	updated_at text NOT NULL
);

CREATE TABLE juhe_business.api_key_schedule_status_events (
	event_key text PRIMARY KEY,
	api_key_id text NOT NULL,
	status text NOT NULL,
	executed_at text NOT NULL
);

CREATE TABLE juhe_business.account_schedule_status_events (
	event_key text PRIMARY KEY,
	account_id text NOT NULL,
	status text NOT NULL,
	executed_at text NOT NULL
);

CREATE TABLE juhe_business.account_quality_enforcements (
	account_id text PRIMARY KEY,
	system_account_id text NOT NULL,
	 enforcement_id text NOT NULL,
	generation bigint NOT NULL DEFAULT 1,
	state text NOT NULL DEFAULT 'active',
	action text NOT NULL,
	trigger_run_id text NOT NULL,
	config_source text NOT NULL DEFAULT 'manual',
	config_source_id text,
	policy_revision integer NOT NULL,
	profile text NOT NULL DEFAULT 'quick',
	penalty_threshold integer NOT NULL DEFAULT 70,
	recovery_interval_minutes integer NOT NULL DEFAULT 10,
	recovery_model text,
	account_config_revision integer NOT NULL,
	before_status text NOT NULL,
	after_status text NOT NULL,
	fallback_was_enabled integer NOT NULL DEFAULT 0,
	super_priority_was_enabled integer NOT NULL DEFAULT 0,
	started_at text NOT NULL,
	recovery_due_at text,
	recovery_lease_owner text,
	recovery_lease_until text,
	last_recovery_run_id text,
	cleared_at text,
	created_at text NOT NULL,
	updated_at text NOT NULL
);

CREATE TABLE juhe_business.account_health_jobs_input_versions (
	account_id text PRIMARY KEY,
	current_version integer NOT NULL,
	reserved_at text NOT NULL
);

CREATE TABLE juhe_business.account_health_jobs_input_outbox (
	event_id text PRIMARY KEY,
	account_id text NOT NULL,
	input_version integer NOT NULL,
	event_kind text NOT NULL,
	reason text NOT NULL,
	config_revision integer NOT NULL,
	dispatch_revision bigint NOT NULL,
	status text NOT NULL,
	claim_token text,
	claimed_until text,
	attempt_count integer NOT NULL DEFAULT 0,
	available_at text NOT NULL,
	last_error text,
	created_at text NOT NULL,
	updated_at text NOT NULL
);
CREATE TABLE account_health_jobs_input_outbox (
	event_id text PRIMARY KEY,
	account_id text NOT NULL,
	input_version integer NOT NULL,
	event_kind text NOT NULL,
	reason text NOT NULL,
	config_revision integer NOT NULL,
	dispatch_revision bigint NOT NULL,
	status text NOT NULL,
	claim_token text,
	claimed_until text,
	attempt_count integer NOT NULL DEFAULT 0,
	available_at text NOT NULL,
	last_error text,
	created_at text NOT NULL,
	updated_at text NOT NULL
);

CREATE TABLE juhe_business.resource_authorization_grants (
	id text PRIMARY KEY,
	resource_type text NOT NULL,
	resource_id text NOT NULL,
	resource_owner_system_account_id text NOT NULL,
	grantee_type text NOT NULL,
	grantee_system_account_id text,
	grantee_team_id text,
	scope text NOT NULL DEFAULT 'use',
	status text NOT NULL DEFAULT 'active',
	remark text,
	expires_at text,
	limits_json text,
	created_by text NOT NULL,
	created_at text NOT NULL,
	revoked_by text,
	revoked_at text,
	updated_at text NOT NULL
);
`

func seedOpenAIOAuthAccount(t *testing.T, db *sql.DB, id string, credentials map[string]any, now time.Time) {
	t.Helper()
	seedAccountRow(t, db, accountRowSeed{
		ID: id, ProviderCode: ProviderGPT,
		Type: "oauth", Credentials: credentials, Now: now,
	})
}

type accountRowSeed struct {
	ID               string
	ProviderCode     string
	Type             string
	Status           string
	LastErrorCode    string
	Credentials      map[string]any
	Now              time.Time
	ExpiresAtDerived bool
}

func seedAccountRow(t *testing.T, db *sql.DB, seed accountRowSeed) {
	t.Helper()
	if seed.Status == "" {
		seed.Status = "active"
	}
	source := stringCredential(seed.Credentials, "refresh_token")
	if source == "" {
		source = stringCredential(seed.Credentials, "access_token")
	}
	sealed, err := EncryptJSON(cryptoTestSecret, seed.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := sql.NullString{}
	if value, ok := seed.Credentials["expires_at"]; ok {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if stringCredential(seed.Credentials, "refresh_token") != "" {
		refreshPresent = 1
	}
	lastErrorCode := sql.NullString{}
	if seed.LastErrorCode != "" {
		lastErrorCode = sql.NullString{String: seed.LastErrorCode, Valid: true}
	}
	_, err = db.Exec(`INSERT INTO juhe_business.accounts (id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask, oauth_access_token_expires_at, oauth_refresh_token_present, last_error_code, config_revision, updated_at, system_account_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1, $15, 'sys_admin', $15)`,
		seed.ID, seed.ProviderCode, seed.ProviderCode, "openai", "v1", "账户-"+seed.ID, seed.Type, seed.Status,
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent,
		lastErrorCode, isoMillis(seed.Now))
	if err != nil {
		t.Fatal(err)
	}
}

func readAccountCredentials(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	var sealed string
	if err := db.QueryRow(`SELECT credentials_encrypted FROM juhe_business.accounts WHERE id = $1`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{}
	if err := DecryptJSON(cryptoTestSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	return credentials
}

func readAccountRow(t *testing.T, db *sql.DB, id string) (status, lastErrorCode string, configRevision int64, oauthExpiresAt sql.NullString, refreshPresent int64) {
	t.Helper()
	var lastError sql.NullString
	if err := db.QueryRow(`SELECT status, last_error_code, config_revision, oauth_access_token_expires_at, oauth_refresh_token_present
		FROM juhe_business.accounts WHERE id = $1`, id).Scan(&status, &lastError, &configRevision, &oauthExpiresAt, &refreshPresent); err != nil {
		t.Fatal(err)
	}
	return status, lastError.String, configRevision, oauthExpiresAt, refreshPresent
}

type recordingExchanger struct {
	mu      sync.Mutex
	request TokenHTTPRequest
	calls   int
	respond func(call int, request TokenHTTPRequest) (TokenHTTPResponse, error)
}

func (r *recordingExchanger) Do(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.request = request
	if r.respond == nil {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-default","refresh_token":"rt-default","expires_in":3600}`}, nil
	}
	return r.respond(r.calls, request)
}

func (r *recordingExchanger) lastRequest() TokenHTTPRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.request
}

func (r *recordingExchanger) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
