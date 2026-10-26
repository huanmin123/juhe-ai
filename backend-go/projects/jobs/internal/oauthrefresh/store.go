package oauthrefresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Store is the business database access for the J4 job family. The
// jobs process owns the narrow credential writers (rotation CAS + keepalive
// merge writer) and the maintenance queries; it never routes gateway traffic.
//
// 行以 juhe_business schema 限定，`?` 占位符改写为 $n（PG 唯一形态）。
type Store struct {
	db     *sql.DB
	secret string
	now    func() time.Time
}

// OpenStore wraps an opened database handle. secret is the Node
// runtimeConfig.secret material: credentials sealed by Node stay decryptable.
// The handle is owned by the caller (Close only closes via CloseHandle).
func OpenStore(db *sql.DB, secret string) (*Store, error) {
	if db == nil {
		return nil, errors.New("oauthrefresh store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("oauthrefresh store requires the runtime secret")
	}
	return &Store{db: db, secret: secret, now: func() time.Time { return time.Now() }}, nil
}

// WithClock overrides the store clock (tests).
func (s *Store) WithClock(clock func() time.Time) *Store {
	if clock != nil {
		s.now = clock
	}
	return s
}

// Close closes the underlying handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) table(name string) string {
	return "juhe_business." + name
}

func (s *Store) bind(query string) string {
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

// nowISO renders the store clock as Node nowIso().
func (s *Store) nowISO() string { return isoMillis(s.now()) }

// ---------------------------------------------------------------------------
// OpenAI refresh candidates
// ---------------------------------------------------------------------------

// RefreshCandidate mirrors OpenAIOAuthRefreshCandidateResult: either a
// decryptable account or a decrypt-failure marker (the local_configuration
// path).
type RefreshCandidate struct {
	Account *RotationAccount // nil for decrypt failures
	// decrypt failure payload
	AccountID                     string
	AccountName                   string
	AccountStatus                 string
	ConfigRevision                int64
	ErrorCode                     string
	ErrorMessage                  string
	AccountLocalEvidenceConfirmed bool
	// ObservedFailure carries the failure state read during batch selection so
	// the success path clears exactly the record it saw.
	ObservedFailure *RefreshFailureState
}

// IsDecryptFailure reports whether the candidate failed credential decryption.
func (c RefreshCandidate) IsDecryptFailure() bool { return c.Account == nil }

// openAIProfileIDs mirrors openAIProtocolProfileIdsForQuery: enabled OpenAI
// protocol profiles, falling back to the pinned GPT profile constant.
// 供应商解耦后（设计 §9.3/§9.11）profile 表退役，OpenAI 刷新候选身份收敛为
// (vendor_code='gpt', type='oauth')——该组合在 §1.1 registry 中恰好绑定唯一
// gpt_openai_v1 预设，不再需要 profile ID 列表门。
//
// ListDueOpenAIRefreshAccounts mirrors
// listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync. leadSeconds/dueBefore
// semantics and the ordering contract stay byte-identical.
func (s *Store) ListDueOpenAIRefreshAccounts(ctx context.Context, leadSeconds int, limit int, stoppedErrorCode string, now time.Time) ([]RefreshCandidate, error) {
	dueBefore := isoMillis(now.Add(time.Duration(leadSeconds) * time.Second))
	limit = clampInt(limit, 1, 500)
	query := `SELECT id, system_account_id, COALESCE(vendor_code, ''), protocol_code, protocol_version, name, type, status, credentials_encrypted,
		proxy_profile_id, concurrency_limit, priority,
		super_priority_enabled, fallback_enabled, schedulable, account_expires_at, cooldown_until,
		last_error_code, last_error_message, health_check_model, health_check_endpoint_mode, config_revision
	FROM ` + s.table("accounts") + `
	WHERE authorization_instance_authorization_id IS NULL
		AND deleted_at IS NULL
		AND COALESCE(vendor_code, '') = ?
		AND type = 'oauth'
		AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> ?)
		AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
	ORDER BY oauth_refresh_token_present ASC,
		(oauth_access_token_expires_at IS NOT NULL) ASC,
		oauth_access_token_expires_at ASC,
		updated_at ASC,
		id ASC
	LIMIT ?`
	args := []any{ProviderGPT, stoppedErrorCode, dueBefore, limit}
	return s.listRefreshCandidates(ctx, query, args...)
}

func (s *Store) listRefreshCandidates(ctx context.Context, query string, args ...any) ([]RefreshCandidate, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []RefreshCandidate{}
	for rows.Next() {
		var (
			id               string
			systemAccountID  string
			providerCode     string
			protocolCode     string
			protocolVersion  string
			name             string
			accountType      string
			status           string
			encrypted        string
			proxyProfileID   sql.NullString
			accountExpiresAt sql.NullString
			lastErrorCode    sql.NullString
			lastErrorMessage sql.NullString
			configRevision   int64
		)
		var concurrencyLimit, priority, superPriority, fallbackEnabled, schedulable, cooldownUntil any
		var healthCheckModel, healthCheckEndpointMode any
		if err := rows.Scan(&id, &systemAccountID, &providerCode, &protocolCode, &protocolVersion, &name, &accountType, &status, &encrypted,
			&proxyProfileID, &concurrencyLimit, &priority,
			&superPriority, &fallbackEnabled, &schedulable, &accountExpiresAt, &cooldownUntil,
			&lastErrorCode, &lastErrorMessage, &healthCheckModel, &healthCheckEndpointMode, &configRevision); err != nil {
			return nil, err
		}
		if configRevision < 1 {
			configRevision = 1
		}
		credentials := map[string]any{}
		if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
			candidates = append(candidates, RefreshCandidate{
				AccountID:      id,
				AccountName:    name,
				AccountStatus:  status,
				ConfigRevision: configRevision,
				ErrorCode:      "oauth_credentials_decrypt_failed",
				ErrorMessage:   "本地 OpenAI OAuth 账户凭据无法解密，请重新授权或更新凭据",
			})
			continue
		}
		candidates = append(candidates, RefreshCandidate{Account: &RotationAccount{
			ID: id, SystemAccountID: systemAccountID,
			ProviderCode: providerCode,
			ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
			Name: name, Type: accountType, Status: status,
			LastErrorCode:    lastErrorCode.String,
			ProxyProfileID:   proxyProfileID.String,
			Credentials:      credentials,
			AccountExpiresAt: accountExpiresAt.String,
			ConfigRevision:   configRevision,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ---------------------------------------------------------------------------
// Rotation account + CAS credential writers
// ---------------------------------------------------------------------------

// RotationAccount mirrors OAuthCredentialRotationAccount. ProviderCode 承载
// 目标 schema 的 vendor_code 身份值（§2.1：真实厂商 vendor_code ==
// provider_code；§9.11 目标列集无 provider_code / provider_protocol_profile_id，
// profile 维度已删除）。
type RotationAccount struct {
	ID               string
	SystemAccountID  string
	ProviderCode     string
	ProtocolCode     string
	ProtocolVersion  string
	Name             string
	Type             string
	Status           string
	LastErrorCode    string
	ProxyProfileID   string
	Credentials      map[string]any
	AccountExpiresAt string
	ConfigRevision   int64
	UpdatedAt        string
}

// CredentialsUnavailableError marks an account whose sealed credentials are
// absent or undecryptable (Node decryptJson throw path).
type CredentialsUnavailableError struct{}

func (e *CredentialsUnavailableError) Error() string { return "OAuth 凭据读取失败" }

// RevisionConflictError maps to AccountConfigRevisionConflictError.
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// FindRotationAccount mirrors findOAuthCredentialRotationAccountAsync (jobs
// internal access crosses owners, so no system_account scope is applied — the
// Node internal access sys_admin/super_admin is owner-free).
func (s *Store) FindRotationAccount(ctx context.Context, accountID string) (*RotationAccount, error) {
	var (
		id               string
		systemAccountID  string
		providerCode     string
		protocolCode     string
		protocolVersion  string
		name             string
		accountType      string
		status           string
		lastErrorCode    sql.NullString
		proxyProfileID   sql.NullString
		encrypted        string
		accountExpiresAt sql.NullString
		configRevision   int64
		updatedAt        string
	)
	// 身份读键 = COALESCE(vendor_code,'')（§9.11 目标列集无 provider_code /
	// provider_protocol_profile_id；与 gateway oauthmgmt T30 轮换读面同口径）。
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, COALESCE(vendor_code, ''),
			protocol_code, protocol_version, name, type, status,
			last_error_code, proxy_profile_id, credentials_encrypted,
			account_expires_at, config_revision, updated_at
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
		LIMIT 1`), accountID).Scan(
		&id, &systemAccountID, &providerCode,
		&protocolCode, &protocolVersion, &name, &accountType, &status,
		&lastErrorCode, &proxyProfileID, &encrypted,
		&accountExpiresAt, &configRevision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	credentials := map[string]any{}
	if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	if configRevision < 1 {
		configRevision = 1
	}
	return &RotationAccount{
		ID: id, SystemAccountID: systemAccountID,
		ProviderCode: providerCode,
		ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
		Name: name, Type: accountType, Status: status,
		LastErrorCode:    lastErrorCode.String,
		ProxyProfileID:   proxyProfileID.String,
		Credentials:      credentials,
		AccountExpiresAt: accountExpiresAt.String,
		ConfigRevision:   configRevision, UpdatedAt: updatedAt,
	}, nil
}

// RotateCredentialsInput mirrors rotateOAuthCredentialsAsync input (jobs
// internal access). 身份 CAS 键 = vendor_code（§2.1 行为键，真实厂商 ==
// provider_code）+ account type + config revision；profile 维度已随目标列集
// 删除（§9.11）。
type RotateCredentialsInput struct {
	AccountID              string
	ExpectedConfigRevision int64
	ExpectedProviderCode   string
	ExpectedAccountType    string
	Credentials            map[string]any
}

// RotationResult mirrors OAuthCredentialRotationResult.
type RotationResult struct {
	ID             string
	ConfigRevision int64
	UpdatedAt      string
	Changed        bool
	Credentials    map[string]any
}

// RotateCredentials mirrors rotateOAuthCredentialsAsync: revision CAS,
// credential column swap (sealed envelope + fingerprint + mask + the derived
// oauth_access_token_expires_at / oauth_refresh_token_present columns),
// config_revision + 1 and the unchanged no-op receipt.
func (s *Store) RotateCredentials(ctx context.Context, input RotateCredentialsInput) (*RotationResult, error) {
	ctx = ensureCtx(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, errors.New("账户配置版本无效")
	}
	if expiresAt, exists := input.Credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, errors.New("OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id             string
		providerCode   string
		accountType    string
		encrypted      string
		configRevision int64
	)
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, COALESCE(vendor_code, ''), type, credentials_encrypted, config_revision
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
		LIMIT 1`), input.AccountID).Scan(&id, &providerCode, &accountType, &encrypted, &configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if providerCode != input.ExpectedProviderCode ||
		accountType != input.ExpectedAccountType {
		return nil, nil
	}
	if configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	current := map[string]any{}
	if err := DecryptJSON(s.secret, encrypted, &current); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentialsEqual(current, input.Credentials) {
		return &RotationResult{
			ID: id, ConfigRevision: configRevision,
			UpdatedAt: s.nowISO(), Changed: false, Credentials: input.Credentials,
		}, nil
	}
	source, err := requiredCredentialSource(input.ExpectedAccountType, input.Credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(input.Credentials))
	if err != nil {
		return nil, err
	}
	expiresAt := sql.NullString{}
	if value, exists := input.Credentials["expires_at"]; exists {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if normalizeText(input.Credentials["refresh_token"]) != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND COALESCE(vendor_code, '') = ? AND type = ?
			AND config_revision = ?
			AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt,
		id, providerCode, accountType, configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &RotationResult{
		ID: id, ConfigRevision: configRevision + 1,
		UpdatedAt: updatedAt, Changed: true, Credentials: input.Credentials,
	}, nil
}

// UpdateAccountCredentials mirrors updateAccountAsync({credentials}, …,
// {expectedConfigRevision}) as used by the three-provider dispatch
// preparation: a merge writer that verifies the expected revision and bumps
// config_revision + 1. The caller supplies the fully merged credentials; the
// derived columns refresh exactly like the rotation path.
func (s *Store) UpdateAccountCredentials(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error) {
	ctx = ensureCtx(ctx)
	if expectedConfigRevision < 1 {
		return nil, errors.New("账户配置版本无效")
	}
	if expiresAt, exists := credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, errors.New("OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var (
		id             string
		configRevision int64
	)
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, config_revision FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL LIMIT 1`), accountID).Scan(&id, &configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if configRevision != expectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	accountType := "oauth"
	source, err := requiredCredentialSource(accountType, credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(credentials))
	if err != nil {
		return nil, err
	}
	expiresAt := sql.NullString{}
	if value, exists := credentials["expires_at"]; exists {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if normalizeText(credentials["refresh_token"]) != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt, id, configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.FindRotationAccount(ctx, accountID)
}

// MarkAccountFailureState mirrors mark_openai_oauth_local_configuration_exception:
// flips an active account into the terminal refresh-failure error state guarded
// by the config revision and the current status. updated=false when the guard
// misses.
func (s *Store) MarkAccountFailureState(ctx context.Context, accountID, errorCode, reason string, expectedConfigRevision int64, expectedStatus string) (bool, error) {
	ctx = ensureCtx(ctx)
	updatedAt := s.nowISO()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'error', last_error_code = ?, last_error_message = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND config_revision = ? AND status = ? AND deleted_at IS NULL`),
		errorCode, reason, updatedAt, accountID, expectedConfigRevision, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// ClearAccountFailureState mirrors clear_account_failure_state with the
// expectedLastErrorCodes guard: the account returns to active when the current
// last_error_code is one of the managed codes. changed reports the CAS guard.
// 守卫谓词为 status = 'error' AND last_error_code IN (managed codes)；空码集
// 时 IN 守卫恒不可满足（PG 不支持空 IN 列表），按 CAS 未命中返回，不发
// UPDATE。占位符数（updated_at + id + n 个码）必须与参数数严格一致——pgx
// 严格校验，多余参数即 mismatched param and argument count。
func (s *Store) ClearAccountFailureState(ctx context.Context, accountID string, expectedLastErrorCodes []string) (changed bool, accountStatus string, err error) {
	ctx = ensureCtx(ctx)
	if len(expectedLastErrorCodes) == 0 {
		return false, "", nil
	}
	guards := make([]string, len(expectedLastErrorCodes))
	args := []any{s.nowISO(), accountID}
	for i, code := range expectedLastErrorCodes {
		guards[i] = "?"
		args = append(args, code)
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'active', last_error_code = NULL, last_error_message = NULL,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND status = 'error' AND last_error_code IN (`+strings.Join(guards, ", ")+`)
			AND deleted_at IS NULL`), args...)
	if err != nil {
		return false, "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if affected != 1 {
		return false, "", nil
	}
	var status string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&status); err != nil {
		return true, "", err
	}
	return true, status, nil
}

// ---------------------------------------------------------------------------
// Keepalive due list (anthropic / gemini / grok)
// ---------------------------------------------------------------------------

// KeepaliveCandidate is one due keepalive account.
type KeepaliveCandidate struct {
	Account *RotationAccount
}

// ListDueKeepaliveAccounts mirrors the dispatch-preparation refresh window as a
// batch query: oauth accounts of the vendor whose derived
// oauth_access_token_expires_at is missing or within the lead window. gemini
// accounts use type google_oauth；身份键为 COALESCE 后的 vendor_code（§2.1
// 行为键；§9.11 目标列集无 provider_code / provider_protocol_profile_id，
// profile 维度门已删除——vendor+type 在 §1.1 registry 已唯一绑定预设）。
func (s *Store) ListDueKeepaliveAccounts(ctx context.Context, providerCode, accountType string, lead time.Duration, limit int, now time.Time) ([]KeepaliveCandidate, error) {
	dueBefore := isoMillis(now.Add(lead))
	limit = clampInt(limit, 1, 500)
	query := `SELECT id, system_account_id, COALESCE(vendor_code, ''), protocol_code, protocol_version, name, type, status, credentials_encrypted,
		proxy_profile_id, account_expires_at, last_error_code, config_revision, updated_at
	FROM ` + s.table("accounts") + `
	WHERE authorization_instance_authorization_id IS NULL
		AND deleted_at IS NULL
		AND COALESCE(vendor_code, '') = ?
		AND type = ?
		AND (status <> 'error' OR last_error_code IS NULL OR last_error_code NOT IN ('oauth_token_refresh_failed', 'oauth_token_refresh_local_configuration_invalid'))
		AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
		AND oauth_refresh_token_present = 1`
	args := []any{providerCode, accountType, dueBefore}
	query += `
	ORDER BY oauth_access_token_expires_at IS NOT NULL ASC, oauth_access_token_expires_at ASC, updated_at ASC, id ASC
	LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []KeepaliveCandidate{}
	for rows.Next() {
		var (
			id              string
			systemAccountID string
			rowProviderCode string
			protocolCode    string
			protocolVersion string
			name            string
			rowAccountType  string
			status          string
			encrypted       string
			proxyProfileID  sql.NullString
			accountExpires  sql.NullString
			lastErrorCode   sql.NullString
			configRevision  int64
			updatedAt       string
		)
		if err := rows.Scan(&id, &systemAccountID, &rowProviderCode, &protocolCode, &protocolVersion, &name, &rowAccountType, &status, &encrypted,
			&proxyProfileID, &accountExpires, &lastErrorCode, &configRevision, &updatedAt); err != nil {
			return nil, err
		}
		if configRevision < 1 {
			configRevision = 1
		}
		credentials := map[string]any{}
		if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
			// Keepalive skips undecryptable rows; the OpenAI refresh family owns
			// the local-configuration terminal path and the sweep stays readable.
			continue
		}
		candidates = append(candidates, KeepaliveCandidate{Account: &RotationAccount{
			ID: id, SystemAccountID: systemAccountID,
			ProviderCode: rowProviderCode,
			ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
			Name: name, Type: rowAccountType, Status: status,
			LastErrorCode:    lastErrorCode.String,
			ProxyProfileID:   proxyProfileID.String,
			Credentials:      credentials,
			AccountExpiresAt: accountExpires.String,
			ConfigRevision:   configRevision, UpdatedAt: updatedAt,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// credentialsEqual mirrors isDeepStrictEqual over the JSON-normalized
// credential maps (canonical re-encode comparison).
func credentialsEqual(current, next map[string]any) bool {
	encode := func(value map[string]any) (string, bool) {
		raw, err := json.Marshal(value)
		return string(raw), err == nil
	}
	left, leftOK := encode(current)
	right, rightOK := encode(next)
	return leftOK && rightOK && left == right
}

// requiredCredentialSource mirrors requiredAccountCredentialSource: the secret
// field that backs the fingerprint/mask columns per account type.
func requiredCredentialSource(accountType string, credentials map[string]any) (string, error) {
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := normalizeText(credentials[key]); value != "" {
				return value
			}
		}
		return ""
	}
	switch accountType {
	case "oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("OAuth 凭据不能为空")
	case "api_key":
		if value := normalizeText(credentials["api_key"]); value != "" {
			return value, nil
		}
		return "", errors.New("API Key 不能为空")
	case "google_oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("Google OAuth 凭据不能为空")
	default:
		if value := pick("api_key", "refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("账户凭据不能为空")
	}
}

// stringCredential mirrors stringCredential: trimmed string credential or "".
func stringCredential(credentials map[string]any, key string) string {
	return normalizeText(credentials[key])
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
