package modelchecksource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckresolver"
	_ "modernc.org/sqlite"
)

// SQLiteReader provides the same candidate and replay contract as
// PostgresReader while the Node runtime is still the sole SQLite writer. It
// opens the business file read-only and uses query_only so a missing schema or
// lock fails visibly instead of falling back to Node IPC or a writable handle.
type SQLiteReader struct {
	common *PostgresReader
	db     *sql.DB
	close  bool
}

func OpenSQLiteReader(path, credentialSecret, identitySecret string, now func() time.Time) (*SQLiteReader, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("model check SQLite reader database path is required")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open model check SQLite reader: %w", err)
	}
	reader, err := NewSQLiteReader(db, credentialSecret, identitySecret, now)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	reader.close = true
	return reader, nil
}

func NewSQLiteReader(db *sql.DB, credentialSecret, identitySecret string, now func() time.Time) (*SQLiteReader, error) {
	common, err := NewPostgresReader(db, credentialSecret, identitySecret, now)
	if err != nil {
		return nil, err
	}
	return &SQLiteReader{common: common, db: db}, nil
}

func (r *SQLiteReader) Close() error {
	if r != nil && r.close && r.db != nil {
		return r.db.Close()
	}
	return nil
}

func (r *SQLiteReader) CheckContract(ctx context.Context) error {
	if r == nil || r.common == nil || r.db == nil {
		return errors.New("model check SQLite reader is not initialized")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open model check SQLite reader contract transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, sqliteContractSQL)
	if err != nil {
		return fmt.Errorf("verify model check SQLite reader contract: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close model check SQLite reader contract result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model check SQLite reader contract transaction: %w", err)
	}
	return nil
}

func (r *SQLiteReader) FreezeTarget(ctx context.Context, request Request) (FrozenTarget, error) {
	if r == nil || r.common == nil || r.db == nil {
		return FrozenTarget{}, errors.New("model check SQLite reader is not initialized")
	}
	if strings.TrimSpace(request.SystemAccountID) == "" || strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.Model) == "" {
		return FrozenTarget{}, errors.New("model check SQLite reader request is incomplete")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FrozenTarget{}, fmt.Errorf("open model check SQLite reader transaction: %w", err)
	}
	defer tx.Rollback()
	candidate, err := r.common.loadCandidateWithQuery(ctx, tx, request, sqliteCandidateSQL, "")
	if err != nil {
		return FrozenTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return FrozenTarget{}, fmt.Errorf("commit model check SQLite reader transaction: %w", err)
	}
	frozen, err := Freeze(request, candidate, r.common.identitySecret)
	if err != nil {
		return FrozenTarget{}, fmt.Errorf("freeze model check SQLite target: %w", err)
	}
	return frozen, nil
}

// ResolveManagementSystemAccount is the SQLite equivalent of the PostgreSQL
// admin target-scope read. It remains read-only until the final SQLite writer
// handoff; no fallback to Node IPC is permitted.
func (r *SQLiteReader) ResolveManagementSystemAccount(ctx context.Context, accountID string) (string, error) {
	if r == nil || r.db == nil || strings.TrimSpace(accountID) == "" {
		return "", errors.New("model check SQLite management account scope is invalid")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("open model check SQLite management scope transaction: %w", err)
	}
	defer tx.Rollback()
	var systemAccountID string
	if err := tx.QueryRowContext(ctx, `SELECT system_account_id FROM accounts WHERE id=? AND deleted_at IS NULL LIMIT 1`, strings.TrimSpace(accountID)).Scan(&systemAccountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("model check account does not exist")
		}
		return "", fmt.Errorf("read model check SQLite management account scope: %w", err)
	}
	if strings.TrimSpace(systemAccountID) == "" {
		return "", errors.New("model check account system scope is empty")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit model check SQLite management scope transaction: %w", err)
	}
	return systemAccountID, nil
}

func (r *SQLiteReader) Resolve(ctx context.Context, resolution modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
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
	resolver, err := modelcheckresolver.New([]modelcheckresolver.Snapshot{frozen.Execution}, r.common.credentialSecret)
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, err
	}
	return resolver.Resolve(ctx, resolution)
}

const sqliteContractSQL = `
SELECT a.id, a.config_revision, a.authorization_instance_source_account_id,
       source.id, profile.id, binding.group_id, group_row.id,
       account_authorization.id, proxy.id, supported.model, mapping.source_model
FROM accounts a
LEFT JOIN accounts source ON source.id=a.authorization_instance_source_account_id
LEFT JOIN provider_protocol_profiles profile ON profile.id=a.provider_protocol_profile_id
LEFT JOIN group_accounts binding ON binding.account_id=a.id
LEFT JOIN groups group_row ON group_row.id=binding.group_id
LEFT JOIN resource_authorizations account_authorization ON account_authorization.id=a.authorization_instance_authorization_id
LEFT JOIN proxy_profiles proxy ON proxy.id=a.proxy_profile_id
LEFT JOIN account_supported_models supported ON supported.account_id=a.id
LEFT JOIN account_model_mappings mapping ON mapping.account_id=a.id
WHERE 0`

const sqliteCandidateSQL = `
SELECT
  a.id, a.system_account_id, a.name, COALESCE(a.authorization_instance_owner_system_account_id, a.system_account_id), a.config_revision, a.status, a.schedulable, a.health_check_endpoint_mode,
  COALESCE(a.authorization_instance_authorization_id, ''), source.id, source.config_revision,
  source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, source.type, source.credentials_encrypted,
  profile.enabled, profile.base_url, profile.updated_at, binding.group_id,
  proxy.id, proxy.enabled, proxy.type, proxy.host, proxy.port, proxy.username, proxy.password_encrypted
FROM accounts a
JOIN accounts source
  ON source.id=CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE a.authorization_instance_source_account_id END
  AND source.deleted_at IS NULL
JOIN provider_protocol_profiles profile
  ON profile.id=source.provider_protocol_profile_id AND profile.enabled=1
JOIN (
  SELECT ga.group_id, ga.account_authorization_id, ga.account_id
  FROM group_accounts ga
  WHERE ga.system_account_id=$1 AND ga.enabled=1
) binding ON binding.account_id=a.id
JOIN groups group_row ON group_row.id=binding.group_id AND group_row.enabled=1
LEFT JOIN resource_authorizations account_authorization ON account_authorization.id=a.authorization_instance_authorization_id
LEFT JOIN proxy_profiles proxy ON proxy.id=source.proxy_profile_id
WHERE a.id=$2
      AND a.system_account_id=$1
      AND a.deleted_at IS NULL
  AND ((a.status='quality_isolated' AND $4) OR (a.schedulable=1 AND a.status IN ('active','temporary_unavailable','rate_limited') AND (a.account_expires_at IS NULL OR a.account_expires_at>$3)))
  AND (a.authorization_instance_authorization_id IS NULL OR binding.account_authorization_id=a.authorization_instance_authorization_id)
  AND (
    group_row.system_account_id=a.system_account_id OR EXISTS (
      SELECT 1 FROM resource_authorizations group_authorization
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
      AND ($4 OR (source.status IN ('active','temporary_unavailable','rate_limited') AND source.schedulable=1 AND source.last_error_code IS NOT 'account_expired'))
      AND (source.account_expires_at IS NULL OR source.account_expires_at>$3)
    )
  )
LIMIT 1`
