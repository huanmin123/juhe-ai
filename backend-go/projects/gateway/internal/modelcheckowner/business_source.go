package modelcheckowner

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
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
	return &BusinessTargetSource{db: db, postgres: postgres, credentialSecret: credentialSecret}, nil
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
		driver, dsn = "sqlite", "file:"+cfg.BusinessDatabasePath+"?mode="+mode+"&_pragma=busy_timeout(5000)"
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
// account inside the same Gateway process and business scope. It never uses
// the primary target ID as a fallback.
func (s *BusinessTargetSource) ComparisonResolver() Resolver {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, request RunRequest) (Target, error) {
		if !request.TrustedComparison || strings.TrimSpace(request.TrustedComparisonAccountID) == "" {
			return Target{}, errors.New("J3b trusted comparison target is not configured")
		}
		comparisonRequest := request
		comparisonRequest.TargetID = request.TrustedComparisonAccountID
		comparisonRequest.ConfigRevision = request.TrustedComparisonConfigRevision
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
	contracts := map[string]string{"accounts": "id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,config_revision,status,schedulable,credentials_encrypted,deleted_at", "provider_protocol_profiles": "id,enabled,base_url", "group_accounts": "account_id,system_account_id,group_id,enabled", "groups": "id,enabled", "model_quality_policies": "system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes", "account_supported_models": "account_id,model", "account_model_mappings": "account_id,source_model,source_endpoint_family,upstream_model,enabled"}
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
	query := `SELECT a.provider_code,a.provider_protocol_profile_id,a.protocol_code,a.config_revision,a.status,a.schedulable,a.credentials_encrypted,p.base_url,p.enabled FROM ` + s.table("accounts") + ` a JOIN ` + s.table("provider_protocol_profiles") + ` p ON p.id=a.provider_protocol_profile_id JOIN ` + s.table("group_accounts") + ` ga ON ga.account_id=a.id AND ga.system_account_id=a.system_account_id AND ga.enabled=1 JOIN ` + s.table("groups") + ` g ON g.id=ga.group_id AND g.enabled=1 WHERE a.id=` + s.placeholder(1) + ` AND a.system_account_id=` + s.placeholder(2) + ` AND a.deleted_at IS NULL`
	var provider, profileID, protocolCode, encrypted, baseURL, status string
	var revision int64
	var schedulable, profileEnabled bool
	if err := tx.QueryRowContext(ctx, query, request.TargetID, request.SystemAccountID).Scan(&provider, &profileID, &protocolCode, &revision, &status, &schedulable, &encrypted, &baseURL, &profileEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Target{}, errors.New("J3b Business account does not exist or is outside scope")
		}
		return Target{}, fmt.Errorf("read J3b Business target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Target{}, fmt.Errorf("commit J3b Business target read: %w", err)
	}
	if request.ConfigRevision != "" && request.ConfigRevision != strconv.FormatInt(revision, 10) {
		return Target{}, errors.New("J3b Business account config revision is stale")
	}
	qualityRecovery := request.TriggerKind == string(SchedulerQualityRecovery)
	if (qualityRecovery && status != "quality_isolated") || (!qualityRecovery && status != "active" && status != "temporary_unavailable" && status != "rate_limited") {
		return Target{}, errors.New("J3b Business account is unavailable")
	}
	if !schedulable || !profileEnabled {
		return Target{}, errors.New("J3b Business account is not schedulable")
	}
	profile, ok := modelcheckprofile.Find(provider, profileID)
	if !ok {
		return Target{}, errors.New("J3b Business provider profile does not support model")
	}
	upstreamModel, err := resolveConfiguredUpstreamModel(ctx, s.db, s.postgres, request.TargetID, profile, request.Model)
	if err != nil {
		return Target{}, err
	}
	if upstreamModel == "" {
		return Target{}, errors.New("J3b Business account model restriction does not allow model")
	}
	token, err := decryptCredential(s.credentialSecret, encrypted)
	if err != nil {
		return Target{}, err
	}
	headers := http.Header{}
	if protocolCode == "anthropic" || profile.Protocol == modelcheckprofile.ProtocolAnthropic {
		headers.Set("x-api-key", token)
		headers.Set("anthropic-version", "2023-06-01")
	} else {
		headers.Set("Authorization", "Bearer "+token)
	}
	return Target{Endpoint: strings.TrimRight(baseURL, "/"), ProviderCode: provider, ConfigRevision: strconv.FormatInt(revision, 10), Protocol: profile.Protocol, UpstreamModel: upstreamModel, Headers: headers, Prompt: "Reply with exactly: OK-MODEL-CHECK"}, nil
}

// BuildRequest freezes the Business target and quality policy in one
// read-only transaction boundary. Credentials remain in the resolver's
// process memory and are never copied into the request snapshot.
func (s *BusinessTargetSource) BuildRequest(ctx context.Context, actorSystemAccountID string, command RunCommand) (RunRequest, error) {
	if s == nil || strings.TrimSpace(actorSystemAccountID) == "" {
		return RunRequest{}, errors.New("J3b Business request actor is required")
	}
	if command.TargetType == "" {
		command.TargetType = "account"
	}
	if command.TargetType != "account" || strings.TrimSpace(command.TargetID) == "" || strings.TrimSpace(command.Model) == "" {
		return RunRequest{}, errors.New("J3b Business request target is incomplete")
	}
	target, err := s.Resolve(ctx, RunRequest{SystemAccountID: actorSystemAccountID, TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model})
	if err != nil {
		return RunRequest{}, err
	}
	profile, revision, threshold, action, recoveryInterval, err := s.readPolicy(ctx, actorSystemAccountID)
	if err != nil {
		return RunRequest{}, err
	}
	selectedProfile := strings.TrimSpace(command.Profile)
	if selectedProfile == "" {
		selectedProfile = profile
	}
	if selectedProfile != "quick" && selectedProfile != "full" {
		return RunRequest{}, errors.New("J3b Business policy profile is invalid")
	}
	if command.Profile != "" && selectedProfile != profile {
		return RunRequest{}, errors.New("J3b request profile differs from frozen policy")
	}
	request := RunRequest{TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model, Profile: selectedProfile, SystemAccountID: actorSystemAccountID, ActorSystemAccountID: actorSystemAccountID, ProviderCode: target.ProviderCode, Threshold: threshold, PenaltyAction: action, RecoveryIntervalMinutes: recoveryInterval, ConfigRevision: target.ConfigRevision, PolicyRevision: revision, ProbeSetVersion: modelcheckprofile.QuickProbeSetVersion, IdentityKey: actorSystemAccountID + ":" + command.TargetID + ":" + command.Model + ":" + selectedProfile}
	if !command.TrustedComparison {
		return request, nil
	}
	if selectedProfile != "full" || strings.TrimSpace(command.TrustedComparisonID) == "" || command.TrustedComparisonID == command.TargetID {
		return RunRequest{}, errors.New("J3b trusted comparison requires a distinct full-profile account")
	}
	comparison, err := s.Resolve(ctx, RunRequest{SystemAccountID: actorSystemAccountID, TargetType: "account", TargetID: command.TrustedComparisonID, Model: command.Model})
	if err != nil {
		return RunRequest{}, fmt.Errorf("resolve J3b trusted comparison: %w", err)
	}
	request.TrustedComparison = true
	request.TrustedComparisonAccountID = command.TrustedComparisonID
	request.TrustedComparisonConfigRevision = comparison.ConfigRevision
	request.IdentityKey += ":comparison:" + command.TrustedComparisonID + ":" + comparison.ConfigRevision
	return request, nil
}

func (s *BusinessTargetSource) readPolicy(ctx context.Context, systemAccountID string) (profile, revision string, threshold int, action string, recoveryInterval int, err error) {
	profile, revision, threshold, action, recoveryInterval = "quick", "0", 70, "fallback", 10
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", "", 0, "", 0, fmt.Errorf("open J3b Business policy transaction: %w", err)
	}
	defer tx.Rollback()
	query := `SELECT revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes FROM ` + s.table("model_quality_policies") + ` WHERE system_account_id=` + s.placeholder(1) + ` LIMIT 1`
	if scanErr := tx.QueryRowContext(ctx, query, systemAccountID).Scan(&revision, &profile, &threshold, &action, &recoveryInterval); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", 0, "", 0, fmt.Errorf("read J3b Business quality policy: %w", scanErr)
	}
	if err := tx.Commit(); err != nil {
		return "", "", 0, "", 0, fmt.Errorf("commit J3b Business policy read: %w", err)
	}
	if threshold < 40 || threshold > 100 || recoveryInterval < 10 || recoveryInterval > 10080 || (profile != "quick" && profile != "full") || (action != "disable" && action != "fallback" && action != "quality_isolate") {
		return "", "", 0, "", 0, errors.New("J3b Business quality policy is invalid")
	}
	return profile, revision, threshold, action, recoveryInterval, nil
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
	var fields map[string]any
	if json.Unmarshal(plain, &fields) == nil {
		for _, key := range []string{"api_key", "access_token", "token"} {
			if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
	}
	if strings.TrimSpace(string(plain)) == "" {
		return "", errors.New("J3b Business credential is empty")
	}
	return strings.TrimSpace(string(plain)), nil
}
