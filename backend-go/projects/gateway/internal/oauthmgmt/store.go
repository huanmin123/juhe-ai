// Package oauthmgmt owns the M17 vertical slice: the four-provider OAuth
// management route family ported from backend/src/modules/{openai,anthropic,
// gemini,grok}-oauth (dual-mounted as /__aisys__/api/{provider}-oauth for
// admins and /__aisys__/api/my-{provider}-oauth under forceSelfAccessScope).
// The slice covers authorize-URL sessions (state/nonce/PKCE per provider),
// create-from-code and create-from-refresh-token account creation on the M08
// accounts store, manual access-token refresh and reauthorization through the
// oauth-credential-rotation CAS (config_revision + 1), the grok SSO device
// flow, the gemini capabilities document and the operation-log emission for
// every mutation. Upstream token endpoints are reached only through the
// injected TokenExchanger; the grok device flow through SSODeviceRequester, so
// tests run with httptest/mock transports and never touch the network.
package oauthmgmt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// ConflictError maps to the route family 409 paths (owner-scoped duplicate
// account names surface through the accounts slice with the same shape).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the 400 mutation paths (provider/profile checks,
// revision validation, missing refresh tokens).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RevisionConflictError maps to AccountConfigRevisionConflictError: 409 with
// the route-specific copy.
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// Provider codes and protocol profile IDs mirror domain/provider-protocol.ts.
const (
	ProviderGPT       = "gpt"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
	ProviderXAI       = "xai"

	ProfileGPTOpenAIV1        = "profile_gpt_openai_v1"
	ProfileXAIOpenAIV1        = "profile_xai_openai_v1"
	ProfileAnthropicV1        = "profile_anthropic_anthropic_v1"
	ProfileGeminiNativeV1Beta = "profile_gemini_native_v1beta"
)

// Store is the dual-mode OAuth account persistence. secret is the Node
// runtimeConfig.secret material: accounts.credentials_encrypted rows written
// by Node stay decryptable. exchanger may be nil only together with an
// explicitly injected transport at the route layer; NewStore rejects nil.
type Store struct {
	db           *sql.DB
	pg           bool
	secret       string
	now          func() time.Time
	newI         func(prefix string) string
	sessions     *sessionStore
	Accounts     *accounts.Store
	exchanger    TokenExchanger
	ssoRequester SSODeviceRequester
	ssoSleep     func(ctx context.Context, delay time.Duration) error
}

// Option configures optional collaborators.
type Option func(*Store)

// WithSSODeviceTransport injects the grok device-flow transport; tests supply
// scripted requesters so the SSO import never leaves the process.
func WithSSODeviceTransport(requester SSODeviceRequester) Option {
	return func(s *Store) {
		if requester != nil {
			s.ssoRequester = requester
		}
	}
}

// WithSSOSleep injects the device-flow poll sleep (tests pass a no-op).
func WithSSOSleep(sleep func(ctx context.Context, delay time.Duration) error) Option {
	return func(s *Store) {
		if sleep != nil {
			s.ssoSleep = sleep
		}
	}
}

// NewStore builds the store. accountsStore is the M08 accounts slice store the
// create routes delegate to; exchanger is the injected upstream transport.
func NewStore(db *sql.DB, postgres bool, secret string, accountsStore *accounts.Store, exchanger TokenExchanger, now func() time.Time, newID func(string) string, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("oauthmgmt store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("oauthmgmt store requires the runtime secret")
	}
	if accountsStore == nil {
		return nil, errors.New("oauthmgmt store requires the accounts store")
	}
	if exchanger == nil {
		return nil, errors.New("oauthmgmt store requires a token exchanger")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	store := &Store{
		db: db, pg: postgres, secret: secret, now: now, newI: newID,
		sessions: newSessionStore(now), Accounts: accountsStore, exchanger: exchanger,
	}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

// ssoDeps renders the device-flow collaborators: the injected (or default)
// transport, the injected (or default) sleep and the store clock.
func (s *Store) ssoDeps() SSODeviceDeps {
	requester := s.ssoRequester
	if requester == nil {
		requester = defaultSSODeviceRequester()
	}
	return SSODeviceDeps{Request: requester, Sleep: s.ssoSleep, Now: s.now}
}

// randomID mirrors Node newId(prefix): "{prefix}_{millis}_{8 hex}".
func randomID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

func itoa64(v int64) string {
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

// isoMillis mirrors Node nowIso() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// nowISO renders the store clock.
func (s *Store) nowISO() string { return isoMillis(s.now()) }

// exchange performs one upstream token call through the injected exchanger.
func (s *Store) exchange(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	return s.exchanger.Do(ensureContext(ctx), request)
}

// unmarshalSession decodes a stored session envelope.
func unmarshalSession(raw json.RawMessage, target any) error {
	return json.Unmarshal(raw, target)
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant.
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// AccessScope mirrors storage/access-scope.ts for this slice: admins operate
// across owners unless a systemAccountId filter narrows the view; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// accountsScope renders the M08 access scope for delegated account creation.
func (a AccessScope) accountsScope() accounts.AccessScope {
	return accounts.AccessScope{ViewerID: a.ViewerID, IsAdmin: a.IsAdmin, FilterID: a.FilterID}
}

// providerProfile mirrors resolveXxxOAuthProviderProfile results: the resolved
// profile row plus the provider default supported models.
type providerProfile struct {
	ID                     string
	ProviderCode           string
	Name                   string
	ProtocolCode           string
	ProtocolVersion        string
	DefaultSupportedModels []string
}

// resolveProviderProfile mirrors resolveOpenAI/Anthropic/Gemini/GrokOAuthProviderProfile:
// provider lookup + enabled check, profile id match + enabled check, protocol
// match, account-type membership and (grok) the exact profile id pin.
func (s *Store) resolveProviderProfile(ctx context.Context, providerCode, profileID, accountType, requiredProfileID string) (*providerProfile, error) {
	var row struct {
		id                     sql.NullString
		name                   sql.NullString
		enabled                sql.NullInt64
		protocolCode           sql.NullString
		protocolVersion        sql.NullString
		accountTypesJSON       sql.NullString
		defaultSupportedModels sql.NullString
		providerEnabled        sql.NullInt64
	}
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT ppp.id, ppp.name, ppp.enabled,
			ppp.protocol_code, ppp.protocol_version, ppp.account_types_json,
			p.default_supported_models_json, p.enabled AS provider_enabled
		FROM `+s.table("providers")+` p
		LEFT JOIN `+s.table("provider_protocol_profiles")+` ppp
			ON ppp.provider_code = p.code
			AND ppp.id = ?
		WHERE p.code = ?
		LIMIT 1`), profileID, providerCode).Scan(
		&row.id, &row.name, &row.enabled,
		&row.protocolCode, &row.protocolVersion, &row.accountTypesJSON,
		&row.defaultSupportedModels, &row.providerEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "不支持的供应商：" + providerCode}
	}
	if err != nil {
		return nil, err
	}
	if !row.providerEnabled.Valid || row.providerEnabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商已停用：" + providerCode}
	}
	profileID = strings.TrimSpace(profileID)
	if !row.id.Valid || row.id.String == "" {
		return nil, &ValidationError{Message: "供应商协议档案无效：" + profileID}
	}
	if !row.enabled.Valid || row.enabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商协议档案已停用：" + row.name.String}
	}
	if row.protocolCode.String != protocolCodeForProvider(providerCode) || row.protocolVersion.String != protocolVersionForProvider(providerCode) {
		return nil, &ValidationError{Message: "供应商协议档案 " + row.name.String + " 不支持 " + oauthLabelForProvider(providerCode) + " OAuth"}
	}
	if requiredProfileID != "" && row.id.String != requiredProfileID {
		return nil, &ValidationError{Message: "供应商协议档案 " + row.name.String + " 不支持 " + oauthLabelForProvider(providerCode) + " OAuth"}
	}
	accountTypes := []string{}
	if row.accountTypesJSON.Valid && strings.TrimSpace(row.accountTypesJSON.String) != "" {
		_ = json.Unmarshal([]byte(row.accountTypesJSON.String), &accountTypes)
	}
	supported := false
	for _, candidate := range accountTypes {
		if candidate == accountType {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &ValidationError{Message: "供应商协议档案 " + row.name.String + " 不支持 " + oauthLabelForProvider(providerCode) + " OAuth"}
	}
	profile := &providerProfile{
		ID: row.id.String, ProviderCode: providerCode, Name: row.name.String,
		ProtocolCode: row.protocolCode.String, ProtocolVersion: row.protocolVersion.String,
		DefaultSupportedModels: []string{},
	}
	if row.defaultSupportedModels.Valid && strings.TrimSpace(row.defaultSupportedModels.String) != "" {
		_ = json.Unmarshal([]byte(row.defaultSupportedModels.String), &profile.DefaultSupportedModels)
	}
	return profile, nil
}

// protocolCodeForProvider mirrors the per-provider protocol expectations.
func protocolCodeForProvider(providerCode string) string {
	if providerCode == ProviderGemini {
		return "gemini"
	}
	if providerCode == ProviderAnthropic {
		return "anthropic"
	}
	return "openai"
}

func protocolVersionForProvider(providerCode string) string {
	if providerCode == ProviderGemini {
		return "v1beta"
	}
	return "v1"
}

// oauthLabelForProvider mirrors the route copy labels ("OpenAI", "Anthropic",
// "Gemini", "Grok").
func oauthLabelForProvider(providerCode string) string {
	switch providerCode {
	case ProviderGPT:
		return "OpenAI"
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderGemini:
		return "Gemini"
	case ProviderXAI:
		return "Grok"
	}
	return providerCode
}

// groupProviderOK mirrors isOpenAIOAuthGroupSummary & friends: the bound group
// must exist in scope and carry the provider code.
func (s *Store) findGroupForProvider(ctx context.Context, groupID string, access AccessScope, providerCode string) (bool, error) {
	where := ""
	args := []any{groupID}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var rowProviderCode string
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT provider_code FROM `+s.table("groups")+`
		WHERE id = ?`+where+` LIMIT 1`), args...).Scan(&rowProviderCode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rowProviderCode == providerCode, nil
}

// CreateAccountInput bundles everything the OAuth create routes hand to the
// M08 accounts store.
type CreateAccountInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	AccountType               string
	Credentials               map[string]any
	Status                    string
	ConcurrencyLimit          *int
	Priority                  *int
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	ModelMappings             []accounts.ModelMapping
	Tags                      []string
	ProxyProfileID            *string
	GroupID                   *string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
}

// CreateOAuthAccount mirrors createAccountAsync on the M08 slice: profile
// defaults for supported models, accountCreationStatusInput semantics and the
// delegated owner-scoped insert with group binding.
func (s *Store) CreateOAuthAccount(ctx context.Context, input CreateAccountInput, access AccessScope) (*accounts.CreateResult, error) {
	supportedModels := input.SupportedModels
	if len(supportedModels) == 0 {
		profile, err := s.resolveProviderProfile(ctx, input.ProviderCode, input.ProviderProtocolProfileID, input.AccountType, requiredProfileForProvider(input.ProviderCode))
		if err != nil {
			return nil, err
		}
		supportedModels = profile.DefaultSupportedModels
	}
	creationStatus := accounts.AccountCreationStatusInput(input.Status)
	result, err := s.Accounts.Create(ensureContext(ctx), accounts.CreateInput{
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		Name:                      input.Name,
		AccountType:               input.AccountType,
		Credentials:               accounts.Credentials(input.Credentials),
		SupportedModels:           supportedModels,
		HealthCheckModel:          input.HealthCheckModel,
		HealthCheckEndpointMode:   input.HealthCheckEndpointMode,
		ModelMappings:             input.ModelMappings,
		Tags:                      input.Tags,
		Status:                    creationStatus,
		ConcurrencyLimit:          input.ConcurrencyLimit,
		Priority:                  input.Priority,
		SuperPriorityEnabled:      input.SuperPriorityEnabled,
		FallbackEnabled:           input.FallbackEnabled,
		ProxyProfileID:            input.ProxyProfileID,
		GroupID:                   input.GroupID,
		AccountExpiresAt:          input.AccountExpiresAt,
		AvailabilitySchedule:      input.AvailabilitySchedule,
		Notes:                     input.Notes,
	}, access.accountsScope())
	if err != nil {
		var conflict *accounts.ConflictError
		if errors.As(err, &conflict) {
			return nil, &ConflictError{Message: conflict.Message}
		}
		var validation *accounts.ValidationError
		if errors.As(err, &validation) {
			return nil, &ValidationError{Message: validation.Message}
		}
		return nil, err
	}
	return result, nil
}

// requiredProfileForProvider mirrors the grok profile pin (XAI_OPENAI_V1 only).
func requiredProfileForProvider(providerCode string) string {
	if providerCode == ProviderXAI {
		return ProfileXAIOpenAIV1
	}
	return ""
}

// rotationAccount mirrors OAuthCredentialRotationAccount.
type rotationAccount struct {
	ID                        string
	SystemAccountID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	Status                    string
	LastErrorCode             string
	ProxyProfileID            string
	Credentials               map[string]any
	CredentialFingerprint     string
	AccountExpiresAt          string
	ConfigRevision            int64
	UpdatedAt                 string
}

// CredentialsUnavailableError marks a scope-matched account whose sealed
// credentials are absent or undecryptable (Node decryptJson throw path).
type CredentialsUnavailableError struct{}

func (e *CredentialsUnavailableError) Error() string { return "OAuth 凭据读取失败" }

// findRotationAccount mirrors findOAuthCredentialRotationAccountAsync.
func (s *Store) findRotationAccount(ctx context.Context, accountID string, access AccessScope) (*rotationAccount, error) {
	where := ""
	args := []any{accountID}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id               string
		systemAccountID  string
		providerCode     string
		profileID        string
		protocolCode     string
		protocolVersion  string
		name             string
		accountType      string
		status           string
		lastErrorCode    sql.NullString
		proxyProfileID   sql.NullString
		encrypted        string
		fingerprint      sql.NullString
		accountExpiresAt sql.NullString
		configRevision   int64
		updatedAt        string
	}
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT id, system_account_id, provider_code,
			provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
			last_error_code, proxy_profile_id, credentials_encrypted, credential_fingerprint,
			account_expires_at, config_revision, updated_at
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL`+where+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.systemAccountID, &row.providerCode,
		&row.profileID, &row.protocolCode, &row.protocolVersion, &row.name, &row.accountType,
		&row.status, &row.lastErrorCode, &row.proxyProfileID, &row.encrypted, &row.fingerprint,
		&row.accountExpiresAt, &row.configRevision, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	credentials := map[string]any{}
	if err := decryptJSON(s.secret, row.encrypted, &credentials); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	if row.configRevision < 1 {
		row.configRevision = 1
	}
	return &rotationAccount{
		ID: row.id, SystemAccountID: row.systemAccountID,
		ProviderCode: row.providerCode, ProviderProtocolProfileID: row.profileID,
		ProtocolCode: row.protocolCode, ProtocolVersion: row.protocolVersion,
		Name: row.name, Type: row.accountType, Status: row.status,
		LastErrorCode: row.lastErrorCode.String, ProxyProfileID: row.proxyProfileID.String,
		Credentials: credentials, CredentialFingerprint: row.fingerprint.String,
		AccountExpiresAt: row.accountExpiresAt.String,
		ConfigRevision:   row.configRevision, UpdatedAt: row.updatedAt,
	}, nil
}

// RotationResult mirrors OAuthCredentialRotationResult.
type RotationResult struct {
	ID             string
	ConfigRevision int64
	UpdatedAt      string
	Changed        bool
	Credentials    map[string]any
}

// RotateCredentialsInput mirrors rotateOAuthCredentialsAsync input.
type RotateCredentialsInput struct {
	AccountID                         string
	ExpectedConfigRevision            int64
	ExpectedProviderCode              string
	ExpectedAccountType               string
	ExpectedProviderProtocolProfileID string
	Credentials                       map[string]any
	Access                            AccessScope
}

// RotateCredentials mirrors rotateOAuthCredentialsAsync (the recovery-state
// branch is an M17 deferral): revision CAS, credential column swap,
// config_revision + 1 and the unchanged no-op receipt.
func (s *Store) RotateCredentials(ctx context.Context, input RotateCredentialsInput) (*RotationResult, error) {
	ctx = ensureContext(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, &ValidationError{Message: "账户配置版本无效"}
	}
	if expiresAt, exists := input.Credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, &ValidationError{Message: "OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	where := ""
	args := []any{input.AccountID}
	if scoped := input.Access.manageableID(); scoped != "" {
		where = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id              string
		systemAccountID string
		providerCode    string
		profileID       string
		accountType     string
		encrypted       string
		configRevision  int64
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code,
			provider_protocol_profile_id, type, credentials_encrypted, config_revision
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL`+where+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.systemAccountID, &row.providerCode, &row.profileID, &row.accountType,
		&row.encrypted, &row.configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.providerCode != input.ExpectedProviderCode ||
		row.accountType != input.ExpectedAccountType ||
		row.profileID != input.ExpectedProviderProtocolProfileID {
		return nil, nil
	}
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	current := map[string]any{}
	if err := decryptJSON(s.secret, row.encrypted, &current); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentialsEqual(current, input.Credentials) {
		return &RotationResult{
			ID: row.id, ConfigRevision: row.configRevision,
			UpdatedAt: s.nowISO(), Changed: false, Credentials: input.Credentials,
		}, nil
	}
	source, err := requiredCredentialSource(input.ExpectedAccountType, input.Credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := encryptJSON(s.secret, map[string]any(input.Credentials))
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
	if text := normalizeText(input.Credentials["refresh_token"]); text != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND provider_code = ?
			AND provider_protocol_profile_id = ? AND type = ? AND config_revision = ?
			AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt,
		row.id, row.systemAccountID, row.providerCode, row.profileID, row.accountType, row.configRevision)
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
		ID: row.id, ConfigRevision: row.configRevision + 1,
		UpdatedAt: updatedAt, Changed: true, Credentials: input.Credentials,
	}, nil
}

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

// stringCredential mirrors stringCredential: trimmed string credential or "".
func stringCredential(credentials map[string]any, key string) string {
	return normalizeText(credentials[key])
}

// requiredCredentialSource mirrors requiredAccountCredentialSource
// (account-credentials-normalization.ts): the secret field that backs the
// fingerprint/mask columns per account type. Kept package-local because the
// accounts slice keeps its own copy private.
func requiredCredentialSource(accountType string, credentials map[string]any) (string, error) {
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := stringCredential(credentials, key); value != "" {
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
		return "", &ValidationError{Message: "OAuth 凭据不能为空"}
	case "api_key":
		if value := stringCredential(credentials, "api_key"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "API Key 不能为空"}
	case "google_oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "Google OAuth 凭据不能为空"}
	default:
		if value := pick("api_key", "refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "账户凭据不能为空"}
	}
}
