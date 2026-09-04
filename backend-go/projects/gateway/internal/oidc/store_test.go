// store_test.go covers the oauth_* persistence slice (store.go) against an
// in-memory SQLite database (strict mock: no network, injected clock). The
// DDL mirrors the migrated business schema in
// projects/maintenance/internal/schema/sqlite_schema.go; PostgreSQL behavior
// is covered at the SQL-translation layer (table/bind) since the pg dialect
// shares every statement through s.bind().
package oidc

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// nodeMillis converts the Node-style millisecond constants into Durations
// (the implementation stores lifetimes as millisecond ints; tests need the
// matching time.Duration).
func nodeMillis(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

const oidcTestSecret = "p04-oidc-test-secret"

const oidcTestIssuer = "https://oidc.example.com"

// fakeClock is the injected clock shared by store and route tests.
type fakeClock struct {
	mu    sync.Mutex
	nowMs int64
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{nowMs: start.UnixMilli()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.UnixMilli(c.nowMs)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowMs += d.Milliseconds()
}

func (c *fakeClock) Set(ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowMs = ms
}

// oidcTestDDL mirrors the migrated sqlite business schema for exactly the
// tables the oidc store touches (columns trimmed to the consumed surface).
const oidcTestDDL = `
CREATE TABLE IF NOT EXISTS system_accounts (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  description TEXT,
  role TEXT NOT NULL DEFAULT 'user',
  status TEXT NOT NULL DEFAULT 'active',
  password_hash TEXT NOT NULL DEFAULT '',
  must_change_password INTEGER NOT NULL DEFAULT 0,
  image_generation_enabled INTEGER NOT NULL DEFAULT 0,
  ai_account_limit INTEGER,
  request_limits_json TEXT,
  last_login_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS system_sessions (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_clients (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  client_type TEXT NOT NULL CHECK (client_type IN ('public', 'confidential')),
  client_secret_hash TEXT,
  client_secret_ciphertext TEXT,
  redirect_uris_json TEXT NOT NULL,
  allowed_scopes_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_grants (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  scopes_json TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_authorization_transactions (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  scopes_json TEXT NOT NULL,
  state_ciphertext TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  csrf_hash TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  completed_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
  id TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
  client_id TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  consumed_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_access_tokens (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  client_id TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  issued_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  replaced_at TEXT,
  successor_token_id TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_authorization_code_oidc_contexts (
  code_id TEXT PRIMARY KEY,
  nonce_ciphertext TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_signing_keys (
  id TEXT PRIMARY KEY,
  kid TEXT NOT NULL UNIQUE,
  private_key_ciphertext TEXT NOT NULL,
  public_jwk_json TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
  created_at TEXT NOT NULL,
  retired_at TEXT
);
CREATE TABLE IF NOT EXISTS oauth_device_authorizations (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  device_code_hash TEXT NOT NULL UNIQUE,
  user_code TEXT NOT NULL UNIQUE,
  verification_uri TEXT NOT NULL,
  scopes_json TEXT NOT NULL,
  nonce_ciphertext TEXT,
  expires_at TEXT NOT NULL,
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 1 AND 60),
  last_polled_at TEXT,
  csrf_hash TEXT,
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'consumed', 'expired')),
  system_account_id TEXT,
  approved_at TEXT,
  denied_at TEXT,
  consumed_at TEXT,
  created_at TEXT NOT NULL
);
`

type oidcStoreEnv struct {
	db           *sql.DB
	store        *Store
	clock        *fakeClock
	accountID    string
	publicID     string
	confID       string
	confSecret   string
	sessionToken string
	// publicRedirect / confRedirect are the registered redirect URIs.
	publicRedirect  string
	loopbackPort    string
	deviceScopesCSV string
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustQueryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %q: %v", query, err)
	}
	return out
}

func mustQueryString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	values := mustQueryStrings(t, db, query, args...)
	if len(values) != 1 {
		t.Fatalf("query %q: want exactly 1 row, got %d", query, len(values))
	}
	return values[0]
}

// newStoreEnv builds an in-memory SQLite store with a fixed clock and the
// base fixture: one active account, one public client, one confidential
// client and one live browser session.
func newStoreEnv(t *testing.T) *oidcStoreEnv {
	t.Helper()
	clock := newFakeClock(time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(t.Name())
	db, err := sql.Open("sqlite", "file:oidc-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(oidcTestDDL); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	store, err := NewStore(db, false, clock.Now, oidcTestSecret)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	env := &oidcStoreEnv{
		db:              db,
		store:           store,
		clock:           clock,
		accountID:       "acc-1",
		publicID:        "juhe_public_client",
		confID:          "juhe_conf_client",
		confSecret:      "jcs_confidential_test_secret",
		sessionToken:    "browser-session-token-1",
		publicRedirect:  "https://app.example.com/callback",
		loopbackPort:    "http://127.0.0.1:7777/callback",
		deviceScopesCSV: "openid profile juhe:profile.read juhe:profile.write",
	}
	env.seedBase(t)
	return env
}

func (e *oidcStoreEnv) seedBase(t *testing.T) {
	t.Helper()
	now := isoMillis(e.clock.Now())
	mustExec(t, e.db, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, 'alice', 'Alice', 'admin', 'active', 'hash', ?, ?)`, e.accountID, now, now)
	mustExec(t, e.db, `INSERT INTO oauth_clients (id, client_id, display_name, client_type, client_secret_hash,
		client_secret_ciphertext, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES ('cl-public', ?, 'Public App', 'public', NULL, NULL, ?, ?, 'active', ?, ?)`,
		e.publicID,
		`["`+e.publicRedirect+`","`+e.loopbackPort+`"]`,
		`["openid","profile","juhe:profile.read","juhe:profile.write"]`, now, now)
	mustExec(t, e.db, `INSERT INTO oauth_clients (id, client_id, display_name, client_type, client_secret_hash,
		client_secret_ciphertext, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES ('cl-conf', ?, 'Conf App', 'confidential', ?, NULL, ?, ?, 'active', ?, ?)`,
		e.confID, hashSecret(e.confSecret),
		`["`+e.publicRedirect+`"]`,
		`["openid","profile","juhe:profile.read"]`, now, now)
	mustExec(t, e.db, `INSERT INTO system_sessions (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
		VALUES ('sess-1', ?, ?, ?, ?, ?)`,
		e.accountID, hashSecret(e.sessionToken),
		isoMillis(e.clock.Now().Add(24*time.Hour)), now, now)
}

func (e *oidcStoreEnv) insertGrant(t *testing.T, id string, expiresAt time.Time) {
	t.Helper()
	now := isoMillis(e.clock.Now())
	mustExec(t, e.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?)`,
		id, e.publicID, e.accountID, `["openid","profile"]`, isoMillis(expiresAt), now)
}

func (e *oidcStoreEnv) insertAccessTokenRow(t *testing.T, token, grantID string, issuedAt, expiresAt time.Time) {
	t.Helper()
	mustExec(t, e.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)`,
		"tok-"+token, hashSecret(token), e.publicID, grantID,
		isoMillis(issuedAt), isoMillis(expiresAt), isoMillis(issuedAt))
}

// authorizeQuery builds a valid consent-start query for the public client.
func (e *oidcStoreEnv) authorizeQuery(overrides map[string]string) string {
	values := map[string]string{
		"response_type":         "code",
		"client_id":             e.publicID,
		"redirect_uri":          e.publicRedirect,
		"scope":                 "openid profile",
		"state":                 "st-123",
		"code_challenge":        pkceChallengeOf(pkceTestVerifier),
		"code_challenge_method": "S256",
		"nonce":                 "n-abc",
	}
	for key, value := range overrides {
		values[key] = value
	}
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+urlQueryEscape(value))
	}
	return "?" + strings.Join(parts, "&")
}

func iso(t time.Time) string { return isoMillis(t) }

// seedSigningKey inserts an active signing key row directly. Route-level
// tests use it to pin a deterministic kid/created_at (the Node runtime has
// always a key provisioned by the time protocol traffic arrives); the
// bootstrap-from-empty-table path itself is covered by
// TestFindActiveSigningKeyEmptyTable and TestEnsureSigningKeyBootstrapsEmptyTable.
func (e *oidcStoreEnv) seedSigningKey(t *testing.T, kid string, createdAt time.Time) {
	t.Helper()
	material, err := CreateSigningKeyMaterial(oidcTestSecret, kid)
	if err != nil {
		t.Fatalf("create signing key material: %v", err)
	}
	jwkJSON, err := json.Marshal(material.PublicJWK)
	if err != nil {
		t.Fatalf("marshal jwk: %v", err)
	}
	mustExec(t, e.db, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES (?, ?, ?, ?, 'active', ?, NULL)`, "key-"+kid, kid, material.PrivateKeyCiphertext, string(jwkJSON), isoMillis(createdAt))
}

// seedAuthorizationCode inserts a grant+code pair with Node-correct
// lifetimes. It remains the precise tool for exchange-path fixtures that need
// custom bindings, while CreateAuthorizationCode's own output is pinned by
// TestCreateAuthorizationCodeStoresHashedRows.
func (e *oidcStoreEnv) seedAuthorizationCode(t *testing.T, clientID, accountID string, scopes []string,
	redirect, challenge, nonce string, grantTTL, codeTTL time.Duration) string {
	t.Helper()
	now := e.clock.Now()
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		t.Fatalf("marshal scopes: %v", err)
	}
	grantID := "grant-seed-" + newUUIDv4()
	codeID := "code-seed-" + newUUIDv4()
	code := "seeded-" + randomBase64URLBytes(16)
	mustExec(t, e.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?)`,
		grantID, clientID, accountID, string(scopesJSON), isoMillis(now.Add(grantTTL)), isoMillis(now))
	mustExec(t, e.db, `INSERT INTO oauth_authorization_codes (id, code_hash, client_id, grant_id, redirect_uri, code_challenge, expires_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		codeID, hashSecret(code), clientID, grantID, redirect, challenge, isoMillis(now.Add(codeTTL)), isoMillis(now))
	if nonce != "" {
		ciphertext, err := EncryptOidcValue(oidcTestSecret, map[string]string{"nonce": nonce})
		if err != nil {
			t.Fatalf("encrypt nonce: %v", err)
		}
		mustExec(t, e.db, `INSERT INTO oauth_authorization_code_oidc_contexts (code_id, nonce_ciphertext, created_at) VALUES (?, ?, ?)`,
			codeID, ciphertext, isoMillis(now))
	}
	return code
}

// ---------------------------------------------------------------------------
// Constructor and SQL dialect translation.
// ---------------------------------------------------------------------------

func TestNewStoreRequiresDatabase(t *testing.T) {
	if _, err := NewStore(nil, false, nil, oidcTestSecret); err == nil || err.Error() != "oidc store requires a database" {
		t.Fatalf("NewStore(nil) error = %v, want %q", err, "oidc store requires a database")
	}
	// nil now falls back to time.Now without panicking.
	store, err := NewStore(&sql.DB{}, false, nil, oidcTestSecret)
	if err != nil {
		t.Fatalf("NewStore with empty db: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil store")
	}
}

func TestStoreBindAndTable(t *testing.T) {
	env := newStoreEnv(t)
	if got := env.store.table("oauth_clients"); got != "oauth_clients" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := env.store.bind("SELECT * FROM t WHERE a = ? AND b = ?"); got != "SELECT * FROM t WHERE a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	pgStore, err := NewStore(env.db, true, env.clock.Now, oidcTestSecret)
	if err != nil {
		t.Fatalf("NewStore pg: %v", err)
	}
	if got := pgStore.table("oauth_clients"); got != "juhe_business.oauth_clients" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pgStore.bind("SELECT * FROM t WHERE a = ? AND b = ? OR c IN (?, ?)"); got != "SELECT * FROM t WHERE a = $1 AND b = $2 OR c IN ($3, $4)" {
		t.Fatalf("pg bind = %q", got)
	}
	// '?' inside string literals is translated too — the store only ever
	// builds queries programmatically, so this documents the translation rule.
	if got := pgStore.bind("no placeholders"); got != "no placeholders" {
		t.Fatalf("pg bind passthrough = %q", got)
	}
}

func TestISOAndTimestampHelpers(t *testing.T) {
	utc := time.Date(2026, 1, 5, 10, 0, 0, 123_400_000, time.UTC)
	if got := isoMillis(utc); got != "2026-01-05T10:00:00.123Z" {
		t.Fatalf("isoMillis = %q", got)
	}
	// A +08:00 input is accepted and canonicalized to UTC (Node toISOString).
	if got, err := requiredTimestamp("2026-01-05T18:00:00.123+08:00"); err != nil || got != "2026-01-05T10:00:00.123Z" {
		t.Fatalf("requiredTimestamp offset = %q, %v", got, err)
	}
	ms, err := requiredTimestampMS("2026-01-05T10:00:00.123Z")
	if err != nil || ms != utc.UnixMilli() {
		t.Fatalf("requiredTimestampMS = %d, %v", ms, err)
	}
	if _, err := requiredTimestampMS("not-a-time"); err == nil || err.Error() != "not-a-time 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("requiredTimestampMS error = %v", err)
	}
	if _, err := requiredTimestampMS("2026-01-05 10:00:00"); err == nil || err.Error() != "2026-01-05 10:00:00 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("requiredTimestampMS bare error = %v", err)
	}
}

// TestRequiredTimestampKeepsMilliseconds pins the fix 4a regression: the
// canonical form is Node's Date.toISOString(), which always carries the
// millisecond segment — RFC3339Nano used to drop the trailing ".000" and to
// desynchronize lexicographic expires_at comparisons.
func TestRequiredTimestampKeepsMilliseconds(t *testing.T) {
	cases := []struct{ input, want string }{
		{"2026-01-05T10:00:00Z", "2026-01-05T10:00:00.000Z"},
		{"2026-01-05T10:00:00.5Z", "2026-01-05T10:00:00.500Z"},
		{"2026-01-05T10:00:00.123456789Z", "2026-01-05T10:00:00.123Z"},
		{"2026-01-05T18:00:00+08:00", "2026-01-05T10:00:00.000Z"},
	}
	for _, tc := range cases {
		got, err := requiredTimestamp(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("requiredTimestamp(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	// Round trip: isoMillis output parses back to the identical canonical form.
	canonical := "2026-01-05T10:00:00.000Z"
	if got, err := requiredTimestamp(canonical); err != nil || got != canonical {
		t.Fatalf("requiredTimestamp round trip = %q, %v", got, err)
	}
	if _, err := requiredTimestamp("not-a-time"); err == nil || err.Error() != "not-a-time 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("requiredTimestamp error = %v", err)
	}
}

// TestIntervalEscalationExpr pins the slow-down escalation dialect: SQLite
// (the Node source dialect) uses scalar MIN(a, b), PostgreSQL needs LEAST.
func TestIntervalEscalationExpr(t *testing.T) {
	env := newStoreEnv(t)
	if got := env.store.intervalEscalationExpr(); got != "MIN(interval_seconds + 5, 60)" {
		t.Fatalf("sqlite escalation expr = %q", got)
	}
	pgStore, err := NewStore(env.db, true, env.clock.Now, oidcTestSecret)
	if err != nil {
		t.Fatalf("NewStore pg: %v", err)
	}
	if got := pgStore.intervalEscalationExpr(); got != "LEAST(interval_seconds + 5, 60)" {
		t.Fatalf("pg escalation expr = %q", got)
	}
}

func TestParseStringArray(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`[]`, []string{}},
		{`{"not":"array"}`, []string{}},
		{`broken`, []string{}},
		{``, []string{}},
	}
	for _, tc := range cases {
		got := parseStringArray(tc.input)
		if len(got) != len(tc.want) {
			t.Fatalf("parseStringArray(%q) = %v, want %v", tc.input, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseStringArray(%q) = %v, want %v", tc.input, got, tc.want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Clients, accounts, browser sessions.
// ---------------------------------------------------------------------------

func TestFindClient(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	client, err := env.store.FindClient(ctx, env.publicID)
	if err != nil {
		t.Fatalf("FindClient: %v", err)
	}
	if client == nil || client.ClientID != env.publicID || client.ClientType != "public" || client.Status != "active" {
		t.Fatalf("FindClient public = %+v", client)
	}
	if len(client.RedirectUris) != 2 || client.RedirectUris[0] != env.publicRedirect {
		t.Fatalf("FindClient redirectUris = %v", client.RedirectUris)
	}
	if client.ClientSecretHash != nil {
		t.Fatalf("public client must not carry a secret hash: %v", *client.ClientSecretHash)
	}
	conf, err := env.store.FindClient(ctx, env.confID)
	if err != nil || conf == nil || conf.ClientSecretHash == nil || *conf.ClientSecretHash != hashSecret(env.confSecret) {
		t.Fatalf("FindClient confidential = %+v, err=%v", conf, err)
	}
	if missing, err := env.store.FindClient(ctx, "juhe_unknown"); err != nil || missing != nil {
		t.Fatalf("FindClient unknown = %+v, err=%v", missing, err)
	}
}

func TestFindSystemAccountProfile(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	profile, err := env.store.FindSystemAccountProfile(ctx, env.accountID)
	if err != nil || profile == nil || profile.AccountID != env.accountID || profile.Username != "alice" || profile.DisplayName != "Alice" {
		t.Fatalf("FindSystemAccountProfile = %+v, err=%v", profile, err)
	}
	mustExec(t, env.db, `UPDATE system_accounts SET status = 'disabled' WHERE id = ?`, env.accountID)
	if disabled, err := env.store.FindSystemAccountProfile(ctx, env.accountID); err != nil || disabled != nil {
		t.Fatalf("disabled account profile = %+v, err=%v", disabled, err)
	}
	if missing, err := env.store.FindSystemAccountProfile(ctx, "acc-none"); err != nil || missing != nil {
		t.Fatalf("missing account profile = %+v, err=%v", missing, err)
	}
}

func TestFindSessionByToken(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	session, err := env.store.FindSessionByToken(ctx, env.sessionToken)
	if err != nil || session == nil {
		t.Fatalf("FindSessionByToken live = %+v, err=%v", session, err)
	}
	if session.SessionID != "sess-1" || session.AccountID != env.accountID || session.Role != "admin" {
		t.Fatalf("live session = %+v", session)
	}

	// Expired session (expires_at == now is already expired).
	expired := isoMillis(env.clock.Now().Add(-time.Second))
	mustExec(t, env.db, `UPDATE system_sessions SET expires_at = ? WHERE id = 'sess-1'`, expired)
	if session, err := env.store.FindSessionByToken(ctx, env.sessionToken); err != nil || session != nil {
		t.Fatalf("expired session = %+v, err=%v", session, err)
	}

	// Inactive account.
	future := isoMillis(env.clock.Now().Add(24 * time.Hour))
	mustExec(t, env.db, `UPDATE system_sessions SET expires_at = ? WHERE id = 'sess-1'`, future)
	mustExec(t, env.db, `UPDATE system_accounts SET status = 'disabled' WHERE id = ?`, env.accountID)
	if session, err := env.store.FindSessionByToken(ctx, env.sessionToken); err != nil || session != nil {
		t.Fatalf("disabled account session = %+v, err=%v", session, err)
	}

	// Unknown token.
	if session, err := env.store.FindSessionByToken(ctx, "wrong-token"); err != nil || session != nil {
		t.Fatalf("unknown token session = %+v, err=%v", session, err)
	}
}

// ---------------------------------------------------------------------------
// Signing keys: lazy weekly rotation and JWKS retention.
// ---------------------------------------------------------------------------

// TestFindActiveSigningKeyEmptyTable pins the Node contract for an empty
// oauth_signing_keys table: findActiveOidcSigningKey returns undefined, which
// the Go port must express as (nil, nil) — leaking sql.ErrNoRows here used to
// break the EnsureSigningKey bootstrap of fresh deployments.
func TestFindActiveSigningKeyEmptyTable(t *testing.T) {
	env := newStoreEnv(t)
	key, err := env.store.FindActiveSigningKey(context.Background())
	if key != nil || err != nil {
		t.Fatalf("empty table FindActiveSigningKey = %+v, err=%v, want nil, nil (Node: undefined)", key, err)
	}
}

func TestEnsureSigningKeyBootstrapsEmptyTable(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	// Node: ensureOidcSigningKey on an empty table creates the first key.
	key, err := env.store.EnsureSigningKey(ctx)
	if err != nil || key == nil {
		t.Fatalf("EnsureSigningKey on empty table = %+v, err=%v (Node creates the first key)", key, err)
	}
	if !strings.HasPrefix(key.Kid, "oidc_") || key.Status != "active" {
		t.Fatalf("bootstrapped key = %+v", key)
	}
	if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
		t.Fatalf("signing key rows after bootstrap = %d, want 1", rows)
	}
	// The bootstrapped key is the active one and the ensure becomes a no-op.
	found, err := env.store.FindActiveSigningKey(ctx)
	if err != nil || found == nil || found.Kid != key.Kid {
		t.Fatalf("FindActiveSigningKey after bootstrap = %+v, err=%v", found, err)
	}
	again, err := env.store.EnsureSigningKey(ctx)
	if err != nil || again == nil || again.Kid != key.Kid {
		t.Fatalf("no-op ensure after bootstrap = %+v, err=%v", again, err)
	}
	if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
		t.Fatalf("signing key rows after no-op ensure = %d, want 1", rows)
	}
}

func TestEnsureSigningKeyNoOpAndRotation(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	env.seedSigningKey(t, "kid-first", env.clock.Now())

	first, err := env.store.FindActiveSigningKey(ctx)
	if err != nil || first == nil {
		t.Fatalf("seeded key = %+v, err=%v", first, err)
	}
	if !strings.HasPrefix(first.Kid, "kid-") || first.Status != "active" {
		t.Fatalf("first key = %+v", first)
	}
	var payload struct {
		PrivateKeyPem string `json:"privateKeyPem"`
	}
	if err := DecryptOidcValue(oidcTestSecret, first.PrivateKeyCiphertext, &payload); err != nil {
		t.Fatalf("decrypt first key ciphertext: %v", err)
	}
	if !strings.Contains(payload.PrivateKeyPem, "BEGIN PRIVATE KEY") {
		t.Fatalf("private key pem = %.40q", payload.PrivateKeyPem)
	}
	if first.PublicJWK["kty"] != "RSA" || first.PublicJWK["use"] != "sig" || first.PublicJWK["alg"] != "RS256" {
		t.Fatalf("first key jwk = %v", first.PublicJWK)
	}

	// A young key short-circuits: same key, still one row.
	again, err := env.store.EnsureSigningKey(ctx)
	if err != nil || again == nil || again.Kid != first.Kid {
		t.Fatalf("second EnsureSigningKey = %+v, err=%v", again, err)
	}
	if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
		t.Fatalf("signing key rows after no-op ensure = %d", rows)
	}

	// Past the weekly boundary the lazy rotation retires and replaces.
	env.clock.Advance(nodeMillis(SigningKeyRotationIntervalMs) + time.Millisecond)
	rotated, err := env.store.EnsureSigningKey(ctx)
	if err != nil || rotated == nil || rotated.Kid == first.Kid {
		t.Fatalf("rotated key = %+v, err=%v", rotated, err)
	}
	if !strings.HasPrefix(rotated.Kid, "oidc_") {
		t.Fatalf("rotated kid = %q, want oidc_ prefix", rotated.Kid)
	}
	if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 2 {
		t.Fatalf("signing key rows after rotation = %d", rows)
	}
	retiredAt := mustQueryString(t, env.db, `SELECT retired_at FROM oauth_signing_keys WHERE kid = ?`, first.Kid)
	if retiredAt != isoMillis(env.clock.Now()) {
		t.Fatalf("retired_at = %q, want %q", retiredAt, isoMillis(env.clock.Now()))
	}
	if status := mustQueryString(t, env.db, `SELECT status FROM oauth_signing_keys WHERE kid = ?`, first.Kid); status != "retired" {
		t.Fatalf("old key status = %q", status)
	}
}

func TestListSigningJwksRetention(t *testing.T) {
	env := newStoreEnv(t)
	now := isoMillis(env.clock.Now())
	freshRetired := isoMillis(env.clock.Now().Add(-time.Hour))
	ancientRetired := isoMillis(env.clock.Now().Add(-nodeMillis(grantLifetimeMs) - time.Millisecond))
	jwk := func(kid string) string {
		return `{"kty":"RSA","n":"n-` + kid + `","e":"AQAB","kid":"` + kid + `","use":"sig","alg":"RS256"}`
	}
	mustExec(t, env.db, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES ('k1', 'kid-active', 'ct', ?, 'active', ?, NULL)`, jwk("kid-active"), now)
	mustExec(t, env.db, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES ('k2', 'kid-fresh', 'ct', ?, 'retired', ?, ?)`, jwk("kid-fresh"), freshRetired, freshRetired)
	mustExec(t, env.db, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES ('k3', 'kid-ancient', 'ct', ?, 'retired', ?, ?)`, jwk("kid-ancient"), ancientRetired, ancientRetired)

	keys, err := env.store.ListSigningJwks(context.Background())
	if err != nil {
		t.Fatalf("ListSigningJwks: %v", err)
	}
	var kids []string
	for _, key := range keys {
		kids = append(kids, key["kid"].(string))
	}
	if len(kids) != 2 || kids[0] != "kid-active" || kids[1] != "kid-fresh" {
		t.Fatalf("jwks kids = %v, want [kid-active kid-fresh]", kids)
	}

	// A retired key with an invalid retired_at fails the whole listing.
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET retired_at = 'bogus' WHERE kid = 'kid-fresh'`)
	if _, err := env.store.ListSigningJwks(context.Background()); err == nil || err.Error() != "OIDC 签名密钥 retiredAt 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid retired_at error = %v", err)
	}
	// A retired key without retired_at fails too.
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET retired_at = NULL WHERE kid = 'kid-fresh'`)
	if _, err := env.store.ListSigningJwks(context.Background()); err == nil || err.Error() != "OIDC 签名密钥 retiredAt 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("null retired_at error = %v", err)
	}

	// Incomplete public JWKs are skipped silently (Node drops them as well).
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET retired_at = ? WHERE kid = 'kid-fresh'`, freshRetired)
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET public_jwk_json = '{"kid":"kid-active"}' WHERE kid = 'kid-active'`)
	keys, err = env.store.ListSigningJwks(context.Background())
	if err != nil {
		t.Fatalf("ListSigningJwks incomplete: %v", err)
	}
	if len(keys) != 1 || keys[0]["kid"] != "kid-fresh" {
		t.Fatalf("incomplete jwk filtered: %v", keys)
	}
}

func TestSigningKeyRowValidation(t *testing.T) {
	env := newStoreEnv(t)
	now := isoMillis(env.clock.Now())
	mustExec(t, env.db, `INSERT INTO oauth_signing_keys (id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES ('k-bad', 'kid-bad', 'ct', 'not-json', 'active', ?, NULL)`, now)
	if _, err := env.store.FindActiveSigningKey(context.Background()); err == nil || err.Error() != "OIDC 签名公钥内容无效" {
		t.Fatalf("bad jwk json error = %v", err)
	}
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET public_jwk_json = '{"kty":"RSA","n":"nn"}' WHERE id = 'k-bad'`)
	if _, err := env.store.FindActiveSigningKey(context.Background()); err == nil || err.Error() != "OIDC 签名公钥字段不完整" {
		t.Fatalf("incomplete jwk error = %v", err)
	}
	if _, err := env.store.FindActiveSigningKey(context.Background()); err == nil {
		t.Fatal("expected persistent error")
	}
}

// ---------------------------------------------------------------------------
// Authorization transactions (consent state).
// ---------------------------------------------------------------------------

func TestAuthorizationTransactionLifecycle(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	transaction, err := env.store.CreateAuthorizationTransaction(ctx, struct {
		ClientID      string
		RedirectURI   string
		Scopes        []string
		State         string
		CodeChallenge string
		Nonce         string
	}{
		ClientID: env.publicID, RedirectURI: env.publicRedirect, Scopes: []string{"openid", "profile"},
		State: "st-1", CodeChallenge: pkceChallengeOf(pkceTestVerifier), Nonce: "n-1",
	})
	if err != nil {
		t.Fatalf("CreateAuthorizationTransaction: %v", err)
	}
	if transaction.ExpiresAt != iso(env.clock.Now().Add(10*time.Minute)) {
		t.Fatalf("transaction expiresAt = %q", transaction.ExpiresAt)
	}
	// Stored row: hashed csrf, encrypted state envelope, NULL completed_at.
	csrfHash := mustQueryString(t, env.db, `SELECT csrf_hash FROM oauth_authorization_transactions WHERE id = ?`, transaction.ID)
	if csrfHash != hashSecret(transaction.CSRFToken) {
		t.Fatalf("csrf_hash mismatch: %q", csrfHash)
	}
	var payload struct {
		State     string `json:"state"`
		CSRFToken string `json:"csrfToken"`
		Nonce     any    `json:"nonce"`
	}
	ciphertext := mustQueryString(t, env.db, `SELECT state_ciphertext FROM oauth_authorization_transactions WHERE id = ?`, transaction.ID)
	if err := DecryptOidcValue(oidcTestSecret, ciphertext, &payload); err != nil {
		t.Fatalf("decrypt state envelope: %v", err)
	}
	if payload.State != "st-1" || payload.CSRFToken != transaction.CSRFToken {
		t.Fatalf("state envelope payload = %+v", payload)
	}
	if payload.Nonce != "n-1" {
		t.Fatalf("state envelope nonce = %v", payload.Nonce)
	}
	if completed := countRows(t, env.db, "oauth_authorization_transactions WHERE completed_at IS NOT NULL"); completed != 0 {
		t.Fatalf("completed rows = %d", completed)
	}

	found, err := env.store.FindAuthorizationTransaction(ctx, transaction.ID)
	if err != nil || found == nil {
		t.Fatalf("FindAuthorizationTransaction = %+v, err=%v", found, err)
	}
	if found.State != "st-1" || found.CSRFToken != transaction.CSRFToken || found.Nonce != "n-1" {
		t.Fatalf("found transaction = %+v", found)
	}
	if found.Scopes[0] != "openid" || found.Scopes[1] != "profile" {
		t.Fatalf("found scopes = %v", found.Scopes)
	}

	// Wrong CSRF: no consumption, transaction stays open.
	if consumed, err := env.store.ConsumeAuthorizationTransaction(ctx, transaction.ID, "wrong-csrf"); err != nil || consumed != nil {
		t.Fatalf("consume wrong csrf = %+v, err=%v", consumed, err)
	}
	if completed := countRows(t, env.db, "oauth_authorization_transactions WHERE completed_at IS NOT NULL"); completed != 0 {
		t.Fatalf("wrong csrf completed rows = %d", completed)
	}

	consumed, err := env.store.ConsumeAuthorizationTransaction(ctx, transaction.ID, transaction.CSRFToken)
	if err != nil || consumed == nil || consumed.ID != transaction.ID {
		t.Fatalf("consume = %+v, err=%v", consumed, err)
	}
	if completed := countRows(t, env.db, "oauth_authorization_transactions WHERE completed_at IS NOT NULL"); completed != 1 {
		t.Fatalf("completed rows after consume = %d", completed)
	}
	// One-time: a replay finds nothing.
	if replay, err := env.store.ConsumeAuthorizationTransaction(ctx, transaction.ID, transaction.CSRFToken); err != nil || replay != nil {
		t.Fatalf("replayed consume = %+v, err=%v", replay, err)
	}
	// One-time: a replay finds nothing (Node findAuthorizationTransaction
	// returns undefined for completed transactions; the route contract is
	// pinned by TestAuthorizeResumeWithTransactionID).
	replay, replayErr := env.store.FindAuthorizationTransaction(ctx, transaction.ID)
	if replay != nil || replayErr != nil {
		t.Fatalf("completed transaction = %+v, err=%v (Node: nil, nil)", replay, replayErr)
	}
}

func TestAuthorizationTransactionExpiry(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	transaction, err := env.store.CreateAuthorizationTransaction(ctx, struct {
		ClientID      string
		RedirectURI   string
		Scopes        []string
		State         string
		CodeChallenge string
		Nonce         string
	}{ClientID: env.publicID, RedirectURI: env.publicRedirect, Scopes: []string{"openid"}, State: "st-2",
		CodeChallenge: pkceChallengeOf(pkceTestVerifier), Nonce: ""})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.clock.Advance(10 * time.Minute)
	// Boundary: expires_at == now is already expired (expires_at > now filter).
	// Node returns undefined for expired transactions → (nil, nil); the route
	// maps that to 400 invalid_request (TestAuthorizeResumeWithTransactionID).
	if found, err := env.store.FindAuthorizationTransaction(ctx, transaction.ID); found != nil || err != nil {
		t.Fatalf("expired transaction = %+v, err=%v (Node: nil, nil)", found, err)
	}
	if consumed, err := env.store.ConsumeAuthorizationTransaction(ctx, transaction.ID, transaction.CSRFToken); err != nil || consumed != nil {
		t.Fatalf("expired consume = %+v, err=%v", consumed, err)
	}
}

// ---------------------------------------------------------------------------
// Authorization codes + exchange (one-time consumption, PKCE, expiry).
// ---------------------------------------------------------------------------

type codeInput = struct {
	ClientID        string
	SystemAccountID string
	Scopes          []string
	RedirectURI     string
	CodeChallenge   string
	Nonce           string
}

var _ = codeInput{} // kept for parity with the CreateAuthorizationCode input shape

func TestCreateAuthorizationCodeStoresHashedRows(t *testing.T) {
	env := newStoreEnv(t)
	code, err := env.store.CreateAuthorizationCode(context.Background(), codeInput{
		ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"openid", "profile"},
		RedirectURI: env.publicRedirect, CodeChallenge: pkceChallengeOf(pkceTestVerifier), Nonce: "n-9",
	})
	if err != nil {
		t.Fatalf("CreateAuthorizationCode: %v", err)
	}
	if code == "" {
		t.Fatal("empty code")
	}
	// The plaintext code is never stored.
	if rows := countRows(t, env.db, "oauth_authorization_codes WHERE code_hash = "+quoteLiteral(code)); rows != 0 {
		t.Fatalf("plaintext code stored: %d rows", rows)
	}
	if rows := countRows(t, env.db, "oauth_authorization_codes WHERE code_hash = '"+hashSecret(code)+"'"); rows != 1 {
		t.Fatalf("hashed code row missing")
	}
	// Regression (former BUG-3): the millisecond lifetime constants must be
	// applied as time.Duration(ms)*time.Millisecond. Node writes
	// Date.now() + 120s for the code and Date.now() + 7d for the grant.
	codeExpires := mustQueryString(t, env.db, `SELECT expires_at FROM oauth_authorization_codes WHERE code_hash = ?`, hashSecret(code))
	wantCodeExpires := isoMillis(env.clock.Now().Add(nodeMillis(authorizationCodeLifetimeMs)))
	if codeExpires != wantCodeExpires {
		t.Fatalf("code expires_at = %q, want %q (Node contract: +120s)", codeExpires, wantCodeExpires)
	}
	grantExpires := mustQueryString(t, env.db, `SELECT grants.expires_at FROM oauth_grants grants, oauth_authorization_codes codes WHERE grants.id = codes.grant_id AND codes.code_hash = ?`, hashSecret(code))
	wantGrantExpires := isoMillis(env.clock.Now().Add(nodeMillis(grantLifetimeMs)))
	if grantExpires != wantGrantExpires {
		t.Fatalf("grant expires_at = %q, want %q (Node contract: +7d)", grantExpires, wantGrantExpires)
	}
	if rows := countRows(t, env.db, "oauth_authorization_code_oidc_contexts"); rows != 1 {
		t.Fatalf("nonce context rows = %d", rows)
	}
	var noncePayload struct {
		Nonce string `json:"nonce"`
	}
	nonceCiphertext := mustQueryString(t, env.db, `SELECT nonce_ciphertext FROM oauth_authorization_code_oidc_contexts`)
	if err := DecryptOidcValue(oidcTestSecret, nonceCiphertext, &noncePayload); err != nil || noncePayload.Nonce != "n-9" {
		t.Fatalf("nonce envelope = %+v, err=%v", noncePayload, err)
	}

	// No nonce → no oidc context row.
	if _, err := env.store.CreateAuthorizationCode(context.Background(), codeInput{
		ClientID: env.publicID, SystemAccountID: env.accountID, Scopes: []string{"juhe:profile.read"},
		RedirectURI: env.publicRedirect, CodeChallenge: pkceChallengeOf(pkceTestVerifier), Nonce: "",
	}); err != nil {
		t.Fatalf("CreateAuthorizationCode no-nonce: %v", err)
	}
	if rows := countRows(t, env.db, "oauth_authorization_code_oidc_contexts"); rows != 1 {
		t.Fatalf("nonce context rows after no-nonce create = %d", rows)
	}
}

func TestExchangeAuthorizationCodeOneTime(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "n-ex",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))

	// Wrong client / redirect / verifier never consume the code.
	for name, args := range map[string][4]string{
		"wrong client":     {"juhe_other", code, env.publicRedirect, pkceTestVerifier},
		"wrong redirect":   {env.publicID, code, "https://evil.example.com/callback", pkceTestVerifier},
		"wrong verifier":   {env.publicID, code, env.publicRedirect, strings.Repeat("x", 43)},
		"unknown code":     {env.publicID, "no-such-code", env.publicRedirect, pkceTestVerifier},
		"empty verifier":   {env.publicID, code, env.publicRedirect, ""},
		"verifier too hot": {env.publicID, code, env.publicRedirect, "short"},
	} {
		issued, err := env.store.ExchangeAuthorizationCode(ctx, args[0], args[1], args[2], args[3])
		if err != nil || issued != nil {
			t.Fatalf("%s: exchange = %+v, err=%v", name, issued, err)
		}
	}
	if rows := countRows(t, env.db, "oauth_authorization_codes WHERE consumed_at IS NOT NULL"); rows != 0 {
		t.Fatalf("failed attempts consumed the code: %d", rows)
	}

	issued, err := env.store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier)
	if err != nil || issued == nil {
		t.Fatalf("exchange = %+v, err=%v", issued, err)
	}
	if issued.AccessToken == "" || issued.Nonce != "n-ex" {
		t.Fatalf("issued = %+v", issued)
	}
	if issued.Context.ClientID != env.publicID || issued.Context.SystemAccountID != env.accountID {
		t.Fatalf("issued context = %+v", issued.Context)
	}
	if issued.Context.Scopes[0] != "openid" {
		t.Fatalf("issued scopes = %v", issued.Context.Scopes)
	}
	// requiredTimestamp canonicalizes to Node's toISOString() millisecond
	// precision — the trailing ".000" is part of the contract.
	if issued.Context.IssuedAt != iso(env.clock.Now()) {
		t.Fatalf("issued issuedAt = %q, want %q", issued.Context.IssuedAt, iso(env.clock.Now()))
	}
	if rows := countRows(t, env.db, "oauth_access_tokens WHERE token_hash = '"+hashSecret(issued.AccessToken)+"'"); rows != 1 {
		t.Fatal("access token row missing")
	}

	// One-time: replay returns nil and does not create a second token.
	replay, err := env.store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier)
	if err != nil || replay != nil {
		t.Fatalf("replay = %+v, err=%v", replay, err)
	}
	if rows := countRows(t, env.db, "oauth_access_tokens"); rows != 1 {
		t.Fatalf("token rows after replay = %d", rows)
	}
}

func TestExchangeAuthorizationCodeExpiry(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	env.clock.Advance(nodeMillis(authorizationCodeLifetimeMs))
	// Boundary: expires_at == now is already expired.
	if issued, err := env.store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier); err != nil || issued != nil {
		t.Fatalf("expired exchange = %+v, err=%v", issued, err)
	}
}

func TestExchangeAuthorizationCodeGrantExpiredError(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	// Corrupt the grant's client binding: the code JOIN still matches but
	// issueAccessTokenInTransaction refuses the mismatch (Node throws the
	// same message and the route maps it to 500 server_error).
	grantID := mustQueryString(t, env.db, `SELECT grant_id FROM oauth_authorization_codes WHERE code_hash = ?`, hashSecret(code))
	mustExec(t, env.db, `UPDATE oauth_grants SET client_id = 'juhe_conf_client' WHERE id = ?`, grantID)
	_, err := env.store.ExchangeAuthorizationCode(ctx, env.publicID, code, env.publicRedirect, pkceTestVerifier)
	if err == nil || err.Error() != "OAuth grant 已失效" {
		t.Fatalf("grant expired error = %v", err)
	}
}

func TestAuthorizationCodeRequestsIdToken(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	openidCode := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	plainCode := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"juhe:profile.read"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	if got, err := env.store.AuthorizationCodeRequestsIdToken(ctx, env.publicID, openidCode); err != nil || !got {
		t.Fatalf("openid code requests id token = %v, err=%v", got, err)
	}
	if got, err := env.store.AuthorizationCodeRequestsIdToken(ctx, env.publicID, plainCode); err != nil || got {
		t.Fatalf("plain code requests id token = %v, err=%v", got, err)
	}
	if got, err := env.store.AuthorizationCodeRequestsIdToken(ctx, "juhe_other", openidCode); err != nil || got {
		t.Fatalf("wrong client = %v, err=%v", got, err)
	}
	// Consumed codes report false.
	if _, err := env.store.ExchangeAuthorizationCode(ctx, env.publicID, openidCode, env.publicRedirect, pkceTestVerifier); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got, err := env.store.AuthorizationCodeRequestsIdToken(ctx, env.publicID, openidCode); err != nil || got {
		t.Fatalf("consumed code = %v, err=%v", got, err)
	}
}

// ---------------------------------------------------------------------------
// Device flow.
// ---------------------------------------------------------------------------

func TestCreateDeviceAuthorizationRow(t *testing.T) {
	env := newStoreEnv(t)
	authorization, deviceCode, err := env.store.CreateDeviceAuthorization(context.Background(), struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "n-dev", VerificationURI: oidcTestIssuer + "/oauth/device"})
	if err != nil {
		t.Fatalf("CreateDeviceAuthorization: %v", err)
	}
	if deviceCode == "" || authorization.IntervalSeconds != 5 || authorization.Status != "pending" {
		t.Fatalf("authorization = %+v", authorization)
	}
	if authorization.ExpiresAt != iso(env.clock.Now().Add(600*time.Second)) {
		t.Fatalf("device expiresAt = %q", authorization.ExpiresAt)
	}
	if len(authorization.UserCode) != 8 {
		t.Fatalf("user code = %q", authorization.UserCode)
	}
	for _, c := range authorization.UserCode {
		if !strings.ContainsRune(userCodeAlphabet, c) {
			t.Fatalf("user code %q has char %q outside alphabet", authorization.UserCode, c)
		}
	}
	if rows := countRows(t, env.db, "oauth_device_authorizations WHERE device_code_hash = '"+hashSecret(deviceCode)+"'"); rows != 1 {
		t.Fatal("device code hash row missing")
	}
	var noncePayload struct {
		Nonce string `json:"nonce"`
	}
	nonceCiphertext := mustQueryString(t, env.db, `SELECT nonce_ciphertext FROM oauth_device_authorizations WHERE id = ?`, authorization.ID)
	if err := DecryptOidcValue(oidcTestSecret, nonceCiphertext, &noncePayload); err != nil || noncePayload.Nonce != "n-dev" {
		t.Fatalf("device nonce = %+v, err=%v", noncePayload, err)
	}
}

func TestPrepareDeviceAuthorization(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	authorization, deviceCode, err := env.store.CreateDeviceAuthorization(ctx, struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "", VerificationURI: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rows := countRows(t, env.db, "oauth_device_authorizations WHERE csrf_hash IS NOT NULL"); rows != 0 {
		t.Fatal("csrf hash set before prepare")
	}
	// Lowercase input is normalized (Node trims + upper-cases).
	prepared, csrfToken, err := env.store.PrepareDeviceAuthorization(ctx, strings.ToLower(authorization.UserCode))
	if err != nil || prepared == nil || csrfToken == "" {
		t.Fatalf("prepare = %+v, csrf=%q, err=%v", prepared, csrfToken, err)
	}
	if prepared.UserCode != authorization.UserCode {
		t.Fatalf("prepared user code = %q", prepared.UserCode)
	}
	storedHash := mustQueryString(t, env.db, `SELECT csrf_hash FROM oauth_device_authorizations WHERE id = ?`, authorization.ID)
	if storedHash != hashSecret(csrfToken) {
		t.Fatal("csrf hash not persisted")
	}
	_ = deviceCode

	// Expired user codes are not preparable.
	env.clock.Advance(601 * time.Second)
	if prepared, _, err := env.store.PrepareDeviceAuthorization(ctx, authorization.UserCode); err != nil || prepared != nil {
		t.Fatalf("expired prepare = %+v, err=%v", prepared, err)
	}
}

func TestDecideDeviceAuthorization(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	mkDevice := func() (*DeviceAuthorization, string) {
		authorization, _, err := env.store.CreateDeviceAuthorization(ctx, struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "", VerificationURI: "u"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_, csrf, err := env.store.PrepareDeviceAuthorization(ctx, authorization.UserCode)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return authorization, csrf
	}

	// Allow path.
	authorization, csrf := mkDevice()
	decided, err := env.store.DecideDeviceAuthorization(ctx, authorization.UserCode, csrf, env.accountID, "allow")
	if err != nil || decided == nil {
		t.Fatalf("decide allow = %+v, err=%v", decided, err)
	}
	if decided.Status != "approved" || decided.SystemAccountID != env.accountID {
		t.Fatalf("approved = %+v", decided)
	}
	if approvedAt := mustQueryString(t, env.db, `SELECT approved_at FROM oauth_device_authorizations WHERE id = ?`, authorization.ID); approvedAt != iso(env.clock.Now()) {
		t.Fatalf("approved_at = %q", approvedAt)
	}
	// Replay is rejected (status left pending-branch).
	if replay, err := env.store.DecideDeviceAuthorization(ctx, authorization.UserCode, csrf, env.accountID, "allow"); err != nil || replay != nil {
		t.Fatalf("replay decide = %+v, err=%v", replay, err)
	}

	// Deny path with wrong CSRF first.
	deniedDevice, deniedCSRF := mkDevice()
	if bad, err := env.store.DecideDeviceAuthorization(ctx, deniedDevice.UserCode, "wrong", env.accountID, "deny"); err != nil || bad != nil {
		t.Fatalf("wrong csrf decide = %+v, err=%v", bad, err)
	}
	decidedDeny, err := env.store.DecideDeviceAuthorization(ctx, strings.ToLower(deniedDevice.UserCode), deniedCSRF, env.accountID, "deny")
	if err != nil || decidedDeny == nil || decidedDeny.Status != "denied" {
		t.Fatalf("decide deny = %+v, err=%v", decidedDeny, err)
	}
	if deniedAt := mustQueryString(t, env.db, `SELECT denied_at FROM oauth_device_authorizations WHERE id = ?`, deniedDevice.ID); deniedAt != iso(env.clock.Now()) {
		t.Fatalf("denied_at = %q", deniedAt)
	}
	if accountID := mustQueryString(t, env.db, `SELECT COALESCE(system_account_id, '') FROM oauth_device_authorizations WHERE id = ?`, deniedDevice.ID); accountID != "" {
		t.Fatalf("deny must not bind an account: %q", accountID)
	}

	// Expired devices cannot be decided.
	expiryDevice, expiryCSRF := mkDevice()
	env.clock.Advance(601 * time.Second)
	if decided, err := env.store.DecideDeviceAuthorization(ctx, expiryDevice.UserCode, expiryCSRF, env.accountID, "allow"); err != nil || decided != nil {
		t.Fatalf("expired decide = %+v, err=%v", decided, err)
	}
}

func TestPollDeviceAuthorizationKinds(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	create := func() (*DeviceAuthorization, string) {
		authorization, deviceCode, err := env.store.CreateDeviceAuthorization(ctx, struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid", "profile"}, Nonce: "n-poll", VerificationURI: "u"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return authorization, deviceCode
	}
	prepare := func(authorization *DeviceAuthorization) string {
		_, csrf, err := env.store.PrepareDeviceAuthorization(ctx, authorization.UserCode)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return csrf
	}

	// Unknown device code and wrong client → PollInvalid.
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, "unknown-device-code"); err != nil || poll.Kind != PollInvalid {
		t.Fatalf("unknown code poll = %+v, err=%v", poll, err)
	}
	unknownClient, unknownCode := create()
	if poll, err := env.store.PollDeviceAuthorization(ctx, "juhe_other", unknownCode); err != nil || poll.Kind != PollInvalid {
		t.Fatalf("wrong client poll = %+v, err=%v", poll, err)
	}
	_ = unknownClient

	// Pending → authorization_pending, last_polled_at recorded.
	pendingDevice, pendingCode := create()
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, pendingCode); err != nil || poll.Kind != PollAuthorizationPending {
		t.Fatalf("pending poll = %+v, err=%v", poll, err)
	}
	lastPolled := mustQueryString(t, env.db, `SELECT last_polled_at FROM oauth_device_authorizations WHERE id = ?`, pendingDevice.ID)
	if lastPolled != iso(env.clock.Now()) {
		t.Fatalf("last_polled_at = %q", lastPolled)
	}

	// Polling faster than the interval → slow_down and escalating interval.
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, pendingCode); err != nil || poll.Kind != PollSlowDown {
		t.Fatalf("slow poll = %+v, err=%v", poll, err)
	}
	if interval := mustQueryString(t, env.db, `SELECT interval_seconds FROM oauth_device_authorizations WHERE id = ?`, pendingDevice.ID); interval != "10" {
		t.Fatalf("escalated interval = %q", interval)
	}
	// Cap at 60 via the MIN/LEAST(interval+5, 60) escalation (dialect picked
	// by intervalEscalationExpr; SQLite path exercised end to end here).
	mustExec(t, env.db, `UPDATE oauth_device_authorizations SET interval_seconds = 57, last_polled_at = ? WHERE id = ?`, iso(env.clock.Now()), pendingDevice.ID)
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, pendingCode); err != nil || poll.Kind != PollSlowDown {
		t.Fatalf("cap poll = %+v, err=%v", poll, err)
	}
	if interval := mustQueryString(t, env.db, `SELECT interval_seconds FROM oauth_device_authorizations WHERE id = ?`, pendingDevice.ID); interval != "60" {
		t.Fatalf("capped interval = %q", interval)
	}

	// Expiry transition.
	expiryDevice, expiryCode := create()
	env.clock.Advance(601 * time.Second)
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, expiryCode); err != nil || poll.Kind != PollExpired {
		t.Fatalf("expired poll = %+v, err=%v", poll, err)
	}
	if status := mustQueryString(t, env.db, `SELECT status FROM oauth_device_authorizations WHERE id = ?`, expiryDevice.ID); status != "expired" {
		t.Fatalf("expired status = %q", status)
	}

	// Denied → access_denied.
	deniedDevice, deniedCode := create()
	if _, err := env.store.DecideDeviceAuthorization(ctx, deniedDevice.UserCode, prepare(deniedDevice), env.accountID, "deny"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, deniedCode); err != nil || poll.Kind != PollAccessDenied {
		t.Fatalf("denied poll = %+v, err=%v", poll, err)
	}

	// Approved but account inactive → invalid_grant (and the row stays approved,
	// retryable after the operator repairs the account).
	orphanDevice, orphanCode := create()
	orphanCSRF := prepare(orphanDevice)
	if _, err := env.store.DecideDeviceAuthorization(ctx, orphanDevice.UserCode, orphanCSRF, env.accountID, "allow"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	mustExec(t, env.db, `UPDATE system_accounts SET status = 'disabled' WHERE id = ?`, env.accountID)
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, orphanCode); err != nil || poll.Kind != PollInvalidGrant {
		t.Fatalf("inactive account poll = %+v, err=%v", poll, err)
	}
	mustExec(t, env.db, `UPDATE system_accounts SET status = 'active' WHERE id = ?`, env.accountID)

	// Approved with a missing system_account_id → invalid_grant.
	nullAccount, nullCode := create()
	mustExec(t, env.db, `UPDATE oauth_device_authorizations SET status = 'approved' WHERE id = ?`, nullAccount.ID)
	if poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, nullCode); err != nil || poll.Kind != PollInvalidGrant {
		t.Fatalf("null account poll = %+v, err=%v", poll, err)
	}
}

func TestPollDeviceAuthorizationApprovedConsumesOneTime(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	authorization, deviceCode, err := env.store.CreateDeviceAuthorization(ctx, struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: env.publicID, Scopes: []string{"openid", "profile"}, Nonce: "n-once", VerificationURI: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, csrf, err := env.store.PrepareDeviceAuthorization(ctx, authorization.UserCode)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := env.store.DecideDeviceAuthorization(ctx, authorization.UserCode, csrf, env.accountID, "allow"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	// Approve resets nothing about last_polled_at; ensure the poll is not
	// slowed down by the earlier prepare (prepare does not set last_polled_at).
	poll, err := env.store.PollDeviceAuthorization(ctx, env.publicID, deviceCode)
	if err != nil || poll.Kind != PollApproved {
		t.Fatalf("approved poll = %+v, err=%v", poll, err)
	}
	if poll.Nonce != "n-once" || poll.AccessToken == "" {
		t.Fatalf("approved poll payload = %+v", poll)
	}
	if poll.Context.SystemAccountID != env.accountID || poll.Context.ClientID != env.publicID {
		t.Fatalf("approved context = %+v", poll.Context)
	}
	// BUG-3 also hit the device flow: the grant minted by the device poll now
	// carries the Node lifetime of Date.now() + 7d.
	if poll.Context.ExpiresAt != iso(env.clock.Now().Add(nodeMillis(grantLifetimeMs))) {
		t.Fatalf("approved grant expiry = %q, want %q", poll.Context.ExpiresAt, iso(env.clock.Now().Add(nodeMillis(grantLifetimeMs))))
	}
	if status := mustQueryString(t, env.db, `SELECT status FROM oauth_device_authorizations WHERE id = ?`, authorization.ID); status != "consumed" {
		t.Fatalf("consumed status = %q", status)
	}
	if rows := countRows(t, env.db, "oauth_access_tokens WHERE token_hash = '"+hashSecret(poll.AccessToken)+"'"); rows != 1 {
		t.Fatal("access token row missing")
	}
	// One-time: the next poll reports invalid_grant.
	if replay, err := env.store.PollDeviceAuthorization(ctx, env.publicID, deviceCode); err != nil || replay.Kind != PollInvalidGrant {
		t.Fatalf("replay poll = %+v, err=%v", replay, err)
	}
}

// ---------------------------------------------------------------------------
// Access tokens: lookup filters, renewal, revocation.
// ---------------------------------------------------------------------------

func TestFindAccessTokenContextFilters(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	grantID := "grant-live"
	env.insertGrant(t, grantID, env.clock.Now().Add(24*time.Hour))
	issuedAt := env.clock.Now()
	env.insertAccessTokenRow(t, "tok-live", grantID, issuedAt, env.clock.Now().Add(time.Hour))

	context, err := env.store.FindAccessTokenContext(ctx, "tok-live")
	if err != nil || context == nil {
		t.Fatalf("live token = %+v, err=%v", context, err)
	}
	if context.GrantID != grantID || context.Scopes[0] != "openid" {
		t.Fatalf("live context = %+v", context)
	}

	// Revoked token.
	env.insertAccessTokenRow(t, "tok-revoked", grantID, issuedAt, env.clock.Now().Add(time.Hour))
	mustExec(t, env.db, `UPDATE oauth_access_tokens SET revoked_at = ? WHERE id = 'tok-tok-revoked'`, iso(env.clock.Now()))
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-revoked"); err != nil || context != nil {
		t.Fatalf("revoked token = %+v, err=%v", context, err)
	}

	// Replaced token.
	env.insertAccessTokenRow(t, "tok-replaced", grantID, issuedAt, env.clock.Now().Add(time.Hour))
	mustExec(t, env.db, `UPDATE oauth_access_tokens SET replaced_at = ? WHERE id = 'tok-tok-replaced'`, iso(env.clock.Now()))
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-replaced"); err != nil || context != nil {
		t.Fatalf("replaced token = %+v, err=%v", context, err)
	}

	// Expired token (expires_at == now is expired).
	env.insertAccessTokenRow(t, "tok-expired", grantID, issuedAt.Add(-2*time.Hour), env.clock.Now())
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-expired"); err != nil || context != nil {
		t.Fatalf("expired token = %+v, err=%v", context, err)
	}

	// Expired grant.
	expiredGrant := "grant-expired"
	env.insertGrant(t, expiredGrant, env.clock.Now().Add(-time.Second))
	env.insertAccessTokenRow(t, "tok-grant-expired", expiredGrant, issuedAt, env.clock.Now().Add(time.Hour))
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-grant-expired"); err != nil || context != nil {
		t.Fatalf("expired grant token = %+v, err=%v", context, err)
	}

	// Disabled client.
	disabledGrant := "grant-disabled-client"
	env.insertGrant(t, disabledGrant, env.clock.Now().Add(24*time.Hour))
	env.insertAccessTokenRow(t, "tok-client-disabled", disabledGrant, issuedAt, env.clock.Now().Add(time.Hour))
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'disabled' WHERE client_id = ?`, env.publicID)
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-client-disabled"); err != nil || context != nil {
		t.Fatalf("disabled client token = %+v, err=%v", context, err)
	}
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'active' WHERE client_id = ?`, env.publicID)

	// Disabled account.
	accountGrant := "grant-disabled-account"
	mustExec(t, env.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES (?, ?, 'acc-ghost', '["openid"]', ?, NULL, ?)`, accountGrant, env.publicID, iso(env.clock.Now().Add(24*time.Hour)), iso(env.clock.Now()))
	env.insertAccessTokenRow(t, "tok-account-disabled", accountGrant, issuedAt, env.clock.Now().Add(time.Hour))
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-account-disabled"); err != nil || context != nil {
		t.Fatalf("disabled account token = %+v, err=%v", context, err)
	}

	// Unknown token.
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-unknown"); err != nil || context != nil {
		t.Fatalf("unknown token = %+v, err=%v", context, err)
	}
}

func TestRotateAccessToken(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	grantID := "grant-rotate"
	env.insertGrant(t, grantID, env.clock.Now().Add(96*time.Hour))
	originalExpiry := env.clock.Now().Add(96 * time.Hour)
	env.insertAccessTokenRow(t, "tok-rotate", grantID, env.clock.Now(), originalExpiry)

	// Inside the 72h window the renewal is refused.
	renewed, notEligible, err := env.store.RotateAccessToken(ctx, env.publicID, "tok-rotate")
	if err != nil || renewed != nil || !notEligible {
		t.Fatalf("early rotate = %+v, notEligible=%v, err=%v", renewed, notEligible, err)
	}
	env.clock.Advance(72*time.Hour - time.Millisecond)
	if _, notEligible, err := env.store.RotateAccessToken(ctx, env.publicID, "tok-rotate"); err != nil || !notEligible {
		t.Fatalf("72h-1ms rotate notEligible = %v, err=%v", notEligible, err)
	}
	// At exactly 72h the rotation is allowed.
	env.clock.Advance(time.Millisecond)
	renewed, notEligible, err = env.store.RotateAccessToken(ctx, env.publicID, "tok-rotate")
	if err != nil || renewed == nil || notEligible {
		t.Fatalf("rotate = %+v, notEligible=%v, err=%v", renewed, notEligible, err)
	}
	if renewed.AccessToken == "tok-rotate" || renewed.AccessToken == "" {
		t.Fatalf("rotated token = %q", renewed.AccessToken)
	}
	// The successor inherits the original expiry and grant binding, passed
	// through requiredTimestamp's toISOString() millisecond canonicalization.
	if renewed.Context.ExpiresAt != isoMillis(originalExpiry) {
		t.Fatalf("rotated expiry = %q, want %q", renewed.Context.ExpiresAt, isoMillis(originalExpiry))
	}
	if renewed.Context.GrantID != grantID {
		t.Fatalf("rotated grant = %q", renewed.Context.GrantID)
	}
	// The old token is replaced and no longer resolvable.
	oldToken := mustQueryString(t, env.db, `SELECT replaced_at FROM oauth_access_tokens WHERE id = 'tok-tok-rotate'`)
	if oldToken != iso(env.clock.Now()) {
		t.Fatalf("replaced_at = %q", oldToken)
	}
	successor := mustQueryString(t, env.db, `SELECT successor_token_id FROM oauth_access_tokens WHERE id = 'tok-tok-rotate'`)
	if successor != renewed.Context.TokenID {
		t.Fatalf("successor_token_id = %q, want %q", successor, renewed.Context.TokenID)
	}
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-rotate"); err != nil || context != nil {
		t.Fatalf("old token still valid = %+v, err=%v", context, err)
	}
	if context, err := env.store.FindAccessTokenContext(ctx, renewed.AccessToken); err != nil || context == nil {
		t.Fatalf("new token unresolvable = %+v, err=%v", context, err)
	}
	// The fresh token is inside its own 72h window.
	if _, notEligible, err := env.store.RotateAccessToken(ctx, env.publicID, renewed.AccessToken); err != nil || !notEligible {
		t.Fatalf("fresh rotate notEligible = %v, err=%v", notEligible, err)
	}
}

func TestRotateAccessTokenClientAndLookupMisses(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	grantID := "grant-rotate-miss"
	env.insertGrant(t, grantID, env.clock.Now().Add(96*time.Hour))
	env.insertAccessTokenRow(t, "tok-miss", grantID, env.clock.Now().Add(-73*time.Hour), env.clock.Now().Add(48*time.Hour))

	if renewed, notEligible, err := env.store.RotateAccessToken(ctx, "juhe_other", "tok-miss"); err != nil || renewed != nil || notEligible {
		t.Fatalf("wrong client rotate = %+v, %v, %v", renewed, notEligible, err)
	}
	if renewed, notEligible, err := env.store.RotateAccessToken(ctx, env.publicID, "tok-ghost"); err != nil || renewed != nil || notEligible {
		t.Fatalf("unknown token rotate = %+v, %v, %v", renewed, notEligible, err)
	}
}

func TestRevokeAccessToken(t *testing.T) {
	env := newStoreEnv(t)
	ctx := context.Background()
	grantID := "grant-revoke"
	env.insertGrant(t, grantID, env.clock.Now().Add(24*time.Hour))
	env.insertAccessTokenRow(t, "tok-revoke", grantID, env.clock.Now(), env.clock.Now().Add(time.Hour))

	if err := env.store.RevokeAccessToken(ctx, "tok-revoke", "juhe_other"); err != nil {
		t.Fatalf("revoke wrong client: %v", err)
	}
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-revoke"); err != nil || context == nil {
		t.Fatalf("wrong-client revoke took effect = %+v, err=%v", context, err)
	}
	if err := env.store.RevokeAccessToken(ctx, "tok-revoke", env.publicID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revokedAt := mustQueryString(t, env.db, `SELECT revoked_at FROM oauth_access_tokens WHERE id = 'tok-tok-revoke'`); revokedAt != iso(env.clock.Now()) {
		t.Fatalf("revoked_at = %q", revokedAt)
	}
	if context, err := env.store.FindAccessTokenContext(ctx, "tok-revoke"); err != nil || context != nil {
		t.Fatalf("revoked token still valid = %+v, err=%v", context, err)
	}
	// Revoking again is idempotent (revoked_at stays the first value).
	if err := env.store.RevokeAccessToken(ctx, "tok-revoke", env.publicID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if revokedAt := mustQueryString(t, env.db, `SELECT revoked_at FROM oauth_access_tokens WHERE id = 'tok-tok-revoke'`); revokedAt != iso(env.clock.Now()) {
		t.Fatalf("second revoke changed revoked_at = %q", revokedAt)
	}
}

// ---------------------------------------------------------------------------
// Test utilities.
// ---------------------------------------------------------------------------

func countRows(t *testing.T, db *sql.DB, tableAndWhere string) int {
	t.Helper()
	rows, err := db.Query(`SELECT COUNT(*) FROM ` + tableAndWhere)
	if err != nil {
		t.Fatalf("count %s: %v", tableAndWhere, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("count returned no rows")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	return count
}

// quoteLiteral guards the "plaintext never stored" assertion.
func quoteLiteral(value string) string {
	return "'" + strings.NewReplacer("'", "''").Replace(value) + "'"
}
