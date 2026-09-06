package oauthrefresh

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestStore builds a sqlite-backed Store with the minimum business schema
// the J4 job family touches. The credential columns and derived guards mirror
// the Node business migrations the jobs SQL reads and writes.
func newTestStore(t *testing.T) (*Store, *sql.DB, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauthrefresh.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = MEMORY; PRAGMA busy_timeout = 5000;`); err != nil {
		t.Fatal(err)
	}
	schema := `
CREATE TABLE providers (
	code TEXT PRIMARY KEY,
	enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE provider_protocol_profiles (
	id TEXT PRIMARY KEY,
	provider_code TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	protocol_code TEXT NOT NULL,
	protocol_version TEXT NOT NULL
);
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
	provider_code TEXT NOT NULL,
	provider_protocol_profile_id TEXT NOT NULL,
	protocol_code TEXT NOT NULL DEFAULT 'openai',
	protocol_version TEXT NOT NULL DEFAULT 'v1',
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	credentials_encrypted TEXT NOT NULL,
	credential_fingerprint TEXT NOT NULL DEFAULT '',
	credential_mask TEXT NOT NULL DEFAULT '',
	proxy_profile_id TEXT,
	concurrency_limit INTEGER NOT NULL DEFAULT 0,
	priority INTEGER NOT NULL DEFAULT 0,
	super_priority_enabled INTEGER NOT NULL DEFAULT 0,
	fallback_enabled INTEGER NOT NULL DEFAULT 0,
	client_compatibility TEXT NOT NULL DEFAULT '',
	schedulable INTEGER NOT NULL DEFAULT 1,
	account_expires_at TEXT,
	cooldown_until TEXT,
	last_error_code TEXT,
	last_error_message TEXT,
	health_check_model TEXT,
	health_check_endpoint_mode TEXT,
	config_revision INTEGER NOT NULL DEFAULT 1,
	dispatch_revision INTEGER NOT NULL DEFAULT 1,
	authorization_instance_source_account_id TEXT,
	oauth_access_token_expires_at TEXT,
	oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
	availability_schedule_json TEXT,
	availability_schedule_next_check_at TEXT,
	authorization_instance_authorization_id TEXT,
	deleted_at TEXT,
	updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE api_keys (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'active',
	availability_schedule_json TEXT,
	availability_schedule_next_check_at TEXT,
	updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE api_key_schedule_status_events (
	event_key TEXT PRIMARY KEY,
	api_key_id TEXT NOT NULL,
	status TEXT NOT NULL,
	executed_at TEXT NOT NULL
);
CREATE TABLE account_schedule_status_events (
	event_key TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	status TEXT NOT NULL,
	executed_at TEXT NOT NULL
);
CREATE TABLE account_quality_enforcements (
	account_id TEXT NOT NULL,
	state TEXT NOT NULL,
	action TEXT NOT NULL
);
CREATE TABLE account_health_jobs_input_versions (
	account_id TEXT PRIMARY KEY,
	current_version INTEGER NOT NULL CHECK (current_version >= 1),
	reserved_at TEXT NOT NULL
);
CREATE TABLE account_health_jobs_input_outbox (
	event_id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	input_version INTEGER NOT NULL CHECK (input_version >= 1),
	event_kind TEXT NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')),
	reason TEXT NOT NULL,
	config_revision INTEGER NOT NULL CHECK (config_revision >= 1),
	dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
	status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
	claim_token TEXT,
	claimed_until TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
	available_at TEXT NOT NULL,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (account_id, input_version)
);
CREATE TABLE resource_authorization_grants (
	id TEXT PRIMARY KEY,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	owner_system_account_id TEXT NOT NULL,
	grantee_type TEXT NOT NULL,
	grantee_id TEXT NOT NULL,
	status TEXT NOT NULL,
	revoked_at TEXT,
	revoked_by TEXT,
	created_by TEXT NOT NULL DEFAULT '',
	expires_at TEXT,
	updated_at TEXT NOT NULL DEFAULT ''
);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(db, StoreSQLite, cryptoTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { _ = db.Close() }
	t.Cleanup(cleanup)
	return store, db, cleanup
}

func seedProviderProfiles(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO providers (code, enabled) VALUES ('gpt', 1), ('anthropic', 1), ('gemini', 1), ('xai', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, enabled, protocol_code, protocol_version) VALUES
		('profile_gpt_openai_v1', 'gpt', 1, 'openai', 'v1'),
		('profile_anthropic_anthropic_v1', 'anthropic', 1, 'anthropic', 'v1'),
		('profile_gemini_native_v1beta', 'gemini', 1, 'gemini', 'v1beta'),
		('profile_xai_openai_v1', 'xai', 1, 'openai', 'v1')`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedOpenAIOAuthAccount(t *testing.T, db *sql.DB, id string, credentials map[string]any, now time.Time) {
	t.Helper()
	seedAccountRow(t, db, accountRowSeed{
		ID: id, ProviderCode: "gpt", ProfileID: "profile_gpt_openai_v1",
		Type: "oauth", Credentials: credentials, Now: now,
	})
}

type accountRowSeed struct {
	ID               string
	ProviderCode     string
	ProfileID        string
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
	_, err = db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
		credentials_encrypted, credential_fingerprint, credential_mask, oauth_access_token_expires_at, oauth_refresh_token_present,
		last_error_code, config_revision, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		seed.ID, seed.ProviderCode, seed.ProfileID, "openai", "v1", "账户-"+seed.ID, seed.Type, seed.Status,
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent,
		lastErrorCode, isoMillis(seed.Now))
	if err != nil {
		t.Fatal(err)
	}
}

func readAccountCredentials(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	var sealed string
	if err := db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, id).Scan(&sealed); err != nil {
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
		FROM accounts WHERE id = ?`, id).Scan(&status, &lastError, &configRevision, &oauthExpiresAt, &refreshPresent); err != nil {
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
