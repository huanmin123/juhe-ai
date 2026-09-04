// store.go owns the oauth_* persistence for the public protocol surface,
// ported from backend/src/modules/oidc-provider/oidc-provider.repository.ts.
// Token/code/device values are stored only as sha256 hashes; every
// reconstructable secret (state/csrf/nonce/private key/client secret) is AES-GCM
// sealed with the OIDC value encryption.
package oidc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

const (
	authorizationCodeLifetimeMs = 120_000
	grantLifetimeMs             = 168 * 60 * 60 * 1_000
	tokenRenewalDelayMs         = 72 * 60 * 60 * 1_000
	// SigningKeyRotationIntervalMs mirrors oidcSigningKeyRotationIntervalMs
	// (7 days; rotation is lazy on the first protocol request past the boundary).
	SigningKeyRotationIntervalMs = 7 * 24 * 60 * 60 * 1_000
)

// Client mirrors OAuthClient including the secret hash (used for confidential
// client authentication; never serialized to responses in this package).
type Client struct {
	ID               string
	ClientID         string
	DisplayName      string
	ClientType       string // public | confidential
	ClientSecretHash *string
	RedirectUris     []string
	AllowedScopes    []string
	Status           string // active | disabled
	CreatedAt        string
	UpdatedAt        string
}

// AccessTokenContext mirrors OAuthAccessTokenContext.
type AccessTokenContext struct {
	TokenID        string
	ClientID       string
	GrantID        string
	SystemAccountID string
	Scopes         []string
	IssuedAt       string
	ExpiresAt      string
}

// SigningKey mirrors OAuthSigningKey.
type SigningKey struct {
	ID                   string
	Kid                  string
	PrivateKeyCiphertext string
	PublicJWK            map[string]any
	Status               string
	CreatedAt            string
	RetiredAt            *string
}

// AuthorizationTransaction mirrors OAuthAuthorizationTransaction.
type AuthorizationTransaction struct {
	ID           string
	ClientID     string
	RedirectURI  string
	Scopes       []string
	State        string
	CodeChallenge string
	CSRFToken    string
	Nonce        string
	ExpiresAt    string
}

// DeviceAuthorization mirrors OAuthDeviceAuthorization.
type DeviceAuthorization struct {
	ID              string
	ClientID        string
	UserCode        string
	VerificationURI string
	Scopes          []string
	Nonce           string
	ExpiresAt       string
	IntervalSeconds int
	Status          string // pending | approved | denied | consumed | expired
	SystemAccountID string
	LastPolledAt    *string
}

// IssuedToken mirrors the {accessToken, context, nonce} exchange outcomes.
type IssuedToken struct {
	AccessToken string
	Context     AccessTokenContext
	Nonce       string
}

// BrowserSession mirrors the findSessionByTokenAsync subset the public
// surface consumes: an active account bound to an unexpired session.
type BrowserSession struct {
	SessionID  string
	AccountID  string
	Username   string
	DisplayName string
	Role       string
}

// Store is the dual-mode oauth_* persistence.
type Store struct {
	db  *sql.DB
	pg  bool
	now func() time.Time
	// KeyEncryptionSecret mirrors runtimeConfig.oidc.keyEncryptionSecret.
	KeyEncryptionSecret string

	ensureMu sync.Mutex
}

// NewStore builds the store. keyEncryptionSecret is required for any flow
// that reads or writes encrypted envelopes.
func NewStore(db *sql.DB, postgres bool, now func() time.Time, keyEncryptionSecret string) (*Store, error) {
	if db == nil {
		return nil, errors.New("oidc store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, pg: postgres, now: now, KeyEncryptionSecret: keyEncryptionSecret}, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

func newUUIDv4() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("oidc: random source failed: " + err.Error())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

func hashSecret(value string) string { return apikeys.HashSecret(value) }

// requiredTimestampMS mirrors requiredTimestampMilliseconds.
func requiredTimestampMS(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, errors.New(value + " 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return parsed.UnixMilli(), nil
}

// requiredTimestamp mirrors requiredRfc3339Instant: the canonical form is
// Node's Date.prototype.toISOString(), which always keeps millisecond
// precision ("2026-01-02T03:04:05.000Z") — RFC3339Nano would drop the
// trailing ".000" and desynchronize the lexicographic expires_at comparisons.
func requiredTimestamp(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New(value + " 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return isoMillis(parsed), nil
}

// milliDuration converts the Node-style millisecond lifetime constants into a
// time.Duration. Passing the raw int to time.Add would read it as
// nanoseconds (120_000ns = 120µs), minting born-expired codes.
func milliDuration(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

func parseStringArray(value string) []string {
	parsed := []string{}
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(parsed))
	for _, item := range parsed {
		out = append(out, item)
	}
	return out
}

// ---------------------------------------------------------------------------
// Clients.
// ---------------------------------------------------------------------------

const oauthClientColumns = `id, client_id, display_name, client_type, client_secret_hash,
	redirect_uris_json, allowed_scopes_json, status, created_at, updated_at`

func scanClientRow(scan func(...any) error) (*Client, error) {
	var row struct {
		id, clientID, displayName, clientType string
		secretHash                            sql.NullString
		redirectUrisJSON, allowedScopesJSON   string
		status, createdAt, updatedAt          string
	}
	err := scan(&row.id, &row.clientID, &row.displayName, &row.clientType, &row.secretHash,
		&row.redirectUrisJSON, &row.allowedScopesJSON, &row.status, &row.createdAt, &row.updatedAt)
	if err != nil {
		return nil, err
	}
	client := &Client{
		ID: row.id, ClientID: row.clientID, DisplayName: row.displayName, ClientType: row.clientType,
		RedirectUris: parseStringArray(row.redirectUrisJSON), AllowedScopes: parseStringArray(row.allowedScopesJSON),
		Status: row.status, CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
	}
	if row.secretHash.Valid {
		hash := row.secretHash.String
		client.ClientSecretHash = &hash
	}
	return client, nil
}

// FindClient mirrors findOAuthClient; nil when missing.
func (s *Store) FindClient(ctx context.Context, clientID string) (*Client, error) {
	ctx = ensureCtx(ctx)
	client, err := scanClientRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+oauthClientColumns+`
			FROM `+s.table("oauth_clients")+` WHERE client_id = ?`), clientID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return client, err
}

// FindSystemAccountProfile mirrors findSystemAccountProfile; nil when missing
// or inactive.
func (s *Store) FindSystemAccountProfile(ctx context.Context, systemAccountID string) (*BrowserSession, error) {
	ctx = ensureCtx(ctx)
	var id, username, displayName, status string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, username, display_name, status
		FROM `+s.table("system_accounts")+` WHERE id = ? AND status = 'active'`), systemAccountID).
		Scan(&id, &username, &displayName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &BrowserSession{AccountID: id, Username: username, DisplayName: displayName, Role: status}, nil
}

// FindSessionByToken mirrors findSessionByTokenAsync for the consent flow:
// the session must exist, belong to an active account and be unexpired.
func (s *Store) FindSessionByToken(ctx context.Context, token string) (*BrowserSession, error) {
	ctx = ensureCtx(ctx)
	var sessionID, expiresAt, lastSeenAt, accountID, username, displayName, role, status string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT ss.id, ss.expires_at, ss.last_seen_at,
			sa.id, sa.username, sa.display_name, sa.role, sa.status
		FROM `+s.table("system_sessions")+` ss
		INNER JOIN `+s.table("system_accounts")+` sa ON sa.id = ss.system_account_id
		WHERE ss.token_hash = ?`), hashSecret(token)).Scan(
		&sessionID, &expiresAt, &lastSeenAt, &accountID, &username, &displayName, &role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, nil
	}
	expiresAtMs, err := requiredTimestampMS(expiresAt)
	if err != nil || lastSeenAt == "" {
		return nil, nil
	}
	if _, err := requiredTimestampMS(lastSeenAt); err != nil {
		return nil, nil
	}
	if expiresAtMs <= s.now().UnixMilli() {
		return nil, nil
	}
	return &BrowserSession{SessionID: sessionID, AccountID: accountID, Username: username, DisplayName: displayName, Role: role}, nil
}

// ---------------------------------------------------------------------------
// Signing keys (ensureOidcSigningKey: lazy weekly rotation).
// ---------------------------------------------------------------------------

func signingKeyFromRow(scan func(...any) error) (*SigningKey, error) {
	var row struct {
		id, kid, privateKeyCiphertext, publicJWKJSON string
		status, createdAt                            string
		retiredAt                                    sql.NullString
	}
	err := scan(&row.id, &row.kid, &row.privateKeyCiphertext, &row.publicJWKJSON, &row.status, &row.createdAt, &row.retiredAt)
	if err != nil {
		return nil, err
	}
	var publicJWK map[string]any
	if err := json.Unmarshal([]byte(row.publicJWKJSON), &publicJWK); err != nil {
		return nil, errors.New("OIDC 签名公钥内容无效")
	}
	if publicJWK["kty"] == nil || publicJWK["n"] == nil || publicJWK["e"] == nil || publicJWK["kid"] == nil {
		return nil, errors.New("OIDC 签名公钥字段不完整")
	}
	key := &SigningKey{
		ID: row.id, Kid: row.kid, PrivateKeyCiphertext: row.privateKeyCiphertext, PublicJWK: publicJWK,
		Status: row.status, CreatedAt: row.createdAt,
	}
	if row.retiredAt.Valid {
		retired := row.retiredAt.String
		key.RetiredAt = &retired
	}
	return key, nil
}

const signingKeyColumns = `id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at`

// FindActiveSigningKey mirrors findActiveOidcSigningKey: nil (not an error)
// when no active key exists, so EnsureSigningKey can bootstrap the first key
// of a fresh deployment.
func (s *Store) FindActiveSigningKey(ctx context.Context) (*SigningKey, error) {
	ctx = ensureCtx(ctx)
	key, err := signingKeyFromRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+signingKeyColumns+`
			FROM `+s.table("oauth_signing_keys")+` WHERE status = 'active'
			ORDER BY created_at DESC, id DESC LIMIT 1`)).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return key, err
}

func signingKeyRotationDue(createdAt string, now time.Time) bool {
	createdAtMs, err := requiredTimestampMS(createdAt)
	if err != nil {
		return true
	}
	return now.UnixMilli()-createdAtMs >= SigningKeyRotationIntervalMs
}

// EnsureSigningKey mirrors ensureOidcSigningKey: no-op while the active key is
// young; otherwise retire and insert a fresh RS256 key inside one transaction.
// An empty key table (FindActiveSigningKey → nil) takes the insert path, which
// bootstraps the first key of a fresh deployment exactly like Node.
// The mutex serializes the in-process lazy rotation.
func (s *Store) EnsureSigningKey(ctx context.Context) (*SigningKey, error) {
	ctx = ensureCtx(ctx)
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	current, err := s.FindActiveSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	if current != nil && !signingKeyRotationDue(current.CreatedAt, s.now()) {
		return current, nil
	}
	kid := "oidc_" + randomBase64URLBytes(12)
	material, err := CreateSigningKeyMaterial(s.KeyEncryptionSecret, kid)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	keyID := newUUIDv4()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var activeCreatedAt string
	var activeStatus string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT created_at, status FROM `+s.table("oauth_signing_keys")+`
		WHERE status = 'active' ORDER BY created_at DESC, id DESC LIMIT 1`)).Scan(&activeCreatedAt, &activeStatus)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No active key: insert below.
	case err != nil:
		return nil, err
	default:
		if activeStatus == "active" && !signingKeyRotationDue(activeCreatedAt, s.now()) {
			return s.FindActiveSigningKey(ctx)
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_signing_keys")+`
		SET status = 'retired', retired_at = ? WHERE status = 'active'`), now); err != nil {
		return nil, err
	}
	publicJWKJSON, err := json.Marshal(material.PublicJWK)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_signing_keys")+`
		(id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at)
		VALUES (?, ?, ?, ?, 'active', ?, NULL)`),
		keyID, kid, material.PrivateKeyCiphertext, string(publicJWKJSON), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &SigningKey{
		ID: keyID, Kid: kid, PrivateKeyCiphertext: material.PrivateKeyCiphertext,
		PublicJWK: material.PublicJWK, Status: "active", CreatedAt: now,
	}, nil
}

// ListSigningJwks mirrors listOidcSigningJwks: active plus retired keys inside
// the grant retention window, newest first.
func (s *Store) ListSigningJwks(ctx context.Context) ([]map[string]any, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT public_jwk_json, status, retired_at
		FROM `+s.table("oauth_signing_keys")+` WHERE status IN ('active', 'retired')
		ORDER BY created_at DESC, id DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	retainedSinceMs := s.now().UnixMilli() - grantLifetimeMs
	keys := []map[string]any{}
	for rows.Next() {
		var publicJWKJSON, status string
		var retiredAt sql.NullString
		if err := rows.Scan(&publicJWKJSON, &status, &retiredAt); err != nil {
			return nil, err
		}
		if status == "retired" {
			if retiredAt.Valid {
				retiredMs, err := requiredTimestampMS(retiredAt.String)
				if err == nil && retiredMs <= retainedSinceMs {
					continue
				}
				if err != nil {
					return nil, errors.New("OIDC 签名密钥 retiredAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
				}
			} else {
				return nil, errors.New("OIDC 签名密钥 retiredAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(publicJWKJSON), &parsed); err != nil {
			continue
		}
		if parsed["kid"] == nil || parsed["kty"] == nil || parsed["n"] == nil || parsed["e"] == nil {
			continue
		}
		keys = append(keys, parsed)
	}
	return keys, rows.Err()
}

// ---------------------------------------------------------------------------
// Authorization transactions (consent state).
// ---------------------------------------------------------------------------

// CreateAuthorizationTransaction mirrors createAuthorizationTransaction.
func (s *Store) CreateAuthorizationTransaction(ctx context.Context, input struct {
	ClientID      string
	RedirectURI   string
	Scopes        []string
	State         string
	CodeChallenge string
	Nonce         string
}) (*AuthorizationTransaction, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	csrfToken := randomBase64URLBytes(24)
	transaction := &AuthorizationTransaction{
		ID: newUUIDv4(), ClientID: input.ClientID, RedirectURI: input.RedirectURI,
		Scopes: input.Scopes, State: input.State, CodeChallenge: input.CodeChallenge,
		CSRFToken: csrfToken, Nonce: input.Nonce,
		ExpiresAt: isoMillis(s.now().Add(10 * time.Minute)),
	}
	stateCiphertext, err := EncryptOidcValue(s.KeyEncryptionSecret, map[string]any{
		"state": input.State, "csrfToken": csrfToken, "nonce": orNil(input.Nonce),
	})
	if err != nil {
		return nil, err
	}
	scopesJSON, err := json.Marshal(transaction.Scopes)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_authorization_transactions")+`
		(id, client_id, redirect_uri, scopes_json, state_ciphertext, code_challenge, csrf_hash, expires_at, completed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`),
		transaction.ID, transaction.ClientID, transaction.RedirectURI, string(scopesJSON), stateCiphertext,
		transaction.CodeChallenge, hashSecret(csrfToken), transaction.ExpiresAt, now); err != nil {
		return nil, err
	}
	return transaction, nil
}

func orNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func transactionFromRow(scan func(...any) error, secret string) (*AuthorizationTransaction, error) {
	var row struct {
		id, clientID, redirectURI, scopesJSON, stateCiphertext, codeChallenge, expiresAt string
	}
	err := scan(&row.id, &row.clientID, &row.redirectURI, &row.scopesJSON, &row.stateCiphertext,
		&row.codeChallenge, &row.expiresAt)
	if err != nil {
		return nil, err
	}
	var payload struct {
		State     string `json:"state"`
		CSRFToken string `json:"csrfToken"`
		Nonce     string `json:"nonce"`
	}
	if err := DecryptOidcValue(secret, row.stateCiphertext, &payload); err != nil {
		return nil, err
	}
	if payload.State == "" || payload.CSRFToken == "" {
		return nil, errors.New("OIDC 授权事务内容无效")
	}
	transaction := &AuthorizationTransaction{
		ID: row.id, ClientID: row.clientID, RedirectURI: row.redirectURI,
		Scopes: parseStringArray(row.scopesJSON), State: payload.State,
		CodeChallenge: row.codeChallenge, CSRFToken: payload.CSRFToken, Nonce: payload.Nonce,
		ExpiresAt: row.expiresAt,
	}
	if _, err := requiredTimestamp(row.expiresAt); err != nil {
		return nil, err
	}
	return transaction, nil
}

// FindAuthorizationTransaction mirrors findAuthorizationTransaction (only
// uncompleted, unexpired transactions are visible). Unknown, completed and
// expired ids return nil — the authorize route maps that to 400
// invalid_request "授权请求不存在或已过期".
func (s *Store) FindAuthorizationTransaction(ctx context.Context, id string) (*AuthorizationTransaction, error) {
	ctx = ensureCtx(ctx)
	transaction, err := transactionFromRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT id, client_id, redirect_uri, scopes_json, state_ciphertext,
			code_challenge, expires_at FROM `+s.table("oauth_authorization_transactions")+`
			WHERE id = ? AND completed_at IS NULL AND expires_at > ?`), id, s.nowISO()).Scan(targets...)
	}, s.KeyEncryptionSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return transaction, err
}

// ConsumeAuthorizationTransaction mirrors consumeAuthorizationTransaction:
// single-use completion guarded by the CSRF hash.
func (s *Store) ConsumeAuthorizationTransaction(ctx context.Context, id, csrfToken string) (*AuthorizationTransaction, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	transaction, err := transactionFromRow(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT id, client_id, redirect_uri, scopes_json, state_ciphertext,
			code_challenge, expires_at FROM `+s.table("oauth_authorization_transactions")+`
			WHERE id = ? AND completed_at IS NULL AND expires_at > ?`), id, s.nowISO()).Scan(targets...)
	}, s.KeyEncryptionSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hashSecret(csrfToken) != hashSecret(transaction.CSRFToken) {
		return nil, nil
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_authorization_transactions")+`
		SET completed_at = ? WHERE id = ? AND completed_at IS NULL AND expires_at > ?`),
		s.nowISO(), id, s.nowISO())
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return transaction, nil
}

// ---------------------------------------------------------------------------
// Authorization codes + grants.
// ---------------------------------------------------------------------------

// CreateAuthorizationCode mirrors createAuthorizationCode: a fresh grant plus
// a hashed one-time code (and an encrypted nonce context when openid is
// requested). Returns the plaintext code.
func (s *Store) CreateAuthorizationCode(ctx context.Context, input struct {
	ClientID       string
	SystemAccountID string
	Scopes         []string
	RedirectURI    string
	CodeChallenge  string
	Nonce          string
}) (string, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	expiresAt := isoMillis(s.now().Add(milliDuration(grantLifetimeMs)))
	grantID := newUUIDv4()
	code := randomBase64URLBytes(32)
	codeID := newUUIDv4()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	scopesJSON, err := json.Marshal(input.Scopes)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_grants")+`
		(id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?)`),
		grantID, input.ClientID, input.SystemAccountID, string(scopesJSON), expiresAt, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_authorization_codes")+`
		(id, code_hash, client_id, grant_id, redirect_uri, code_challenge, expires_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`),
		codeID, hashSecret(code), input.ClientID, grantID, input.RedirectURI, input.CodeChallenge,
		isoMillis(s.now().Add(milliDuration(authorizationCodeLifetimeMs))), now); err != nil {
		return "", err
	}
	if input.Nonce != "" {
		nonceCiphertext, err := EncryptOidcValue(s.KeyEncryptionSecret, map[string]string{"nonce": input.Nonce})
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_authorization_code_oidc_contexts")+`
			(code_id, nonce_ciphertext, created_at) VALUES (?, ?, ?)`), codeID, nonceCiphertext, now); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

// nonceFromCiphertext mirrors nonceFromCiphertext.
func nonceFromCiphertext(secret, value string) (string, error) {
	var payload struct {
		Nonce string `json:"nonce"`
	}
	if err := DecryptOidcValue(secret, value, &payload); err != nil {
		return "", err
	}
	if payload.Nonce == "" {
		return "", errors.New("OIDC nonce 密文内容无效")
	}
	return payload.Nonce, nil
}

// insertAccessToken mirrors insertAccessToken.
func (s *Store) insertAccessToken(ctx context.Context, q queryer, input IssuedToken) (*IssuedToken, error) {
	issuedAt, err := requiredTimestamp(input.Context.IssuedAt)
	if err != nil {
		return nil, err
	}
	expiresAt, err := requiredTimestamp(input.Context.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_access_tokens")+`
		(id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)`),
		input.Context.TokenID, hashSecret(input.AccessToken), input.Context.ClientID, input.Context.GrantID,
		issuedAt, expiresAt, issuedAt); err != nil {
		return nil, err
	}
	input.Context.IssuedAt = issuedAt
	input.Context.ExpiresAt = expiresAt
	return &input, nil
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// issueAccessTokenInTransaction mirrors issueAccessTokenInTransaction.
func (s *Store) issueAccessTokenInTransaction(ctx context.Context, q queryer, grantID, clientID, issuedAt string) (*IssuedToken, error) {
	var grantClientID, systemAccountID, scopesJSON, expiresAt string
	err := q.QueryRowContext(ctx, s.bind(`SELECT client_id, system_account_id, scopes_json, expires_at
		FROM `+s.table("oauth_grants")+` WHERE id = ?`), grantID).
		Scan(&grantClientID, &systemAccountID, &scopesJSON, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("OAuth grant 已失效")
	}
	if err != nil {
		return nil, err
	}
	if grantClientID != clientID {
		return nil, errors.New("OAuth grant 已失效")
	}
	expiresAtMs, err := requiredTimestampMS(expiresAt)
	if err != nil {
		return nil, err
	}
	if expiresAtMs <= s.now().UnixMilli() {
		return nil, errors.New("OAuth grant 已失效")
	}
	return s.insertAccessToken(ctx, q, IssuedToken{
		AccessToken: randomBase64URLBytes(32),
		Context: AccessTokenContext{
			TokenID: newUUIDv4(), ClientID: clientID, GrantID: grantID, SystemAccountID: systemAccountID,
			Scopes: parseStringArray(scopesJSON), IssuedAt: issuedAt, ExpiresAt: expiresAt,
		},
	})
}

// ExchangeAuthorizationCode mirrors exchangeAuthorizationCode: validates the
// one-time code against the client, redirect URI and PKCE challenge, then
// consumes it and issues an access token bound to the grant lifetime.
func (s *Store) ExchangeAuthorizationCode(ctx context.Context, clientID, code, redirectURI, codeVerifier string) (*IssuedToken, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var codeID, grantID, codeClientID, storedRedirectURI, codeChallenge string
	var nonceCiphertext sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT codes.id, codes.client_id, codes.grant_id, codes.redirect_uri,
			codes.code_challenge, contexts.nonce_ciphertext
		FROM `+s.table("oauth_authorization_codes")+` codes
		LEFT JOIN `+s.table("oauth_authorization_code_oidc_contexts")+` contexts ON contexts.code_id = codes.id
		INNER JOIN `+s.table("oauth_grants")+` grants ON grants.id = codes.grant_id
		INNER JOIN `+s.table("oauth_clients")+` clients ON clients.client_id = codes.client_id
		INNER JOIN `+s.table("system_accounts")+` accounts ON accounts.id = grants.system_account_id
		WHERE codes.code_hash = ?
			AND codes.consumed_at IS NULL
			AND codes.expires_at > ?
			AND grants.revoked_at IS NULL
			AND grants.expires_at > ?
			AND clients.status = 'active'
			AND accounts.status = 'active'`), hashSecret(code), now, now).
		Scan(&codeID, &codeClientID, &grantID, &storedRedirectURI, &codeChallenge, &nonceCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if codeClientID != clientID || storedRedirectURI != redirectURI || !VerifyPKCE(codeVerifier, codeChallenge) {
		return nil, nil
	}
	// Decrypt before consuming so key failures remain retryable.
	var nonce string
	if nonceCiphertext.Valid {
		nonce, err = nonceFromCiphertext(s.KeyEncryptionSecret, nonceCiphertext.String)
		if err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_authorization_codes")+`
		SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`), now, codeID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	issued, err := s.issueAccessTokenInTransaction(ctx, tx, grantID, clientID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	issued.Nonce = nonce
	return issued, nil
}

// AuthorizationCodeRequestsIdToken mirrors authorizationCodeRequestsIdToken.
func (s *Store) AuthorizationCodeRequestsIdToken(ctx context.Context, clientID, code string) (bool, error) {
	ctx = ensureCtx(ctx)
	var scopesJSON string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT grants.scopes_json
		FROM `+s.table("oauth_authorization_codes")+` codes
		INNER JOIN `+s.table("oauth_grants")+` grants ON grants.id = codes.grant_id
		WHERE codes.code_hash = ? AND codes.client_id = ? AND codes.consumed_at IS NULL`),
		hashSecret(code), clientID).Scan(&scopesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return containsScope(parseStringArray(scopesJSON), "openid"), nil
}

// DeviceAuthorizationRequestsIdToken mirrors deviceAuthorizationRequestsIdToken.
func (s *Store) DeviceAuthorizationRequestsIdToken(ctx context.Context, clientID, deviceCode string) (bool, error) {
	ctx = ensureCtx(ctx)
	var scopesJSON string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT scopes_json FROM `+s.table("oauth_device_authorizations")+`
		WHERE device_code_hash = ? AND client_id = ? AND status IN ('pending', 'approved')`),
		hashSecret(deviceCode), clientID).Scan(&scopesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return containsScope(parseStringArray(scopesJSON), "openid"), nil
}

func containsScope(scopes []string, scope string) bool {
	for _, item := range scopes {
		if item == scope {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Device flow.
// ---------------------------------------------------------------------------

const userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generateUserCode mirrors generateUserCode (8 chars, no 0/O/1/I).
func generateUserCode() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		panic("oidc: random source failed: " + err.Error())
	}
	out := make([]byte, 8)
	for i, b := range bytes {
		out[i] = userCodeAlphabet[int(b)%len(userCodeAlphabet)]
	}
	return string(out)
}

// CreateDeviceAuthorization mirrors createDeviceAuthorization.
func (s *Store) CreateDeviceAuthorization(ctx context.Context, input struct {
	ClientID        string
	Scopes          []string
	Nonce           string
	VerificationURI string
}) (*DeviceAuthorization, string, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	deviceCode := randomBase64URLBytes(32)
	userCode := generateUserCode()
	var nonceCiphertext any
	if input.Nonce != "" {
		ciphertext, err := EncryptOidcValue(s.KeyEncryptionSecret, map[string]string{"nonce": input.Nonce})
		if err != nil {
			return nil, "", err
		}
		nonceCiphertext = ciphertext
	}
	scopesJSON, err := json.Marshal(input.Scopes)
	if err != nil {
		return nil, "", err
	}
	authorization := &DeviceAuthorization{
		ID: newUUIDv4(), ClientID: input.ClientID, UserCode: userCode, VerificationURI: input.VerificationURI,
		Scopes: input.Scopes, Nonce: input.Nonce,
		ExpiresAt: isoMillis(s.now().Add(600 * time.Second)), IntervalSeconds: 5, Status: "pending",
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_device_authorizations")+`
		(id, client_id, device_code_hash, user_code, verification_uri, scopes_json, nonce_ciphertext,
		 expires_at, interval_seconds, last_polled_at, csrf_hash, status, system_account_id,
		 approved_at, denied_at, consumed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, 'pending', NULL, NULL, NULL, NULL, ?)`),
		authorization.ID, authorization.ClientID, hashSecret(deviceCode), userCode, input.VerificationURI,
		string(scopesJSON), nonceCiphertext, authorization.ExpiresAt, authorization.IntervalSeconds, now); err != nil {
		return nil, "", err
	}
	return authorization, deviceCode, nil
}

const deviceAuthColumns = `id, client_id, user_code, verification_uri, scopes_json, expires_at,
	interval_seconds, status, system_account_id, last_polled_at`

func scanDeviceAuthorization(scan func(...any) error) (*DeviceAuthorization, error) {
	var row struct {
		id, clientID, userCode, verificationURI, scopesJSON, expiresAt string
		intervalSeconds                                                int
		status, systemAccountID                                        sql.NullString
		lastPolledAt                                                   sql.NullString
	}
	err := scan(&row.id, &row.clientID, &row.userCode, &row.verificationURI, &row.scopesJSON, &row.expiresAt,
		&row.intervalSeconds, &row.status, &row.systemAccountID, &row.lastPolledAt)
	if err != nil {
		return nil, err
	}
	status := row.status.String
	switch status {
	case "pending", "approved", "denied", "consumed", "expired":
	default:
		return nil, nil
	}
	authorization := &DeviceAuthorization{
		ID: row.id, ClientID: row.clientID, UserCode: row.userCode, VerificationURI: row.verificationURI,
		Scopes: parseStringArray(row.scopesJSON), ExpiresAt: row.expiresAt,
		IntervalSeconds: row.intervalSeconds, Status: status,
	}
	if row.intervalSeconds < 1 {
		authorization.IntervalSeconds = 5
	}
	if _, err := requiredTimestamp(row.expiresAt); err != nil {
		return nil, err
	}
	if row.systemAccountID.Valid && row.systemAccountID.String != "" {
		authorization.SystemAccountID = row.systemAccountID.String
	}
	if row.lastPolledAt.Valid && row.lastPolledAt.String != "" {
		if _, err := requiredTimestamp(row.lastPolledAt.String); err != nil {
			return nil, err
		}
		lastPolled := row.lastPolledAt.String
		authorization.LastPolledAt = &lastPolled
	}
	return authorization, nil
}

// PrepareDeviceAuthorization mirrors prepareDeviceAuthorization: mint and
// store the decision CSRF token for a pending, unexpired user code.
func (s *Store) PrepareDeviceAuthorization(ctx context.Context, userCode string) (*DeviceAuthorization, string, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	csrfToken := randomBase64URLBytes(24)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("oauth_device_authorizations")+`
		WHERE user_code = ? AND status = 'pending' AND expires_at > ?`), strings.ToUpper(strings.TrimSpace(userCode)), now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
		SET csrf_hash = ? WHERE id = ? AND status = 'pending' AND expires_at > ?`),
		hashSecret(csrfToken), id, now); err != nil {
		return nil, "", err
	}
	authorization, err := scanDeviceAuthorization(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+deviceAuthColumns+` FROM `+s.table("oauth_device_authorizations")+`
			WHERE id = ?`), id).Scan(targets...)
	})
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return authorization, csrfToken, nil
}

// DecideDeviceAuthorization mirrors decideDeviceAuthorization.
func (s *Store) DecideDeviceAuthorization(ctx context.Context, userCode, csrfToken, systemAccountID, decision string) (*DeviceAuthorization, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("oauth_device_authorizations")+`
		WHERE user_code = ? AND status = 'pending' AND expires_at > ? AND csrf_hash = ?`),
		strings.ToUpper(strings.TrimSpace(userCode)), now, hashSecret(csrfToken)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := "denied"
	approvedAt, deniedAt := any(nil), any(nil)
	systemAccountArg := any(nil)
	if decision == "allow" {
		status = "approved"
		approvedAt = now
		systemAccountArg = systemAccountID
	} else {
		deniedAt = now
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
		SET status = ?, system_account_id = ?, approved_at = ?, denied_at = ?
		WHERE id = ? AND status = 'pending' AND expires_at > ?`),
		status, systemAccountArg, approvedAt, deniedAt, id, now)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	authorization, err := scanDeviceAuthorization(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+deviceAuthColumns+` FROM `+s.table("oauth_device_authorizations")+`
			WHERE id = ?`), id).Scan(targets...)
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return authorization, nil
}

// DevicePollKind mirrors OAuthDevicePollResult kinds.
type DevicePollKind string

const (
	PollInvalid              DevicePollKind = "invalid"
	PollExpired              DevicePollKind = "expired"
	PollSlowDown             DevicePollKind = "slow_down"
	PollAuthorizationPending DevicePollKind = "authorization_pending"
	PollAccessDenied         DevicePollKind = "access_denied"
	PollInvalidGrant         DevicePollKind = "invalid_grant"
	PollApproved             DevicePollKind = "approved"
)

// DevicePoll mirrors OAuthDevicePollResult.
type DevicePoll struct {
	Kind        DevicePollKind
	AccessToken string
	Context     AccessTokenContext
	Nonce       string
}

// intervalEscalationExpr returns the poll slow-down escalation expression.
// SQLite (Node source) uses the scalar MIN(a, b); PostgreSQL has no two-arg
// min and needs LEAST.
func (s *Store) intervalEscalationExpr() string {
	if s.pg {
		return "LEAST(interval_seconds + 5, 60)"
	}
	return "MIN(interval_seconds + 5, 60)"
}

// PollDeviceAuthorization mirrors pollDeviceAuthorization (slow-down
// escalation, expiry transition, one-time consumption).
func (s *Store) PollDeviceAuthorization(ctx context.Context, clientID, deviceCode string) (*DevicePoll, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	nowMs := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var row struct {
		id, clientID, scopesJSON, expiresAt             string
		nonceCiphertext, status, systemAccountID        sql.NullString
		intervalSeconds                                 int
		lastPolledAt                                    sql.NullString
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, client_id, scopes_json, expires_at, nonce_ciphertext,
			interval_seconds, last_polled_at, status, system_account_id
		FROM `+s.table("oauth_device_authorizations")+` WHERE device_code_hash = ?`), hashSecret(deviceCode)).
		Scan(&row.id, &row.clientID, &row.scopesJSON, &row.expiresAt, &row.nonceCiphertext,
			&row.intervalSeconds, &row.lastPolledAt, &row.status, &row.systemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return &DevicePoll{Kind: PollInvalid}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.clientID != clientID {
		return &DevicePoll{Kind: PollInvalid}, nil
	}
	status := row.status.String
	if status == "consumed" {
		return &DevicePoll{Kind: PollInvalidGrant}, nil
	}
	expiresAtMs, err := requiredTimestampMS(row.expiresAt)
	if err != nil {
		return nil, err
	}
	if expiresAtMs <= nowMs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
			SET status = 'expired' WHERE id = ? AND status IN ('pending', 'approved')`), row.id); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &DevicePoll{Kind: PollExpired}, nil
	}
	if status == "denied" {
		return &DevicePoll{Kind: PollAccessDenied}, nil
	}
	intervalSeconds := row.intervalSeconds
	if intervalSeconds < 1 {
		intervalSeconds = 5
	}
	if row.lastPolledAt.Valid && row.lastPolledAt.String != "" {
		lastPolledMs, err := requiredTimestampMS(row.lastPolledAt.String)
		if err == nil && lastPolledMs+int64(intervalSeconds)*1_000 > nowMs {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
				SET interval_seconds = `+s.intervalEscalationExpr()+`, last_polled_at = ?
				WHERE id = ? AND status IN ('pending', 'approved')`), now, row.id); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &DevicePoll{Kind: PollSlowDown}, nil
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
		SET last_polled_at = ? WHERE id = ?`), now, row.id); err != nil {
		return nil, err
	}
	if status == "pending" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &DevicePoll{Kind: PollAuthorizationPending}, nil
	}
	if status != "approved" || !row.systemAccountID.Valid || row.systemAccountID.String == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &DevicePoll{Kind: PollInvalidGrant}, nil
	}
	var accountID string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+`
		WHERE id = ? AND status = 'active'`), row.systemAccountID.String).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &DevicePoll{Kind: PollInvalidGrant}, nil
	}
	if err != nil {
		return nil, err
	}
	// Decrypt before mutating the one-time device authorization.
	var nonce string
	if row.nonceCiphertext.Valid && row.nonceCiphertext.String != "" {
		nonce, err = nonceFromCiphertext(s.KeyEncryptionSecret, row.nonceCiphertext.String)
		if err != nil {
			return nil, err
		}
	}
	grantID := newUUIDv4()
	grantExpiresAt := isoMillis(s.now().Add(milliDuration(grantLifetimeMs)))
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_grants")+`
		(id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?)`),
		grantID, clientID, row.systemAccountID.String, row.scopesJSON, grantExpiresAt, now); err != nil {
		return nil, err
	}
	issued, err := s.insertAccessToken(ctx, tx, IssuedToken{
		AccessToken: randomBase64URLBytes(32),
		Context: AccessTokenContext{
			TokenID: newUUIDv4(), ClientID: clientID, GrantID: grantID, SystemAccountID: row.systemAccountID.String,
			Scopes: parseStringArray(row.scopesJSON), IssuedAt: now, ExpiresAt: grantExpiresAt,
		},
	})
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_device_authorizations")+`
		SET status = 'consumed', consumed_at = ? WHERE id = ? AND status = 'approved' AND consumed_at IS NULL`), now, row.id)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &DevicePoll{Kind: PollInvalidGrant}, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &DevicePoll{Kind: PollApproved, AccessToken: issued.AccessToken, Context: issued.Context, Nonce: nonce}, nil
}

// ---------------------------------------------------------------------------
// Access tokens: lookup, renewal, revocation.
// ---------------------------------------------------------------------------

func (s *Store) tokenContextFromRow(scan func(...any) error) (*AccessTokenContext, error) {
	var row struct {
		tokenID, clientID, grantID, systemAccountID, scopesJSON, issuedAt, expiresAt string
	}
	err := scan(&row.tokenID, &row.clientID, &row.grantID, &row.systemAccountID, &row.scopesJSON, &row.issuedAt, &row.expiresAt)
	if err != nil {
		return nil, err
	}
	issuedAt, err := requiredTimestamp(row.issuedAt)
	if err != nil {
		return nil, err
	}
	expiresAt, err := requiredTimestamp(row.expiresAt)
	if err != nil {
		return nil, err
	}
	return &AccessTokenContext{
		TokenID: row.tokenID, ClientID: row.clientID, GrantID: row.grantID, SystemAccountID: row.systemAccountID,
		Scopes: parseStringArray(row.scopesJSON), IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}, nil
}

func (s *Store) accessTokenJoinQuery(extraWhere string) string {
	return `FROM ` + s.table("oauth_access_tokens") + ` tokens
		INNER JOIN ` + s.table("oauth_grants") + ` grants ON grants.id = tokens.grant_id
		INNER JOIN ` + s.table("oauth_clients") + ` clients ON clients.client_id = tokens.client_id
		INNER JOIN ` + s.table("system_accounts") + ` accounts ON accounts.id = grants.system_account_id` + extraWhere
}

// FindAccessTokenContext mirrors findAccessTokenContext — the delegated API
// and userinfo shared lookup.
func (s *Store) FindAccessTokenContext(ctx context.Context, accessToken string) (*AccessTokenContext, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	query := s.accessTokenJoinQuery(` WHERE tokens.token_hash = ?
		AND tokens.revoked_at IS NULL
		AND tokens.replaced_at IS NULL
		AND tokens.expires_at > ?
		AND grants.revoked_at IS NULL
		AND grants.expires_at > ?
		AND clients.status = 'active'
		AND accounts.status = 'active'`)
	context, err := s.tokenContextFromRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT tokens.id AS token_id, tokens.client_id, tokens.grant_id,
			grants.system_account_id, grants.scopes_json, tokens.issued_at, tokens.expires_at
			`+query), hashSecret(accessToken), now, now).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return context, err
}

// RenewedToken mirrors the rotateAccessToken success payload.
type RenewedToken struct {
	AccessToken string
	Context     AccessTokenContext
}

// RotateAccessToken mirrors rotateAccessToken. Returns (nil, false, nil) when
// the token is unknown/invalid, (nil, true, nil) inside the 72h no-renewal
// window, or the replacement token.
func (s *Store) RotateAccessToken(ctx context.Context, clientID, currentAccessToken string) (*RenewedToken, bool, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	nowMs := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	query := s.accessTokenJoinQuery(` WHERE tokens.token_hash = ?
		AND tokens.revoked_at IS NULL
		AND tokens.replaced_at IS NULL
		AND tokens.expires_at > ?
		AND grants.revoked_at IS NULL
		AND grants.expires_at > ?
		AND clients.status = 'active'
		AND accounts.status = 'active'`)
	var row struct {
		tokenID, clientID, grantID, systemAccountID, scopesJSON, issuedAt, expiresAt string
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT tokens.id AS token_id, tokens.client_id, tokens.grant_id,
		grants.system_account_id, grants.scopes_json, tokens.issued_at, tokens.expires_at
		`+query), hashSecret(currentAccessToken), now, now).
		Scan(&row.tokenID, &row.clientID, &row.grantID, &row.systemAccountID, &row.scopesJSON, &row.issuedAt, &row.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.clientID != clientID {
		return nil, false, nil
	}
	issuedAtMs, err := requiredTimestampMS(row.issuedAt)
	if err != nil {
		return nil, false, err
	}
	if issuedAtMs+tokenRenewalDelayMs > nowMs {
		return nil, true, nil
	}
	tokenID := newUUIDv4()
	accessToken := randomBase64URLBytes(32)
	expiresAt, err := requiredTimestamp(row.expiresAt)
	if err != nil {
		return nil, false, err
	}
	inserted, err := s.insertAccessToken(ctx, tx, IssuedToken{
		AccessToken: accessToken,
		Context: AccessTokenContext{
			TokenID: tokenID, ClientID: row.clientID, GrantID: row.grantID, SystemAccountID: row.systemAccountID,
			Scopes: parseStringArray(row.scopesJSON), IssuedAt: now, ExpiresAt: expiresAt,
		},
	})
	if err != nil {
		return nil, false, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_access_tokens")+`
		SET replaced_at = ?, successor_token_id = ? WHERE id = ? AND replaced_at IS NULL AND revoked_at IS NULL`),
		now, tokenID, row.tokenID)
	if err != nil {
		return nil, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, false, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &RenewedToken{AccessToken: inserted.AccessToken, Context: inserted.Context}, false, nil
}

// RevokeAccessToken mirrors revokeAccessToken.
func (s *Store) RevokeAccessToken(ctx context.Context, accessToken, clientID string) error {
	ctx = ensureCtx(ctx)
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_access_tokens")+`
		SET revoked_at = ? WHERE token_hash = ? AND client_id = ? AND revoked_at IS NULL`),
		s.nowISO(), hashSecret(accessToken), clientID)
	return err
}
