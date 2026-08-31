package modelcheckowner

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// BusinessTargetSource reads the Gateway-owned Business database directly.
// It is intentionally read-only: schema lifecycle and all mutations belong to
// the Business owner handoff, while this port only freezes an execution target.
type BusinessTargetSource struct {
	db               *sql.DB
	postgres         bool
	credentialSecret string
	now              func() time.Time
}

type BusinessTargetConnection struct {
	DB     *sql.DB
	Source *BusinessTargetSource
	Close  func() error
}

func NewBusinessTargetSource(db *sql.DB, postgres bool, credentialSecret string) (*BusinessTargetSource, error) {
	if db == nil || strings.TrimSpace(credentialSecret) == "" {
		return nil, errors.New("J3b Business target source requires database and credential secret")
	}
	return &BusinessTargetSource{db: db, postgres: postgres, credentialSecret: credentialSecret, now: time.Now}, nil
}

// OpenBusinessTargetSource opens the configured Business database without
// granting this J3b reader a write path. SQLite uses query_only and a
// read-only URI; PostgreSQL callers must provision a role with SELECT-only
// access to the required relations.
func OpenBusinessTargetSource(ctx context.Context, cfg Config) (*BusinessTargetSource, func() error, error) {
	connection, err := OpenBusinessTargetConnection(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return connection.Source, connection.Close, nil
}

// OpenBusinessTargetConnection returns the validated DB handle together with
// the source. Gateway auth and source resolution must share this handle so
// they use one DSN, one permission contract and one lifecycle boundary.
func OpenBusinessTargetConnection(ctx context.Context, cfg Config) (*BusinessTargetConnection, error) {
	if !cfg.Enabled || strings.TrimSpace(cfg.CredentialSecret) == "" {
		return nil, errors.New("J3b Business source configuration is incomplete")
	}
	if cfg.BusinessHandoffConfirmed && !cfg.NodeWriterStopped {
		return nil, errors.New("J3b Business owner handoff 已确认但 Node writer 未停止，必须保持关闭")
	}
	if cfg.StoreMode != "sqlite" && cfg.StoreMode != "postgres" {
		return nil, errors.New("J3b Business source store mode is invalid")
	}
	postgres := cfg.StoreMode == "postgres"
	driver, dsn := "", ""
	if postgres {
		driver, dsn = "pgx", cfg.BusinessPostgresURL
		if strings.TrimSpace(dsn) == "" {
			return nil, errors.New("J3b Business PostgreSQL URL is required")
		}
	} else {
		// Before the complete Business handoff this connection is a strict
		// read-only coexistence reader. Once the handoff gate is confirmed,
		// auth/session and enforcement share the same owner connection and
		// need normal SQLite write access; the source itself still uses
		// read-only transactions for target resolution.
		mode := "ro&_pragma=query_only(1)"
		if cfg.BusinessHandoffConfirmed {
			mode = "rw"
		}
		// SQLite foreign-key enforcement is connection-local and disabled by
		// default. The Gateway owner must enable it explicitly so the schema
		// contract's cascade/relationship guarantees hold for every connection.
		driver, dsn = "sqlite", "file:"+cfg.BusinessDatabasePath+"?mode="+mode+"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		if strings.TrimSpace(cfg.BusinessDatabasePath) == "" {
			return nil, errors.New("J3b Business SQLite path is required")
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open J3b Business source database: %w", err)
	}
	if !postgres {
		// SQLite's physical-file owner must serialize all reads/writes through
		// one connection. This applies before and after handoff; query_only is
		// an access mode, not a substitute for single-connection ownership.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	closeDB := db.Close
	// schemaReady is evidence, not a trust-me switch: on SQLite it must be
	// backed by the same versioned table/column/index contract that
	// maintenance reports before Gateway exposes the owner.
	if cfg.SchemaReady && !postgres {
		if err := CheckBusinessSQLiteSchema(ctx, db); err != nil {
			_ = closeDB()
			return nil, err
		}
	} else if cfg.SchemaReady && postgres {
		if err := CheckBusinessPostgresSchema(ctx, db, "juhe_business"); err != nil {
			_ = closeDB()
			return nil, err
		}
	}
	source, err := NewBusinessTargetSource(db, postgres, cfg.CredentialSecret)
	if err != nil {
		_ = closeDB()
		return nil, err
	}
	if err := source.CheckContract(ctx); err != nil {
		_ = closeDB()
		return nil, err
	}
	return &BusinessTargetConnection{DB: db, Source: source, Close: closeDB}, nil
}

// Resolver returns the narrow in-process function expected by Runtime. It is
// a convenience adapter only; all reads still execute inside this source.
func (s *BusinessTargetSource) Resolver() Resolver {
	if s == nil {
		return nil
	}
	return s.Resolve
}

// ComparisonResolver resolves the separately frozen trusted-comparison
// account inside the same Gateway process and its own frozen business scope.
// It never uses the primary target ID or tenant as a fallback.
func (s *BusinessTargetSource) ComparisonResolver() Resolver {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, request RunRequest) (Target, error) {
		if !request.TrustedComparison || strings.TrimSpace(request.TrustedComparisonAccountID) == "" {
			return Target{}, errors.New("J3b trusted comparison target is not configured")
		}
		if strings.TrimSpace(request.TrustedComparisonSystemAccountID) == "" {
			return Target{}, errors.New("J3b trusted comparison system account is not configured")
		}
		comparisonRequest := request
		comparisonRequest.SystemAccountID = request.TrustedComparisonSystemAccountID
		comparisonRequest.TargetID = request.TrustedComparisonAccountID
		comparisonRequest.ConfigRevision = request.TrustedComparisonConfigRevision
		comparisonRequest.DispatchRevision = request.TrustedComparisonDispatchRevision
		comparisonRequest.SourceConfigRevision = request.TrustedComparisonSourceConfigRevision
		comparisonRequest.SourceDispatchRevision = request.TrustedComparisonSourceDispatchRevision
		return s.Resolve(ctx, comparisonRequest)
	}
}

func (s *BusinessTargetSource) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("J3b Business target source is not initialized")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open J3b Business source contract: %w", err)
	}
	defer tx.Rollback()
	contracts := map[string]string{"accounts": "id,name,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,account_expires_at,cooldown_until,last_error_code,credentials_encrypted,proxy_profile_id,availability_schedule_json,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at", "provider_protocol_profiles": "id,enabled,base_url", "proxy_profiles": "id,enabled,type,host,port,username,password_encrypted", "group_accounts": "account_id,system_account_id,group_id,account_authorization_id,enabled", "groups": "id,system_account_id,enabled", "resource_authorizations": "id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,scope,status,expires_at", "model_quality_policies": "system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes", "account_supported_models": "account_id,model", "account_model_mappings": "account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled"}
	for table, columns := range contracts {
		if _, err := tx.ExecContext(ctx, "SELECT "+columns+" FROM "+s.table(table)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify J3b Business source table %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// Resolve implements the Runtime resolver contract. The query includes the
// authenticated system account in SQL, and only active/schedulable accounts
// are eligible for ordinary checks.
func (s *BusinessTargetSource) Resolve(ctx context.Context, request RunRequest) (Target, error) {
	if s == nil || s.db == nil {
		return Target{}, errors.New("J3b Business target source is not initialized")
	}
	if strings.TrimSpace(request.SystemAccountID) == "" || request.TargetType != "account" || strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.Model) == "" {
		return Target{}, errors.New("J3b Business target request is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Target{}, fmt.Errorf("open J3b Business target transaction: %w", err)
	}
	defer tx.Rollback()
	query := `SELECT a.provider_code,a.provider_protocol_profile_id,a.protocol_code,a.type,a.config_revision,a.dispatch_revision,a.status,a.schedulable,a.health_check_endpoint_mode,a.account_expires_at,a.cooldown_until,a.last_error_code,COALESCE(a.availability_schedule_json,''),a.credentials_encrypted,a.authorization_instance_authorization_id,a.authorization_instance_source_account_id,p.base_url,p.enabled,a.proxy_profile_id,proxy.enabled,proxy.type,proxy.host,proxy.port,proxy.username,proxy.password_encrypted,a.name,ga.group_id FROM ` + s.table("accounts") + ` a JOIN ` + s.table("provider_protocol_profiles") + ` p ON p.id=a.provider_protocol_profile_id LEFT JOIN ` + s.table("proxy_profiles") + ` proxy ON proxy.id=a.proxy_profile_id JOIN ` + s.table("group_accounts") + ` ga ON ga.account_id=a.id AND ga.system_account_id=a.system_account_id AND ga.enabled=` + s.boolLiteral(true) + ` JOIN ` + s.table("groups") + ` g ON g.id=ga.group_id AND g.enabled=` + s.boolLiteral(true) + ` WHERE a.id=` + s.placeholder(1) + ` AND a.system_account_id=` + s.placeholder(2) + ` AND a.deleted_at IS NULL AND (g.system_account_id=a.system_account_id OR EXISTS (SELECT 1 FROM ` + s.table("resource_authorizations") + ` group_auth WHERE group_auth.resource_type='group' AND group_auth.resource_id=g.id AND group_auth.resource_owner_system_account_id=g.system_account_id AND group_auth.grantee_system_account_id=a.system_account_id AND group_auth.scope='use' AND group_auth.status='active' AND ` + s.expiryAfterNow("group_auth.expires_at") + `))`
	var provider, profileID, protocolCode, credentialType, encrypted, baseURL, status, endpointMode string
	var targetName, groupID sql.NullString
	var accountExpiresAt, cooldownUntil, lastErrorCode, availabilitySchedule sql.NullString
	var authorizationInstance, authorizationSource sql.NullString
	var proxyProfileID, proxyType, proxyHost, proxyUsername, proxyPassword sql.NullString
	var proxyEnabled sql.NullBool
	var proxyPort sql.NullInt64
	var revision, dispatchRevision int64
	var schedulable, profileEnabled bool
	if err := tx.QueryRowContext(ctx, query, request.TargetID, request.SystemAccountID).Scan(&provider, &profileID, &protocolCode, &credentialType, &revision, &dispatchRevision, &status, &schedulable, &endpointMode, &accountExpiresAt, &cooldownUntil, &lastErrorCode, &availabilitySchedule, &encrypted, &authorizationInstance, &authorizationSource, &baseURL, &profileEnabled, &proxyProfileID, &proxyEnabled, &proxyType, &proxyHost, &proxyPort, &proxyUsername, &proxyPassword, &targetName, &groupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Target{}, &RequestError{StatusCode: http.StatusNotFound, Message: "J3b Business account does not exist or is outside scope"}
		}
		return Target{}, fmt.Errorf("read J3b Business target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Target{}, fmt.Errorf("commit J3b Business target read: %w", err)
	}
	if (authorizationInstance.Valid && strings.TrimSpace(authorizationInstance.String) != "") || (authorizationSource.Valid && strings.TrimSpace(authorizationSource.String) != "") {
		return s.resolveAuthorizedTarget(ctx, request)
	}
	client, err := buildProxyClient(s.credentialSecret, proxyProfileID, proxyEnabled, proxyType, proxyHost, proxyPort, proxyUsername, proxyPassword)
	if err != nil {
		return Target{}, err
	}
	if request.ConfigRevision != "" && request.ConfigRevision != strconv.FormatInt(revision, 10) {
		return Target{}, errors.New("J3b Business account config revision is stale")
	}
	if request.DispatchRevision > 0 && request.DispatchRevision != dispatchRevision {
		return Target{}, errors.New("J3b Business account dispatch revision is stale")
	}
	if request.SourceConfigRevision != "" && request.SourceConfigRevision != strconv.FormatInt(revision, 10) {
		return Target{}, errors.New("J3b Business source account config revision is stale")
	}
	if request.SourceDispatchRevision > 0 && request.SourceDispatchRevision != dispatchRevision {
		return Target{}, errors.New("J3b Business source account dispatch revision is stale")
	}
	qualityRecovery := request.TriggerKind == string(SchedulerQualityRecovery)
	availabilityNow := s.nowUTC()
	if (qualityRecovery && status != "quality_isolated") || (!qualityRecovery && status != "active" && status != "temporary_unavailable" && status != "rate_limited") {
		return Target{}, errors.New("J3b Business account is unavailable")
	}
	if (!qualityRecovery && !schedulable) || !profileEnabled || accountUnavailableAt(accountExpiresAt.String, cooldownUntil.String, lastErrorCode.String, availabilityNow, qualityRecovery) {
		return Target{}, errors.New("J3b Business account is not schedulable")
	}
	if allowed, err := availabilityAllowedGateway(availabilitySchedule.String, availabilityNow); err != nil || (!qualityRecovery && !allowed) {
		if err != nil {
			return Target{}, fmt.Errorf("evaluate J3b account availability schedule: %w", err)
		}
		return Target{}, errors.New("J3b Business account is outside availability schedule")
	}
	profile, ok := modelcheckprofile.Find(provider, profileID)
	if !ok {
		return Target{}, errors.New("J3b Business provider profile does not support model")
	}
	mapping, err := resolveConfiguredUpstreamModelMapping(ctx, s.db, s.postgres, request.TargetID, profile, request.Model)
	if err != nil {
		return Target{}, err
	}
	if mapping.UpstreamModel == "" {
		return Target{}, errors.New("J3b Business account model restriction does not allow model")
	}
	upstreamProtocol := profile.Protocol
	upstreamEndpointMode := endpointMode
	if mapping.UpstreamEndpointFamily == modelcheckprofile.EndpointChatCompletions {
		upstreamProtocol = modelcheckprofile.ProtocolOpenAIChat
		upstreamEndpointMode = modelcheckprofile.EndpointModeForProtocol(upstreamProtocol, modelcheckprofile.EndpointModeIsStreaming(endpointMode))
	}
	credentialType = strings.TrimSpace(credentialType)
	if credentialType != "api_key" && credentialType != "oauth" && credentialType != "google_oauth" {
		return Target{}, errors.New("J3b Business account credential type is unsupported")
	}
	material, err := decryptAccountCredentialMaterial(s.credentialSecret, encrypted, credentialType)
	if err != nil {
		return Target{}, err
	}
	if err := validateCredentialEndpointMode(endpointMode, profile.Protocol, material); err != nil {
		return Target{}, err
	}
	adapter, err := openAIOAuthCodexAdapter(provider, profileID, credentialType, profile.Protocol, endpointMode)
	if err != nil {
		return Target{}, err
	}
	if adapter == modelcheckprobe.AdapterOpenAIOAuthCodex && upstreamProtocol != modelcheckprofile.ProtocolOpenAIResponses {
		return Target{}, errors.New("J3b Business OpenAI OAuth Codex model mapping is unsupported")
	}
	headers, err := credentialHeaders(provider, profileID, profile.Protocol, protocolCode, credentialType, material.Token)
	if err != nil {
		return Target{}, err
	}
	if material.BaseURL != "" && adapter == "" {
		baseURL = material.BaseURL
	}
	if adapter == modelcheckprobe.AdapterOpenAIOAuthCodex {
		baseURL = modelcheckprobe.OpenAIOAuthCodexBaseURL
		if material.ChatGPTAccountID != "" {
			headers.Set("chatgpt-account-id", material.ChatGPTAccountID)
		}
	}
	if profile.Protocol == modelcheckprofile.ProtocolGeminiNative && credentialType == "google_oauth" {
		if material.QuotaProjectID != "" {
			headers.Set("x-goog-user-project", material.QuotaProjectID)
		}
	}
	if dispatchRevision < 1 {
		return Target{}, errors.New("J3b Business account dispatch revision is invalid")
	}
	return Target{Endpoint: strings.TrimRight(baseURL, "/"), TargetName: strings.TrimSpace(targetName.String), TargetOwnerSystemAccountID: request.SystemAccountID, GroupID: strings.TrimSpace(groupID.String), ProviderCode: provider, CredentialType: credentialType, UpstreamAdapter: adapter, ConfigRevision: strconv.FormatInt(revision, 10), SourceConfigRevision: strconv.FormatInt(revision, 10), CredentialSourceAccountID: request.TargetID, SourceDispatchRevision: dispatchRevision, DispatchRevision: dispatchRevision, OwnPhysicalAccount: true, Protocol: profile.Protocol, SourceEndpointFamily: mapping.SourceEndpointFamily, UpstreamProtocol: upstreamProtocol, UpstreamEndpointFamily: mapping.UpstreamEndpointFamily, EndpointMode: endpointMode, UpstreamEndpointMode: upstreamEndpointMode, SupportedEndpointModes: append([]string(nil), material.SupportedEndpointModes...), Headers: headers, Client: client, UpstreamModel: mapping.UpstreamModel, Prompt: "Reply with exactly: OK-MODEL-CHECK"}, nil
}

func (s *BusinessTargetSource) resolveAuthorizedTarget(ctx context.Context, request RunRequest) (Target, error) {
	query := `SELECT a.config_revision,a.dispatch_revision,a.health_check_endpoint_mode,sa.config_revision,sa.dispatch_revision,a.status,a.schedulable,a.account_expires_at,a.cooldown_until,a.last_error_code,COALESCE(a.availability_schedule_json,''),sa.id,sa.provider_code,sa.provider_protocol_profile_id,sa.protocol_code,sa.type,sa.credentials_encrypted,p.base_url,p.enabled,sa.status,sa.schedulable,sa.account_expires_at,sa.cooldown_until,sa.last_error_code,COALESCE(sa.availability_schedule_json,''),ra.status,ra.expires_at,a.proxy_profile_id,sa.proxy_profile_id,instance_proxy.enabled,instance_proxy.type,instance_proxy.host,instance_proxy.port,instance_proxy.username,instance_proxy.password_encrypted,source_proxy.enabled,source_proxy.type,source_proxy.host,source_proxy.port,source_proxy.username,source_proxy.password_encrypted,a.name,a.system_account_id,ga.group_id FROM ` + s.table("accounts") + ` a JOIN ` + s.table("resource_authorizations") + ` ra ON ra.id=a.authorization_instance_authorization_id AND ra.resource_type='account' AND ra.resource_id=a.authorization_instance_source_account_id AND ra.grantee_system_account_id=` + s.placeholder(1) + ` AND ra.scope='use' JOIN ` + s.table("accounts") + ` sa ON sa.id=a.authorization_instance_source_account_id AND sa.system_account_id=ra.resource_owner_system_account_id AND sa.deleted_at IS NULL JOIN ` + s.table("provider_protocol_profiles") + ` p ON p.id=sa.provider_protocol_profile_id LEFT JOIN ` + s.table("proxy_profiles") + ` instance_proxy ON instance_proxy.id=a.proxy_profile_id LEFT JOIN ` + s.table("proxy_profiles") + ` source_proxy ON source_proxy.id=sa.proxy_profile_id JOIN ` + s.table("group_accounts") + ` ga ON ga.account_id=a.id AND ga.system_account_id=` + s.placeholder(2) + ` AND ga.enabled=` + s.boolLiteral(true) + ` AND ga.account_authorization_id=ra.id JOIN ` + s.table("groups") + ` g ON g.id=ga.group_id AND g.enabled=` + s.boolLiteral(true) + ` WHERE a.id=` + s.placeholder(3) + ` AND a.system_account_id=` + s.placeholder(4) + ` AND a.deleted_at IS NULL AND a.authorization_instance_source_account_id IS NOT NULL AND ra.status='active' AND ` + s.expiryAfterNow("ra.expires_at") + ` AND (g.system_account_id=a.system_account_id OR EXISTS (SELECT 1 FROM ` + s.table("resource_authorizations") + ` group_auth WHERE group_auth.resource_type='group' AND group_auth.resource_id=g.id AND group_auth.resource_owner_system_account_id=g.system_account_id AND group_auth.grantee_system_account_id=a.system_account_id AND group_auth.scope='use' AND group_auth.status='active' AND ` + s.expiryAfterNow("group_auth.expires_at") + `))`
	var sourceID, provider, profileID, protocolCode, credentialType, encrypted, accountStatus, sourceStatus, authorizationStatus, endpointMode string
	var targetName, targetOwnerSystemAccountID, groupID sql.NullString
	var accountExpiresAt, cooldownUntil, lastErrorCode, availabilitySchedule, sourceExpiresAt, sourceCooldownUntil, sourceLastErrorCode, sourceAvailabilitySchedule sql.NullString
	var baseURL string
	var revision, dispatchRevision, sourceRevision, sourceDispatchRevision int64
	var schedulable, sourceSchedulable, profileEnabled bool
	var expiresAt sql.NullString
	var instanceProxyProfileID, sourceProxyProfileID, instanceProxyType, instanceProxyHost, instanceProxyUsername, instanceProxyPassword, sourceProxyType, sourceProxyHost, sourceProxyUsername, sourceProxyPassword sql.NullString
	var instanceProxyEnabled, sourceProxyEnabled sql.NullBool
	var instanceProxyPort, sourceProxyPort sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, request.SystemAccountID, request.SystemAccountID, request.TargetID, request.SystemAccountID).Scan(&revision, &dispatchRevision, &endpointMode, &sourceRevision, &sourceDispatchRevision, &accountStatus, &schedulable, &accountExpiresAt, &cooldownUntil, &lastErrorCode, &availabilitySchedule, &sourceID, &provider, &profileID, &protocolCode, &credentialType, &encrypted, &baseURL, &profileEnabled, &sourceStatus, &sourceSchedulable, &sourceExpiresAt, &sourceCooldownUntil, &sourceLastErrorCode, &sourceAvailabilitySchedule, &authorizationStatus, &expiresAt, &instanceProxyProfileID, &sourceProxyProfileID, &instanceProxyEnabled, &instanceProxyType, &instanceProxyHost, &instanceProxyPort, &instanceProxyUsername, &instanceProxyPassword, &sourceProxyEnabled, &sourceProxyType, &sourceProxyHost, &sourceProxyPort, &sourceProxyUsername, &sourceProxyPassword, &targetName, &targetOwnerSystemAccountID, &groupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Target{}, &RequestError{StatusCode: http.StatusNotFound, Message: "J3b Business account does not exist or is outside scope"}
		}
		return Target{}, fmt.Errorf("read J3b authorized target: %w", err)
	}
	proxyProfileID, proxyEnabled, proxyType, proxyHost, proxyPort, proxyUsername, proxyPassword := sourceProxyProfileID, sourceProxyEnabled, sourceProxyType, sourceProxyHost, sourceProxyPort, sourceProxyUsername, sourceProxyPassword
	if !proxyProfileID.Valid || strings.TrimSpace(proxyProfileID.String) == "" {
		// Node resolves the physical source proxy first and falls back to the
		// virtual instance proxy only when the source has no proxy reference.
		proxyProfileID, proxyEnabled, proxyType, proxyHost, proxyPort, proxyUsername, proxyPassword = instanceProxyProfileID, instanceProxyEnabled, instanceProxyType, instanceProxyHost, instanceProxyPort, instanceProxyUsername, instanceProxyPassword
	}
	client, err := buildProxyClient(s.credentialSecret, proxyProfileID, proxyEnabled, proxyType, proxyHost, proxyPort, proxyUsername, proxyPassword)
	if err != nil {
		return Target{}, err
	}
	if request.ConfigRevision != "" && request.ConfigRevision != strconv.FormatInt(revision, 10) {
		return Target{}, errors.New("J3b Business account config revision is stale")
	}
	if request.DispatchRevision > 0 && request.DispatchRevision != dispatchRevision {
		return Target{}, errors.New("J3b Business authorized account dispatch revision is stale")
	}
	if request.SourceConfigRevision != "" && request.SourceConfigRevision != strconv.FormatInt(sourceRevision, 10) {
		return Target{}, errors.New("J3b Business source account config revision is stale")
	}
	if request.SourceDispatchRevision > 0 && request.SourceDispatchRevision != sourceDispatchRevision {
		return Target{}, errors.New("J3b Business source account dispatch revision is stale")
	}
	qualityRecovery := request.TriggerKind == string(SchedulerQualityRecovery)
	availabilityNow := s.nowUTC()
	if ((!qualityRecovery && accountStatus != "active") || (qualityRecovery && accountStatus != "quality_isolated" && accountStatus != "active")) || (!qualityRecovery && !schedulable) || sourceStatus != "active" || !sourceSchedulable || !profileEnabled || authorizationStatus != "active" || accountUnavailableAt(accountExpiresAt.String, cooldownUntil.String, lastErrorCode.String, availabilityNow, qualityRecovery) || accountUnavailableAt(sourceExpiresAt.String, sourceCooldownUntil.String, sourceLastErrorCode.String, availabilityNow, false) {
		return Target{}, errors.New("J3b Business authorized account is unavailable")
	}
	for label, raw := range map[string]string{"instance": availabilitySchedule.String, "source": sourceAvailabilitySchedule.String} {
		allowed, err := availabilityAllowedGateway(raw, availabilityNow)
		if err != nil {
			return Target{}, fmt.Errorf("evaluate J3b authorized %s availability schedule: %w", label, err)
		}
		if !qualityRecovery && !allowed {
			return Target{}, fmt.Errorf("J3b Business authorized %s account is outside availability schedule", label)
		}
	}
	profile, ok := modelcheckprofile.Find(provider, profileID)
	if !ok {
		return Target{}, errors.New("J3b Business provider profile does not support model")
	}
	mapping, err := resolveConfiguredUpstreamModelMapping(ctx, s.db, s.postgres, sourceID, profile, request.Model)
	if err != nil {
		return Target{}, err
	}
	if mapping.UpstreamModel == "" {
		return Target{}, errors.New("J3b Business account model restriction does not allow model")
	}
	upstreamProtocol := profile.Protocol
	upstreamEndpointMode := endpointMode
	if mapping.UpstreamEndpointFamily == modelcheckprofile.EndpointChatCompletions {
		upstreamProtocol = modelcheckprofile.ProtocolOpenAIChat
		upstreamEndpointMode = modelcheckprofile.EndpointModeForProtocol(upstreamProtocol, modelcheckprofile.EndpointModeIsStreaming(endpointMode))
	}
	credentialType = strings.TrimSpace(credentialType)
	if credentialType != "api_key" && credentialType != "oauth" && credentialType != "google_oauth" {
		return Target{}, errors.New("J3b Business account credential type is unsupported")
	}
	material, err := decryptAccountCredentialMaterial(s.credentialSecret, encrypted, credentialType)
	if err != nil {
		return Target{}, err
	}
	if err := validateCredentialEndpointMode(endpointMode, profile.Protocol, material); err != nil {
		return Target{}, err
	}
	adapter, err := openAIOAuthCodexAdapter(provider, profileID, credentialType, profile.Protocol, endpointMode)
	if err != nil {
		return Target{}, err
	}
	if adapter == modelcheckprobe.AdapterOpenAIOAuthCodex && upstreamProtocol != modelcheckprofile.ProtocolOpenAIResponses {
		return Target{}, errors.New("J3b Business OpenAI OAuth Codex model mapping is unsupported")
	}
	headers, err := credentialHeaders(provider, profileID, profile.Protocol, protocolCode, credentialType, material.Token)
	if err != nil {
		return Target{}, err
	}
	if material.BaseURL != "" && adapter == "" {
		baseURL = material.BaseURL
	}
	if adapter == modelcheckprobe.AdapterOpenAIOAuthCodex {
		baseURL = modelcheckprobe.OpenAIOAuthCodexBaseURL
		if material.ChatGPTAccountID != "" {
			headers.Set("chatgpt-account-id", material.ChatGPTAccountID)
		}
	}
	if profile.Protocol == modelcheckprofile.ProtocolGeminiNative && credentialType == "google_oauth" {
		if material.QuotaProjectID != "" {
			headers.Set("x-goog-user-project", material.QuotaProjectID)
		}
	}
	if dispatchRevision < 1 {
		return Target{}, errors.New("J3b Business account dispatch revision is invalid")
	}
	if sourceDispatchRevision < 1 {
		return Target{}, errors.New("J3b Business source account dispatch revision is invalid")
	}
	return Target{Endpoint: strings.TrimRight(baseURL, "/"), TargetName: strings.TrimSpace(targetName.String), TargetOwnerSystemAccountID: strings.TrimSpace(targetOwnerSystemAccountID.String), GroupID: strings.TrimSpace(groupID.String), ProviderCode: provider, CredentialType: credentialType, UpstreamAdapter: adapter, ConfigRevision: strconv.FormatInt(revision, 10), SourceConfigRevision: strconv.FormatInt(sourceRevision, 10), CredentialSourceAccountID: sourceID, SourceDispatchRevision: sourceDispatchRevision, DispatchRevision: dispatchRevision, OwnPhysicalAccount: false, Protocol: profile.Protocol, SourceEndpointFamily: mapping.SourceEndpointFamily, UpstreamProtocol: upstreamProtocol, UpstreamEndpointFamily: mapping.UpstreamEndpointFamily, EndpointMode: endpointMode, UpstreamEndpointMode: upstreamEndpointMode, SupportedEndpointModes: append([]string(nil), material.SupportedEndpointModes...), Headers: headers, Client: client, UpstreamModel: mapping.UpstreamModel, Prompt: "Reply with exactly: OK-MODEL-CHECK"}, nil
}

// BuildRequest freezes the Business target and quality policy across bounded
// read-only reads. Credentials remain in the resolver's process memory and
// are never copied into the request snapshot. The revision recheck below
// closes the account/auth/mapping drift window before the request is issued;
// it is not a substitute for the eventual single repeatable-read snapshot.
func (s *BusinessTargetSource) BuildRequest(ctx context.Context, systemAccountID string, command RunCommand) (RunRequest, error) {
	return s.buildRequest(ctx, strings.TrimSpace(systemAccountID), strings.TrimSpace(systemAccountID), false, command)
}

// BuildScopedRequest preserves the authenticated actor independently from the
// selected tenant. The legacy BuildRequest entry remains for scheduler paths,
// whose payloads are already single-tenant and do not carry administrator
// scope.
func (s *BusinessTargetSource) BuildScopedRequest(ctx context.Context, scope ManagementScope, command RunCommand) (RunRequest, error) {
	if s == nil || !scope.valid() {
		return RunRequest{}, errors.New("J3b Business management scope is incomplete")
	}
	targetSystemAccountID := strings.TrimSpace(scope.SelectedSystemAccountID)
	if scope.AllSystemAccounts {
		var err error
		targetSystemAccountID, err = s.targetSystemAccountID(ctx, command.TargetID)
		if err != nil {
			return RunRequest{}, err
		}
	}
	return s.buildRequest(ctx, strings.TrimSpace(scope.ActorSystemAccountID), targetSystemAccountID, scope.AllSystemAccounts, command)
}

func (s *BusinessTargetSource) targetSystemAccountID(ctx context.Context, targetID string) (string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(targetID) == "" {
		return "", errors.New("J3b Business global target is incomplete")
	}
	var systemAccountID string
	err := s.db.QueryRowContext(ctx, `SELECT system_account_id FROM `+s.table("accounts")+` WHERE id=`+s.placeholder(1)+` AND deleted_at IS NULL`, strings.TrimSpace(targetID)).Scan(&systemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &RequestError{StatusCode: http.StatusNotFound, Message: "J3b Business account does not exist or is outside scope"}
	}
	if err != nil {
		return "", fmt.Errorf("read J3b Business global target owner: %w", err)
	}
	if strings.TrimSpace(systemAccountID) == "" {
		return "", errors.New("J3b Business global target owner is empty")
	}
	return strings.TrimSpace(systemAccountID), nil
}

func (s *BusinessTargetSource) buildRequest(ctx context.Context, actorSystemAccountID, targetSystemAccountID string, allSystemAccounts bool, command RunCommand) (RunRequest, error) {
	if s == nil || strings.TrimSpace(actorSystemAccountID) == "" || strings.TrimSpace(targetSystemAccountID) == "" {
		return RunRequest{}, errors.New("J3b Business request actor is required")
	}
	if command.TargetType == "" {
		command.TargetType = "account"
	}
	if command.TargetType != "account" || strings.TrimSpace(command.TargetID) == "" || strings.TrimSpace(command.Model) == "" {
		return RunRequest{}, errors.New("J3b Business request target is incomplete")
	}
	target, err := s.Resolve(ctx, RunRequest{SystemAccountID: targetSystemAccountID, TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model})
	if err != nil {
		return RunRequest{}, err
	}
	targetFence, err := s.readTargetFence(ctx, targetSystemAccountID, command.TargetID, target)
	if err != nil {
		return RunRequest{}, err
	}
	policyProfile, revision, manualEnforcementEnabled, threshold, action, recoveryInterval, err := s.readPolicy(ctx, targetSystemAccountID)
	if err != nil {
		return RunRequest{}, err
	}
	// Resolve again with the first target's immutable config revision. This
	// catches account changes, revoked grants and mapping changes that occur
	// while the policy row is being read, and fails closed before issuing input.
	recheckedTarget, err := s.Resolve(ctx, RunRequest{SystemAccountID: targetSystemAccountID, TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model, ConfigRevision: target.ConfigRevision, DispatchRevision: target.DispatchRevision, SourceConfigRevision: target.SourceConfigRevision, SourceDispatchRevision: target.SourceDispatchRevision})
	if err != nil {
		return RunRequest{}, fmt.Errorf("J3b Business target changed while freezing request: %w", err)
	}
	if !sameTargetFence(recheckedTarget, target) {
		return RunRequest{}, errors.New("J3b Business target revision changed while freezing request")
	}
	recheckedTargetFence, err := s.readTargetFence(ctx, targetSystemAccountID, command.TargetID, recheckedTarget)
	if err != nil {
		return RunRequest{}, fmt.Errorf("J3b Business target fence changed while freezing request: %w", err)
	}
	if recheckedTargetFence != targetFence {
		return RunRequest{}, errors.New("J3b Business target/source/mapping fence changed while freezing request")
	}
	target = recheckedTarget
	currentPolicyProfile, currentRevision, currentManualEnforcementEnabled, currentThreshold, currentAction, currentRecoveryInterval, err := s.readPolicy(ctx, targetSystemAccountID)
	if err != nil {
		return RunRequest{}, fmt.Errorf("J3b Business policy changed while freezing request: %w", err)
	}
	if currentPolicyProfile != policyProfile || currentRevision != revision || currentManualEnforcementEnabled != manualEnforcementEnabled || currentThreshold != threshold || currentAction != action || currentRecoveryInterval != recoveryInterval {
		return RunRequest{}, errors.New("J3b Business policy changed while freezing request")
	}
	selectedProfile := strings.TrimSpace(command.Profile)
	if selectedProfile == "" {
		// Node's manual model-check route defaults to quick independently of a
		// saved quality-policy profile. The policy remains frozen below for
		// enforcement metadata, but it must not reject an explicit quick/full
		// diagnostic request.
		selectedProfile = "quick"
	}
	if selectedProfile != "quick" && selectedProfile != "full" {
		return RunRequest{}, errors.New("J3b Business policy profile is invalid")
	}
	request := RunRequest{TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model, Profile: selectedProfile, SystemAccountID: targetSystemAccountID, ActorSystemAccountID: actorSystemAccountID, ProviderCode: target.ProviderCode, Threshold: threshold, PenaltyAction: action, RecoveryIntervalMinutes: recoveryInterval, ManualEnforcementEnabled: manualEnforcementEnabled, OwnPhysicalAccount: target.OwnPhysicalAccount, ConfigRevision: target.ConfigRevision, DispatchRevision: target.DispatchRevision, SourceConfigRevision: target.SourceConfigRevision, SourceDispatchRevision: target.SourceDispatchRevision, PolicyRevision: revision, ProbeSetVersion: probeSetForProfile(selectedProfile), IdentityKey: targetSystemAccountID + ":" + command.TargetID + ":" + command.Model + ":" + selectedProfile + ":actor:" + actorSystemAccountID, SourceEndpointFamily: string(target.SourceEndpointFamily), UpstreamEndpointFamily: string(target.UpstreamEndpointFamily), UpstreamProtocol: string(target.UpstreamProtocol), UpstreamEndpointMode: target.UpstreamEndpointMode}
	if !command.TrustedComparison {
		return request, nil
	}
	if selectedProfile != "full" || strings.TrimSpace(command.TrustedComparisonID) == "" || command.TrustedComparisonID == command.TargetID {
		return RunRequest{}, errors.New("J3b trusted comparison requires a distinct full-profile account")
	}
	comparisonSystemAccountID := targetSystemAccountID
	if allSystemAccounts {
		comparisonSystemAccountID, err = s.targetSystemAccountID(ctx, command.TrustedComparisonID)
		if err != nil {
			return RunRequest{}, fmt.Errorf("resolve J3b trusted comparison owner: %w", err)
		}
	}
	comparison, err := s.Resolve(ctx, RunRequest{SystemAccountID: comparisonSystemAccountID, TargetType: "account", TargetID: command.TrustedComparisonID, Model: command.Model})
	if err != nil {
		return RunRequest{}, fmt.Errorf("resolve J3b trusted comparison: %w", err)
	}
	comparisonFence, err := s.readTargetFence(ctx, comparisonSystemAccountID, command.TrustedComparisonID, comparison)
	if err != nil {
		return RunRequest{}, fmt.Errorf("read J3b trusted comparison fence: %w", err)
	}
	comparisonRechecked, err := s.Resolve(ctx, RunRequest{SystemAccountID: comparisonSystemAccountID, TargetType: "account", TargetID: command.TrustedComparisonID, Model: command.Model, ConfigRevision: comparison.ConfigRevision, DispatchRevision: comparison.DispatchRevision, SourceConfigRevision: comparison.SourceConfigRevision, SourceDispatchRevision: comparison.SourceDispatchRevision})
	if err != nil {
		return RunRequest{}, fmt.Errorf("J3b trusted comparison changed while freezing request: %w", err)
	}
	if !sameTargetFence(comparisonRechecked, comparison) {
		return RunRequest{}, errors.New("J3b trusted comparison revision changed while freezing request")
	}
	comparisonRecheckedFence, err := s.readTargetFence(ctx, comparisonSystemAccountID, command.TrustedComparisonID, comparisonRechecked)
	if err != nil {
		return RunRequest{}, fmt.Errorf("J3b trusted comparison fence changed while freezing request: %w", err)
	}
	if comparisonRecheckedFence != comparisonFence {
		return RunRequest{}, errors.New("J3b trusted comparison target/source/mapping fence changed while freezing request")
	}
	comparison = comparisonRechecked
	request.TrustedComparison = true
	request.TrustedComparisonAccountID = command.TrustedComparisonID
	request.TrustedComparisonSystemAccountID = comparisonSystemAccountID
	request.TrustedComparisonConfigRevision = comparison.ConfigRevision
	request.TrustedComparisonDispatchRevision = comparison.DispatchRevision
	request.TrustedComparisonSourceConfigRevision = comparison.SourceConfigRevision
	request.TrustedComparisonSourceDispatchRevision = comparison.SourceDispatchRevision
	request.IdentityKey += ":comparison:" + comparisonSystemAccountID + ":" + command.TrustedComparisonID + ":" + comparison.ConfigRevision
	return request, nil
}

// sameTargetFence compares every resolved semantic that can affect an issued
// probe. Config/dispatch revisions remain the primary CAS values, while the
// additional fields close gaps where Business model mappings or source
// credentials change without incrementing the virtual instance revision.
func sameTargetFence(left, right Target) bool {
	if left.Endpoint != right.Endpoint || left.ProviderCode != right.ProviderCode || left.ConfigRevision != right.ConfigRevision || left.SourceConfigRevision != right.SourceConfigRevision || left.SourceDispatchRevision != right.SourceDispatchRevision || left.UpstreamModel != right.UpstreamModel || left.CredentialType != right.CredentialType || left.UpstreamAdapter != right.UpstreamAdapter || left.CredentialSourceAccountID != right.CredentialSourceAccountID || left.DispatchRevision != right.DispatchRevision || left.OwnPhysicalAccount != right.OwnPhysicalAccount || left.Protocol != right.Protocol || left.SourceEndpointFamily != right.SourceEndpointFamily || left.UpstreamProtocol != right.UpstreamProtocol || left.UpstreamEndpointFamily != right.UpstreamEndpointFamily || left.EndpointMode != right.EndpointMode || left.UpstreamEndpointMode != right.UpstreamEndpointMode || left.Prompt != right.Prompt || !sameStringSet(left.SupportedEndpointModes, right.SupportedEndpointModes) {
		return false
	}
	if !sameHeaderValues(left.Headers, right.Headers) {
		return false
	}
	return true
}

func sameStringSet(left, right []string) bool {
	leftSet, rightSet := map[string]struct{}{}, map[string]struct{}{}
	for _, value := range left {
		if value = strings.TrimSpace(value); value != "" {
			leftSet[value] = struct{}{}
		}
	}
	for _, value := range right {
		if value = strings.TrimSpace(value); value != "" {
			rightSet[value] = struct{}{}
		}
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for value := range leftSet {
		if _, ok := rightSet[value]; !ok {
			return false
		}
	}
	return true
}

func sameHeaderValues(left, right http.Header) bool {
	if len(left) != len(right) {
		return false
	}
	for key, values := range left {
		other, ok := right[key]
		if !ok || len(values) != len(other) {
			return false
		}
		for index := range values {
			if values[index] != other[index] {
				return false
			}
		}
	}
	return true
}

// readTargetFence computes a stable, credential-free digest of all mutable
// Business facts used by Resolve. The digest is deliberately read in one
// read-only transaction and is compared around BuildRequest's policy read.
// It is an explicit fence, not a claim that the later upstream request is
// serialized with Business writes; the owner must still retain final CAS at
// dispatch/enforcement boundaries.
func (s *BusinessTargetSource) readTargetFence(ctx context.Context, actorSystemAccountID, targetID string, target Target) (string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actorSystemAccountID) == "" || strings.TrimSpace(targetID) == "" {
		return "", errors.New("J3b Business target fence request is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("open J3b Business target fence transaction: %w", err)
	}
	defer tx.Rollback()
	hash := sha256.New()
	appendRows := func(label, query string, args ...any) error {
		hash.Write([]byte(label))
		hash.Write([]byte{0})
		rows, queryErr := tx.QueryContext(ctx, query, args...)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		columns, columnsErr := rows.Columns()
		if columnsErr != nil {
			return columnsErr
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if scanErr := rows.Scan(pointers...); scanErr != nil {
				return scanErr
			}
			for _, value := range values {
				hash.Write([]byte(fenceValue(value)))
				hash.Write([]byte{0})
			}
			hash.Write([]byte{1})
		}
		return rows.Err()
	}
	accountQuery := `SELECT a.id,a.system_account_id,a.provider_code,a.provider_protocol_profile_id,a.protocol_code,a.type,a.config_revision,a.dispatch_revision,a.status,a.schedulable,a.account_expires_at,a.cooldown_until,a.last_error_code,a.credentials_encrypted,a.proxy_profile_id,a.authorization_instance_authorization_id,a.authorization_instance_source_account_id,a.deleted_at,p.id,p.enabled,p.base_url,proxy.id,proxy.enabled,proxy.type,proxy.host,proxy.port,proxy.username,proxy.password_encrypted FROM ` + s.table("accounts") + ` a LEFT JOIN ` + s.table("provider_protocol_profiles") + ` p ON p.id=a.provider_protocol_profile_id LEFT JOIN ` + s.table("proxy_profiles") + ` proxy ON proxy.id=a.proxy_profile_id WHERE a.id=` + s.placeholder(1) + ` ORDER BY a.id`
	if err := appendRows("account:"+targetID, accountQuery, targetID); err != nil {
		return "", fmt.Errorf("read J3b Business target fence account: %w", err)
	}
	sourceID := strings.TrimSpace(target.CredentialSourceAccountID)
	if sourceID != "" && sourceID != targetID {
		if err := appendRows("source:"+sourceID, accountQuery, sourceID); err != nil {
			return "", fmt.Errorf("read J3b Business target fence source: %w", err)
		}
	}
	groupQuery := `SELECT ga.account_id,ga.system_account_id,ga.group_id,ga.account_authorization_id,ga.enabled,g.system_account_id,g.enabled,ra.id,ra.resource_type,ra.resource_id,ra.resource_owner_system_account_id,ra.grantee_system_account_id,ra.scope,ra.status,ra.expires_at FROM ` + s.table("group_accounts") + ` ga JOIN ` + s.table("groups") + ` g ON g.id=ga.group_id LEFT JOIN ` + s.table("resource_authorizations") + ` ra ON ra.resource_type='group' AND ra.resource_id=g.id AND ra.grantee_system_account_id=` + s.placeholder(1) + ` WHERE ga.account_id=` + s.placeholder(2) + ` AND ga.system_account_id=` + s.placeholder(3) + ` ORDER BY ga.group_id,ra.id`
	if err := appendRows("groups", groupQuery, actorSystemAccountID, targetID, actorSystemAccountID); err != nil {
		return "", fmt.Errorf("read J3b Business target fence groups: %w", err)
	}
	authorizationQuery := `SELECT id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,scope,status,expires_at FROM ` + s.table("resource_authorizations") + ` WHERE (id IN (SELECT authorization_instance_authorization_id FROM ` + s.table("accounts") + ` WHERE id=` + s.placeholder(1) + `) OR (grantee_system_account_id=` + s.placeholder(2) + ` AND resource_type IN ('account','group'))) ORDER BY id`
	if err := appendRows("authorizations", authorizationQuery, targetID, actorSystemAccountID); err != nil {
		return "", fmt.Errorf("read J3b Business target fence authorizations: %w", err)
	}
	modelID := sourceID
	if modelID == "" {
		modelID = targetID
	}
	supportedQuery := `SELECT model FROM ` + s.table("account_supported_models") + ` WHERE account_id=` + s.placeholder(1) + ` ORDER BY model`
	if err := appendRows("supported-models", supportedQuery, modelID); err != nil {
		return "", fmt.Errorf("read J3b Business target fence supported models: %w", err)
	}
	mappingQuery := `SELECT source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled FROM ` + s.table("account_model_mappings") + ` WHERE account_id=` + s.placeholder(1) + ` ORDER BY source_model,source_endpoint_family,upstream_model,upstream_endpoint_family`
	if err := appendRows("model-mappings", mappingQuery, modelID); err != nil {
		return "", fmt.Errorf("read J3b Business target fence mappings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit J3b Business target fence: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fenceValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "<null>"
	case []byte:
		return "bytes:" + hex.EncodeToString(typed)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}

func (s *BusinessTargetSource) readPolicy(ctx context.Context, systemAccountID string) (profile, revision string, manualEnforcementEnabled bool, threshold int, action string, recoveryInterval int, err error) {
	profile, revision, manualEnforcementEnabled, threshold, action, recoveryInterval = "quick", "0", true, 70, "fallback", 10
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", "", false, 0, "", 0, fmt.Errorf("open J3b Business policy transaction: %w", err)
	}
	defer tx.Rollback()
	query := `SELECT revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes FROM ` + s.table("model_quality_policies") + ` WHERE system_account_id=` + s.placeholder(1) + ` LIMIT 1`
	if scanErr := tx.QueryRowContext(ctx, query, systemAccountID).Scan(&revision, &profile, &manualEnforcementEnabled, &threshold, &action, &recoveryInterval); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", false, 0, "", 0, fmt.Errorf("read J3b Business quality policy: %w", scanErr)
	}
	if err := tx.Commit(); err != nil {
		return "", "", false, 0, "", 0, fmt.Errorf("commit J3b Business policy read: %w", err)
	}
	if threshold < 40 || threshold > 100 || recoveryInterval < 10 || recoveryInterval > 10080 || (profile != "quick" && profile != "full") || (action != "disable" && action != "fallback" && action != "quality_isolate") {
		return "", "", false, 0, "", 0, errors.New("J3b Business quality policy is invalid")
	}
	return profile, revision, manualEnforcementEnabled, threshold, action, recoveryInterval, nil
}

func (s *BusinessTargetSource) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *BusinessTargetSource) placeholder(index int) string {
	if s.postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func decryptCredential(secret, envelope string) (string, error) {
	plain, err := decryptCredentialPlaintext(secret, envelope)
	if err != nil {
		return "", err
	}
	fields, structured, err := parseCredentialFields(plain)
	if err != nil {
		return "", err
	}
	if !structured {
		return plain, nil
	}
	for _, key := range []string{"api_key", "access_token", "token"} {
		if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", errors.New("J3b Business credential JSON has no supported token field")
}

// decryptCredentialPlaintext authenticates and decrypts the envelope but does
// not guess which JSON field is a usable secret. Callers must apply the
// account/protocol-specific field contract before issuing any request.
func decryptCredentialPlaintext(secret, envelope string) (string, error) {
	parts := strings.Split(strings.TrimSpace(envelope), ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", errors.New("J3b Business credential envelope is invalid")
	}
	decode := func(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }
	iv, err := decode(parts[1])
	if err != nil {
		return "", errors.New("J3b Business credential envelope is invalid")
	}
	tag, err := decode(parts[2])
	if err != nil {
		return "", errors.New("J3b Business credential envelope is invalid")
	}
	ciphertext, err := decode(parts[3])
	if err != nil {
		return "", errors.New("J3b Business credential envelope is invalid")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(iv) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return "", errors.New("J3b Business credential envelope is invalid")
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return "", errors.New("J3b Business credential envelope cannot be decrypted")
	}
	if strings.TrimSpace(string(plain)) == "" {
		return "", errors.New("J3b Business credential is empty")
	}
	return strings.TrimSpace(string(plain)), nil
}

// parseCredentialFields distinguishes a legacy raw secret from the structured
// JSON envelope used by Node. A JSON object with unknown/missing fields, or any
// other valid JSON value, is never treated as a bearer token.
func parseCredentialFields(plain string) (map[string]any, bool, error) {
	trimmed := strings.TrimSpace(plain)
	if trimmed == "" {
		return nil, false, errors.New("J3b Business credential is empty")
	}
	if strings.HasPrefix(trimmed, "{") {
		var fields map[string]any
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
			return nil, true, errors.New("J3b Business credential JSON is invalid")
		}
		return fields, true, nil
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) == nil {
		return nil, true, errors.New("J3b Business credential JSON must be an object")
	}
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "\"") {
		return nil, true, errors.New("J3b Business credential JSON is invalid")
	}
	return nil, false, nil
}

// decryptAccountCredential applies the account credential type after the
// envelope has been authenticated. Refresh tokens are not access tokens: a
// refresh-only OAuth snapshot cannot be dispatched by this read-only owner.
func decryptAccountCredential(secret, envelope, credentialType string) (string, error) {
	material, err := decryptAccountCredentialMaterial(secret, envelope, credentialType)
	if err != nil {
		return "", err
	}
	return material.Token, nil
}

type accountCredentialMaterial struct {
	Token                  string
	BaseURL                string
	ChatGPTAccountID       string
	QuotaProjectID         string
	SupportedEndpointModes []string
	EndpointModesPresent   bool
}

func decryptAccountCredentialMaterial(secret, envelope, credentialType string) (accountCredentialMaterial, error) {
	plain, err := decryptCredentialPlaintext(secret, envelope)
	if err != nil {
		return accountCredentialMaterial{}, err
	}
	fields, structured, err := parseCredentialFields(plain)
	if err != nil {
		return accountCredentialMaterial{}, err
	}
	if !structured {
		return accountCredentialMaterial{Token: plain}, nil
	}
	if err := validateAccountCredentialFields(fields, credentialType); err != nil {
		return accountCredentialMaterial{}, err
	}
	var keys []string
	switch credentialType {
	case "api_key":
		keys = []string{"api_key"}
	case "oauth", "google_oauth":
		keys = []string{"access_token"}
	default:
		return accountCredentialMaterial{}, errors.New("J3b Business account credential type is unsupported")
	}
	var token string
	for _, key := range keys {
		if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
			token = strings.TrimSpace(value)
			break
		}
	}
	if token == "" && (credentialType == "oauth" || credentialType == "google_oauth") {
		if refresh, ok := fields["refresh_token"].(string); ok && strings.TrimSpace(refresh) != "" {
			return accountCredentialMaterial{}, errors.New("J3b Business OAuth credential has refresh_token only; access token refresh is required")
		}
	}
	if token == "" {
		return accountCredentialMaterial{}, errors.New("J3b Business credential JSON has no usable token")
	}
	material := accountCredentialMaterial{Token: token}
	if raw, ok := fields["base_url"]; ok {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return accountCredentialMaterial{}, errors.New("J3b Business credential base_url is invalid")
		}
		baseURL, err := normalizeCredentialBaseURL(value)
		if err != nil {
			return accountCredentialMaterial{}, err
		}
		material.BaseURL = baseURL
	}
	for _, key := range []string{"account_id", "chatgpt_account_id"} {
		raw, present := fields[key]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return accountCredentialMaterial{}, errors.New("J3b Business credential ChatGPT account ID is invalid")
		}
		material.ChatGPTAccountID = strings.TrimSpace(value)
		break
	}
	if raw, ok := fields["quota_project_id"]; ok {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return accountCredentialMaterial{}, errors.New("J3b Business credential quota_project_id is invalid")
		}
		material.QuotaProjectID = strings.TrimSpace(value)
	}
	if raw, ok := fields["supported_endpoint_modes"]; ok {
		modes, err := parseCredentialEndpointModes(raw)
		if err != nil {
			return accountCredentialMaterial{}, err
		}
		material.SupportedEndpointModes = modes
		material.EndpointModesPresent = true
	}
	return material, nil
}

// validateAccountCredentialFields keeps the structured envelope closed over
// the Node credential contract. J3b only consumes a small subset of these
// fields, but known provider metadata may legitimately coexist with the
// bearer token. Truly unknown fields must not be silently accepted and
// ignored, because that would make the snapshot semantics ambiguous.
func validateAccountCredentialFields(fields map[string]any, credentialType string) error {
	allowed := map[string]struct{}{}
	add := func(values ...string) {
		for _, value := range values {
			allowed[value] = struct{}{}
		}
	}
	common := []string{"base_url", "supported_endpoint_modes", "service_tier_override", "reasoning_effort_override", "error_handling_rules", "error_handling_rule_overrides", "response_inspection_rules", "quota_recovery_policy"}
	switch credentialType {
	case "api_key":
		add(common...)
		add("api_key")
	case "oauth":
		add(common...)
		add("access_token", "refresh_token", "expires_at", "client_id", "id_token", "token_type", "scope", "email", "account_id", "chatgpt_account_id", "organization_id", "chatgpt_user_id", "plan_type", "sub", "team_id", "subscription_tier", "entitlement_status")
	case "google_oauth":
		add(common...)
		add("access_token", "refresh_token", "expires_at", "client_id", "client_secret", "quota_project_id", "oauth_type", "project_id", "tier_id", "scope", "token_type", "drive_storage_limit", "drive_storage_usage", "drive_tier_updated_at")
	default:
		return errors.New("J3b Business account credential type is unsupported")
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("J3b Business credential JSON field %q is unsupported", key)
		}
	}
	return nil
}

// parseCredentialEndpointModes deliberately permits additional Node-only
// values. The resolver validates the selected Business mode against the Go
// profile; rewriting unrelated list values would misstate account capability.
func parseCredentialEndpointModes(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("J3b Business credential supported_endpoint_modes is invalid")
	}
	modes := make([]string, 0, len(values))
	for _, rawMode := range values {
		mode, ok := rawMode.(string)
		if !ok || mode == "" || mode != strings.TrimSpace(mode) {
			return nil, errors.New("J3b Business credential supported_endpoint_modes is invalid")
		}
		modes = append(modes, mode)
	}
	return modes, nil
}

func validateCredentialEndpointMode(endpointMode string, protocol modelcheckprofile.Protocol, material accountCredentialMaterial) error {
	if endpointMode == "" || endpointMode != strings.TrimSpace(endpointMode) || !modelcheckprofile.EndpointModeMatchesProtocol(protocol, endpointMode) {
		return errors.New("J3b Business health_check_endpoint_mode is incompatible with provider protocol profile")
	}
	if !material.EndpointModesPresent {
		return errors.New("J3b Business credential supported_endpoint_modes is missing")
	}
	for _, candidate := range material.SupportedEndpointModes {
		if candidate == endpointMode {
			return nil
		}
	}
	return errors.New("J3b Business health_check_endpoint_mode is not enabled by credential supported_endpoint_modes")
}

func decryptCredentialBaseURL(secret, envelope string) (string, error) {
	plain, err := decryptCredentialPlaintext(secret, envelope)
	if err != nil {
		return "", err
	}
	fields, structured, err := parseCredentialFields(plain)
	if err != nil {
		return "", err
	}
	if !structured {
		return "", nil
	}
	value, ok := fields["base_url"].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", nil
	}
	return normalizeCredentialBaseURL(value)
}

func decryptCredentialStringField(secret, envelope, field string) (string, bool, error) {
	plain, err := decryptCredentialPlaintext(secret, envelope)
	if err != nil {
		return "", false, err
	}
	fields, structured, err := parseCredentialFields(plain)
	if err != nil {
		return "", false, err
	}
	if !structured {
		return "", false, nil
	}
	value, ok := fields[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(value), true, nil
}

func normalizeCredentialBaseURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(trimmed, "\r\n\t\\") {
		return "", errors.New("J3b Business credential base_url is invalid")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", errors.New("J3b Business credential base_url is invalid")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return "", errors.New("J3b Business credential base_url is invalid")
	}
	return strings.TrimRight(trimmed, "/"), nil
}

func credentialHeaders(providerCode, profileID string, protocol modelcheckprofile.Protocol, protocolCode, credentialType, token string) (http.Header, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("J3b Business credential is empty")
	}
	if credentialType != "api_key" && credentialType != "oauth" && credentialType != "google_oauth" {
		return nil, errors.New("J3b Business account credential type is unsupported")
	}
	headers := http.Header{}
	switch protocol {
	case modelcheckprofile.ProtocolAnthropic:
		switch profileID {
		case "profile_anthropic_anthropic_v1":
			if providerCode != "" && providerCode != "anthropic" {
				return nil, errors.New("J3b Business Anthropic profile/provider mismatch")
			}
		case "profile_deepseek_anthropic_v1":
			if (providerCode != "" && providerCode != "deepseek") || credentialType != "api_key" {
				return nil, errors.New("J3b Business DeepSeek Anthropic profile requires api_key")
			}
		case "profile_glm_coding_anthropic_v1":
			if (providerCode != "" && providerCode != "glm") || credentialType != "api_key" {
				return nil, errors.New("J3b Business GLM Coding Anthropic profile requires api_key")
			}
		}
		if credentialType == "google_oauth" {
			return nil, errors.New("J3b Business google_oauth credential is incompatible with protocol " + protocolCode)
		}
		if credentialType == "api_key" && !(providerCode == "glm" && profileID == "profile_glm_coding_anthropic_v1") {
			headers.Set("x-api-key", token)
		} else {
			headers.Set("Authorization", "Bearer "+token)
		}
		headers.Set("anthropic-version", "2023-06-01")
		if providerCode == "anthropic" && credentialType == "oauth" {
			headers.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
			// The Node provider driver sends OAuth traffic with the Claude CLI
			// identity headers. Keep this narrow to Anthropic OAuth; GLM/DeepSeek
			// Anthropic-compatible API keys must not inherit the subscription lane.
			headers.Set("user-agent", "claude-cli/2.1.161 (external, cli)")
			headers.Set("x-stainless-lang", "js")
			headers.Set("x-stainless-package-version", "0.94.0")
			headers.Set("x-stainless-os", "Linux")
			headers.Set("x-stainless-arch", "arm64")
			headers.Set("x-stainless-runtime", "node")
			headers.Set("x-stainless-runtime-version", "v24.3.0")
			headers.Set("x-stainless-retry-count", "0")
			headers.Set("x-stainless-timeout", "600")
			headers.Set("x-app", "cli")
			headers.Set("anthropic-dangerous-direct-browser-access", "true")
		}
	case modelcheckprofile.ProtocolGeminiNative:
		if profileID != "" && profileID != "profile_gemini_native_v1beta" {
			return nil, errors.New("J3b Business Gemini native profile is unsupported")
		}
		if providerCode != "" && providerCode != "gemini" {
			return nil, errors.New("J3b Business Gemini profile/provider mismatch")
		}
		if credentialType == "oauth" {
			return nil, errors.New("J3b Business oauth credential is incompatible with protocol " + protocolCode)
		}
		if credentialType == "api_key" {
			headers.Set("x-goog-api-key", token)
		} else {
			headers.Set("Authorization", "Bearer "+token)
		}
	default:
		// OpenAI-compatible profiles use Bearer for both API keys and OAuth.
		// google_oauth is not a valid credential for these profiles.
		if credentialType == "google_oauth" {
			return nil, errors.New("J3b Business google_oauth credential is incompatible with protocol " + protocolCode)
		}
		headers.Set("Authorization", "Bearer "+token)
	}
	return headers, nil
}

func openAIOAuthCodexAdapter(providerCode, profileID, credentialType string, protocol modelcheckprofile.Protocol, endpointMode string) (string, error) {
	if credentialType != "oauth" {
		return "", nil
	}
	if providerCode == "gpt" && profileID == "profile_gpt_openai_v1" {
		if protocol != modelcheckprofile.ProtocolOpenAIResponses || (endpointMode != modelcheckprofile.EndpointModeResponsesJSON && endpointMode != modelcheckprofile.EndpointModeResponsesSSE) {
			return "", errors.New("J3b Business OpenAI OAuth Codex endpoint mode is unsupported")
		}
		return modelcheckprobe.AdapterOpenAIOAuthCodex, nil
	}
	if protocol == modelcheckprofile.ProtocolOpenAIResponses || protocol == modelcheckprofile.ProtocolOpenAIChat {
		return "", errors.New("J3b Business OAuth credential is incompatible with OpenAI provider profile")
	}
	return "", nil
}

func buildProxyClient(secret string, profileID sql.NullString, enabled sql.NullBool, proxyType, host sql.NullString, port sql.NullInt64, username, password sql.NullString) (*http.Client, error) {
	if !profileID.Valid || strings.TrimSpace(profileID.String) == "" {
		return nil, nil
	}
	if !enabled.Valid || !enabled.Bool || strings.TrimSpace(host.String) == "" || port.Int64 < 1 || port.Int64 > 65535 {
		return nil, errors.New("J3b Business proxy profile is unavailable")
	}
	scheme := strings.ToLower(strings.TrimSpace(proxyType.String))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return nil, errors.New("J3b Business proxy protocol is unsupported")
	}
	proxyURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(strings.TrimSpace(host.String), strconv.FormatInt(port.Int64, 10))}
	if user := strings.TrimSpace(username.String); user != "" {
		if strings.TrimSpace(password.String) == "" {
			return nil, errors.New("J3b Business proxy password is unavailable")
		}
		plain, err := decryptProxyPassword(secret, password.String)
		if err != nil {
			return nil, errors.New("J3b Business proxy password is unavailable")
		}
		proxyURL.User = url.UserPassword(user, plain)
	}
	client, err := upstreamhttp.SharedClient(proxyURL.String(), upstreamhttp.TransportOptions{})
	if err != nil {
		return nil, fmt.Errorf("J3b Business proxy client: %w", err)
	}
	return client, nil
}

func decryptProxyPassword(secret, envelope string) (string, error) {
	plain, err := decryptCredentialPlaintext(secret, envelope)
	if err != nil {
		return "", err
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(plain), &fields); err != nil {
		return "", errors.New("J3b Business proxy password envelope is invalid")
	}
	value, ok := fields["password"].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", errors.New("J3b Business proxy password is missing")
	}
	return value, nil
}

func accountUnavailableAt(expiresAt, cooldownUntil, lastErrorCode string, now time.Time, _ bool) bool {
	if strings.TrimSpace(lastErrorCode) == "account_expired" {
		return true
	}
	parse := func(raw string) (time.Time, bool) {
		value := strings.TrimSpace(raw)
		if value == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, true
		}
		return parsed, true
	}
	if expiry, present := parse(expiresAt); present && (expiry.IsZero() || !expiry.After(now)) {
		return true
	}
	if cooldown, present := parse(cooldownUntil); present && (cooldown.IsZero() || cooldown.After(now)) {
		return true
	}
	return false
}

func (s *BusinessTargetSource) nowUTC() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
