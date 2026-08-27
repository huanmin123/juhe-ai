package modelchecksource

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckresolver"
)

// PostgresReader is the Go-owned J3b business candidate reader. It has only
// SELECT access to juhe_business and never contacts the Node DB service, IPC,
// or HTTP runtime. The caller authenticates the actor; this reader enforces
// the resulting system-account scope inside every account query.
type PostgresReader struct {
	db               *sql.DB
	credentialSecret string
	identitySecret   string
	now              func() time.Time
}

func NewPostgresReader(db *sql.DB, credentialSecret, identitySecret string, now func() time.Time) (*PostgresReader, error) {
	if db == nil {
		return nil, errors.New("model check PostgreSQL reader database is required")
	}
	if strings.TrimSpace(credentialSecret) == "" {
		return nil, errors.New("model check PostgreSQL reader credential secret is required")
	}
	if strings.TrimSpace(identitySecret) == "" {
		return nil, errors.New("model check PostgreSQL reader identity secret is required")
	}
	if now == nil {
		now = time.Now
	}
	return &PostgresReader{db: db, credentialSecret: credentialSecret, identitySecret: identitySecret, now: now}, nil
}

// CheckContract is intentionally read-only. Deployment must pre-provision the
// business schema and SELECT grants; jobs never repair or migrate it.
func (r *PostgresReader) CheckContract(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("model check PostgreSQL reader is not initialized")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("open model check reader contract transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return fmt.Errorf("set model check reader transaction read-only: %w", err)
	}
	rows, err := tx.QueryContext(ctx, postgresContractSQL)
	if err != nil {
		return fmt.Errorf("verify model check reader contract: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close model check reader contract result: %w", err)
	}
	// Parse and plan the real candidate query during readiness checks. This
	// catches schema/column/type drift before the first management request,
	// while the impossible identifiers keep the check read-only and return no
	// account or credential rows.
	planRows, err := tx.QueryContext(ctx, "EXPLAIN "+postgresCandidateSQL, "__contract_scope__", "__contract_account__", r.now().UTC().Format(time.RFC3339Nano), false)
	if err != nil {
		return fmt.Errorf("verify model check reader candidate query: %w", err)
	}
	if err := planRows.Close(); err != nil {
		return fmt.Errorf("close model check reader candidate plan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model check reader contract transaction: %w", err)
	}
	return nil
}

// FreezeTarget resolves one authenticated J3b request into a durable,
// credential-free AccountSnapshot and its process-local execution material.
func (r *PostgresReader) FreezeTarget(ctx context.Context, request Request) (FrozenTarget, error) {
	if r == nil || r.db == nil {
		return FrozenTarget{}, errors.New("model check PostgreSQL reader is not initialized")
	}
	if strings.TrimSpace(request.SystemAccountID) == "" || strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.Model) == "" {
		return FrozenTarget{}, errors.New("model check reader request is incomplete")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return FrozenTarget{}, fmt.Errorf("open model check reader transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return FrozenTarget{}, fmt.Errorf("set model check reader transaction read-only: %w", err)
	}
	candidate, err := r.loadCandidate(ctx, tx, request)
	if err != nil {
		return FrozenTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return FrozenTarget{}, fmt.Errorf("commit model check reader transaction: %w", err)
	}
	frozen, err := Freeze(request, candidate, r.identitySecret)
	if err != nil {
		return FrozenTarget{}, fmt.Errorf("freeze model check target: %w", err)
	}
	return frozen, nil
}

// ResolveManagementSystemAccount mirrors the Node admin path when no explicit
// system-account filter was supplied: the selected logical account determines
// the business scope used for the following immutable freeze. It performs no
// credential read and callers must authenticate an administrator first.
func (r *PostgresReader) ResolveManagementSystemAccount(ctx context.Context, accountID string) (string, error) {
	if r == nil || r.db == nil || strings.TrimSpace(accountID) == "" {
		return "", errors.New("model check management account scope is invalid")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", fmt.Errorf("open model check management scope transaction: %w", err)
	}
	defer tx.Rollback()
	var systemAccountID string
	if err := tx.QueryRowContext(ctx, `SELECT system_account_id FROM juhe_business.accounts WHERE id=$1 AND deleted_at IS NULL LIMIT 1`, strings.TrimSpace(accountID)).Scan(&systemAccountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("model check account does not exist")
		}
		return "", fmt.Errorf("read model check management account scope: %w", err)
	}
	if strings.TrimSpace(systemAccountID) == "" {
		return "", errors.New("model check account system scope is empty")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit model check management scope transaction: %w", err)
	}
	return systemAccountID, nil
}

// Resolve implements modelcheckexecutor.TargetResolver. Rebuilding the target
// from the durable input means config, source credential, profile, proxy and
// model-mapping drift are all checked again immediately before upstream I/O.
func (r *PostgresReader) Resolve(ctx context.Context, resolution modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
	request := Request{
		SystemAccountID:      resolution.Input.SystemAccountID,
		AccountID:            resolution.Account.ID,
		Model:                resolution.Input.Model,
		AllowQualityIsolated: resolution.Input.Trigger == "quality_recovery",
	}
	frozen, err := r.FreezeTarget(ctx, request)
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, err
	}
	if frozen.DurableAccount != resolution.Account {
		return modelcheckexecutor.ResolvedTarget{}, errors.New("model check account execution snapshot is stale")
	}
	resolver, err := modelcheckresolver.New([]modelcheckresolver.Snapshot{frozen.Execution}, r.credentialSecret)
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, err
	}
	return resolver.Resolve(ctx, resolution)
}

func (r *PostgresReader) loadCandidate(ctx context.Context, tx *sql.Tx, request Request) (Candidate, error) {
	return r.loadCandidateWithQuery(ctx, tx, request, postgresCandidateSQL, "juhe_business.")
}

func (r *PostgresReader) loadCandidateWithQuery(ctx context.Context, tx *sql.Tx, request Request, candidateSQL, tablePrefix string) (Candidate, error) {
	now := r.now().UTC()
	row := tx.QueryRowContext(ctx, candidateSQL, strings.TrimSpace(request.SystemAccountID), strings.TrimSpace(request.AccountID), now.Format(time.RFC3339Nano), request.AllowQualityIsolated)
	var raw postgresCandidate
	if err := row.Scan(
		&raw.accountID, &raw.systemAccountID, &raw.accountName, &raw.targetOwnerSystemID, &raw.accountRevision, &raw.accountStatus, &raw.accountSchedulable, &raw.endpointMode,
		&raw.authorizationID, &raw.sourceID, &raw.sourceRevision, &raw.providerCode, &raw.profileID, &raw.protocolCode, &raw.protocolVersion, &raw.credentialType, &raw.credentialsEncrypted,
		&raw.profileEnabled, &raw.profileBaseURL, &raw.profileUpdatedAt, &raw.groupID,
		&raw.proxyID, &raw.proxyEnabled, &raw.proxyType, &raw.proxyHost, &raw.proxyPort, &raw.proxyUsername, &raw.proxyPasswordEncrypted,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Candidate{}, errors.New("model check account does not exist or is not permitted for this system account")
		}
		return Candidate{}, fmt.Errorf("read model check account candidate: %w", err)
	}
	supportedModels, err := readSupportedModels(ctx, tx, tablePrefix, raw.sourceID)
	if err != nil {
		return Candidate{}, err
	}
	mappings, err := readModelMappings(ctx, tx, tablePrefix, raw.sourceID)
	if err != nil {
		return Candidate{}, err
	}
	material, err := resolveCredentialMaterial(r.credentialSecret, raw)
	if err != nil {
		return Candidate{}, err
	}
	proxy, proxyVersion, err := buildProxyEnvelope(r.credentialSecret, raw)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		AccountID:           raw.accountID,
		SystemAccountID:     raw.systemAccountID,
		TargetName:          raw.accountName,
		TargetOwnerSystemID: raw.targetOwnerSystemID,
		GroupID:             raw.groupID,
		// ConfigRevision is the logical account revision exposed by the Node
		// contract. Effective-source changes are fenced independently by the
		// credential/profile/proxy opaque identities below; folding source
		// revision into this field would make authorized snapshots incompatible
		// with the durable model-check input schema.
		ConfigRevision:    strconv.FormatInt(raw.accountRevision, 10),
		ProviderCode:      raw.providerCode,
		ProtocolProfileID: raw.profileID,
		ProtocolRevision:  profileRevision(raw),
		Status:            raw.accountStatus,
		Eligible:          modelCheckStatusEligible(raw.accountStatus, request.AllowQualityIsolated),
		EndpointMode:      raw.endpointMode,
		Endpoint:          material.baseURL,
		CredentialType:    raw.credentialType,
		Credential:        accounthealth.CredentialEnvelope{Kind: "account_credentials", Ciphertext: raw.credentialsEncrypted},
		// This opaque reference includes the effective physical source revision.
		// A source-only model mapping or credential configuration change must
		// invalidate an already-issued authorized-account input even when its
		// encrypted credential bytes happen to be unchanged.
		CredentialRef:       opaqueRevision("execution-source", strings.Join([]string{raw.sourceID, strconv.FormatInt(raw.sourceRevision, 10), raw.credentialsEncrypted}, "\x00")),
		Proxy:               proxy,
		ProxyVersion:        proxyVersion,
		OAuthQuotaProjectID: material.quotaProjectID,
		SupportedModels:     supportedModels,
		ModelMappings:       mappings,
	}, nil
}

type postgresCandidate struct {
	accountID, systemAccountID, accountName, targetOwnerSystemID string
	accountStatus, endpointMode                                  string
	accountRevision                                              int64
	accountSchedulable                                           bool
	authorizationID, sourceID                                    string
	sourceRevision                                               int64
	providerCode, profileID, protocolCode, protocolVersion       string
	credentialType, credentialsEncrypted                         string
	profileEnabled                                               bool
	profileBaseURL, profileUpdatedAt, groupID                    string
	proxyID                                                      sql.NullString
	proxyEnabled                                                 sql.NullBool
	proxyType, proxyHost, proxyUsername, proxyPasswordEncrypted  sql.NullString
	proxyPort                                                    sql.NullInt64
}

type credentialMaterial struct {
	baseURL        string
	quotaProjectID string
}

func resolveCredentialMaterial(secret string, candidate postgresCandidate) (credentialMaterial, error) {
	plain, err := accounthealth.DecryptV1Envelope(secret, candidate.credentialsEncrypted)
	if err != nil {
		return credentialMaterial{}, errors.New("model check account credentials are unavailable")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(plain, &fields); err != nil {
		return credentialMaterial{}, errors.New("model check account credentials are invalid")
	}
	if !credentialSupportsEndpointMode(fields, candidate.endpointMode) {
		return credentialMaterial{}, errors.New("model check endpoint mode is not enabled by account credentials")
	}
	baseURL := credentialString(fields, "base_url")
	if baseURL == "" {
		baseURL = defaultBaseURL(candidate.profileID, candidate.providerCode, candidate.credentialType, credentialString(fields, "oauth_type"))
	}
	if strings.TrimSpace(baseURL) == "" {
		return credentialMaterial{}, errors.New("model check account base URL is unavailable")
	}
	return credentialMaterial{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), quotaProjectID: credentialString(fields, "quota_project_id")}, nil
}

func credentialSupportsEndpointMode(fields map[string]json.RawMessage, endpointMode string) bool {
	raw, ok := fields["supported_endpoint_modes"]
	if !ok {
		return false
	}
	var modes []string
	if json.Unmarshal(raw, &modes) != nil {
		return false
	}
	for _, mode := range modes {
		if strings.TrimSpace(mode) == strings.TrimSpace(endpointMode) {
			return true
		}
	}
	return false
}

func credentialString(values map[string]json.RawMessage, key string) string {
	var value string
	if raw, found := values[key]; found && json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func defaultBaseURL(profileID, provider, credentialType, oauthType string) string {
	if profileID == "profile_gpt_openai_v1" && credentialType == "oauth" {
		return "https://chatgpt.com/backend-api/codex"
	}
	switch profileID {
	case "profile_xai_openai_v1":
		if credentialType == "oauth" {
			return "https://cli-chat-proxy.grok.com/v1"
		}
		return "https://api.x.ai/v1"
	case "profile_deepseek_openai_v1":
		return "https://api.deepseek.com"
	case "profile_deepseek_anthropic_v1":
		return "https://api.deepseek.com/anthropic"
	case "profile_glm_general_openai_v1":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "profile_glm_coding_openai_v1":
		return "https://open.bigmodel.cn/api/coding/paas/v4"
	case "profile_glm_coding_anthropic_v1":
		return "https://open.bigmodel.cn/api/anthropic"
	case "profile_gemini_native_v1beta":
		if credentialType == "google_oauth" && (oauthType == "code_assist" || oauthType == "google_one") {
			return "https://cloudcode-pa.googleapis.com"
		}
		return "https://generativelanguage.googleapis.com"
	case "profile_gemini_openai_chat_v1beta":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case "profile_anthropic_anthropic_v1", "profile_hybrid_anthropic_messages_v1":
		return "https://api.anthropic.com"
	case "profile_hybrid_gemini_native_v1beta":
		return "https://generativelanguage.googleapis.com"
	}
	if provider == "anthropic" {
		return "https://api.anthropic.com"
	}
	if provider == "gemini" {
		return "https://generativelanguage.googleapis.com"
	}
	return "https://api.openai.com"
}

func buildProxyEnvelope(secret string, candidate postgresCandidate) (*accounthealth.CredentialEnvelope, string, error) {
	if !candidate.proxyID.Valid {
		return nil, "direct", nil
	}
	if !candidate.proxyEnabled.Bool || candidate.proxyPort.Int64 < 1 || candidate.proxyPort.Int64 > 65535 || strings.TrimSpace(candidate.proxyHost.String) == "" {
		return nil, "", errors.New("model check proxy profile is unavailable")
	}
	scheme := strings.TrimSpace(candidate.proxyType.String)
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return nil, "", errors.New("model check proxy protocol is unsupported")
	}
	endpoint := &url.URL{Scheme: scheme, Host: net.JoinHostPort(candidate.proxyHost.String, strconv.FormatInt(candidate.proxyPort.Int64, 10))}
	if user := strings.TrimSpace(candidate.proxyUsername.String); user != "" {
		password, err := proxyPassword(secret, candidate.proxyPasswordEncrypted.String)
		if err != nil {
			return nil, "", err
		}
		endpoint.User = url.UserPassword(user, password)
	}
	ciphertext, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"url":`+mustJSON(endpoint.String())+`}`))
	if err != nil {
		return nil, "", fmt.Errorf("encrypt model check proxy envelope: %w", err)
	}
	version := opaqueRevision("proxy", strings.Join([]string{candidate.proxyID.String, strconv.FormatBool(candidate.proxyEnabled.Bool), scheme, candidate.proxyHost.String, strconv.FormatInt(candidate.proxyPort.Int64, 10), candidate.proxyUsername.String, candidate.proxyPasswordEncrypted.String}, "\x00"))
	return &accounthealth.CredentialEnvelope{Kind: "proxy_url", Ciphertext: ciphertext}, version, nil
}

func proxyPassword(secret, encrypted string) (string, error) {
	if strings.TrimSpace(encrypted) == "" {
		return "", errors.New("model check proxy password is missing")
	}
	plain, err := accounthealth.DecryptV1Envelope(secret, encrypted)
	if err != nil {
		return "", errors.New("model check proxy password is unavailable")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(plain, &fields) != nil {
		return "", errors.New("model check proxy password is invalid")
	}
	password := credentialString(fields, "password")
	if password == "" {
		return "", errors.New("model check proxy password is missing")
	}
	return password, nil
}

func readSupportedModels(ctx context.Context, tx *sql.Tx, tablePrefix, sourceID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT model FROM `+tablePrefix+`account_supported_models WHERE account_id=$1 ORDER BY model ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("read model check supported models: %w", err)
	}
	defer rows.Close()
	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, fmt.Errorf("scan model check supported model: %w", err)
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model check supported models: %w", err)
	}
	return models, nil
}

func readModelMappings(ctx context.Context, tx *sql.Tx, tablePrefix, sourceID string) ([]ModelMapping, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled FROM `+tablePrefix+`account_model_mappings WHERE account_id=$1 ORDER BY source_model ASC,source_endpoint_family ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("read model check mappings: %w", err)
	}
	defer rows.Close()
	mappings := make([]ModelMapping, 0)
	for rows.Next() {
		var mapping ModelMapping
		if err := rows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily, &mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &mapping.Enabled); err != nil {
			return nil, fmt.Errorf("scan model check mapping: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model check mappings: %w", err)
	}
	return mappings, nil
}

func modelCheckStatusEligible(status string, allowQualityIsolated bool) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "temporary_unavailable", "rate_limited":
		return true
	case "quality_isolated":
		return allowQualityIsolated
	default:
		return false
	}
}

func profileRevision(candidate postgresCandidate) string {
	return opaqueRevision("profile", strings.Join([]string{candidate.profileID, candidate.providerCode, candidate.protocolCode, candidate.protocolVersion, strconv.FormatBool(candidate.profileEnabled), candidate.profileBaseURL, candidate.profileUpdatedAt}, "\x00"))
}

func opaqueRevision(label, value string) string {
	sum := sha256.Sum256([]byte(label + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

const postgresContractSQL = `
SELECT a.id, a.config_revision, a.authorization_instance_source_account_id,
       source.id, profile.id, binding.group_id, group_row.id,
       account_authorization.id, proxy.id, supported.model, mapping.source_model
FROM juhe_business.accounts a
LEFT JOIN juhe_business.accounts source ON source.id=a.authorization_instance_source_account_id
LEFT JOIN juhe_business.provider_protocol_profiles profile ON profile.id=a.provider_protocol_profile_id
LEFT JOIN juhe_business.group_accounts binding ON binding.account_id=a.id
LEFT JOIN juhe_business.groups group_row ON group_row.id=binding.group_id
LEFT JOIN juhe_business.resource_authorizations account_authorization ON account_authorization.id=a.authorization_instance_authorization_id
LEFT JOIN juhe_business.proxy_profiles proxy ON proxy.id=a.proxy_profile_id
LEFT JOIN juhe_business.account_supported_models supported ON supported.account_id=a.id
LEFT JOIN juhe_business.account_model_mappings mapping ON mapping.account_id=a.id
WHERE FALSE`

const postgresCandidateSQL = `
SELECT
  a.id, a.system_account_id, a.name, COALESCE(a.authorization_instance_owner_system_account_id, a.system_account_id), a.config_revision, a.status, a.schedulable, a.health_check_endpoint_mode,
  COALESCE(a.authorization_instance_authorization_id, ''), source.id, source.config_revision,
  source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, source.type, source.credentials_encrypted,
  profile.enabled, profile.base_url, profile.updated_at, binding.group_id,
  proxy.id, proxy.enabled, proxy.type, proxy.host, proxy.port, proxy.username, proxy.password_encrypted
FROM juhe_business.accounts a
JOIN juhe_business.accounts source
  ON source.id=CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE a.authorization_instance_source_account_id END
  AND source.deleted_at IS NULL
JOIN juhe_business.provider_protocol_profiles profile
  ON profile.id=source.provider_protocol_profile_id AND profile.enabled=1
JOIN LATERAL (
  SELECT ga.group_id, ga.account_authorization_id
  FROM juhe_business.group_accounts ga
  WHERE ga.account_id=a.id AND ga.system_account_id=a.system_account_id AND ga.enabled=1
    AND (a.authorization_instance_authorization_id IS NULL OR ga.account_authorization_id=a.authorization_instance_authorization_id)
  ORDER BY ga.updated_at DESC, ga.group_id ASC
  LIMIT 1
) binding ON TRUE
JOIN juhe_business.groups group_row ON group_row.id=binding.group_id AND group_row.enabled=1
LEFT JOIN juhe_business.resource_authorizations account_authorization ON account_authorization.id=a.authorization_instance_authorization_id
LEFT JOIN juhe_business.proxy_profiles proxy ON proxy.id=source.proxy_profile_id
WHERE a.id=$2
  AND a.system_account_id=$1
  AND a.deleted_at IS NULL
  AND ((a.status='quality_isolated' AND $4::boolean) OR (a.schedulable=1 AND a.status IN ('active','temporary_unavailable','rate_limited') AND (a.account_expires_at IS NULL OR a.account_expires_at>$3)))
  AND binding.group_id IS NOT NULL
  AND (
    group_row.system_account_id=a.system_account_id OR EXISTS (
      SELECT 1 FROM juhe_business.resource_authorizations group_authorization
      WHERE group_authorization.resource_type='group' AND group_authorization.resource_id=group_row.id
        AND group_authorization.resource_owner_system_account_id=group_row.system_account_id
        AND group_authorization.grantee_system_account_id=a.system_account_id
        AND group_authorization.scope='use' AND group_authorization.status='active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at>$3)
    )
  )
  AND (
    a.authorization_instance_authorization_id IS NULL OR (
      account_authorization.id IS NOT NULL AND account_authorization.resource_type='account'
      AND account_authorization.resource_id=source.id AND account_authorization.resource_owner_system_account_id=source.system_account_id
      AND account_authorization.grantee_system_account_id=a.system_account_id AND account_authorization.scope='use' AND account_authorization.status='active'
      AND (account_authorization.expires_at IS NULL OR account_authorization.expires_at>$3)
      AND ($4::boolean OR (source.status IN ('active','temporary_unavailable','rate_limited') AND source.schedulable=1 AND source.last_error_code IS DISTINCT FROM 'account_expired'))
      AND (source.account_expires_at IS NULL OR source.account_expires_at>$3)
    )
  )
LIMIT 1`
