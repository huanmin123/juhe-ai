package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

const responseInspectionPolicyColumns = `
id, name, enabled, priority, scope_type, protocol_code, provider_code,
match_json, action, notes, created_at, updated_at`

func (s *Store) ListResponseInspectionPolicies(ctx context.Context, limit int) ([]port.ResponseInspectionPolicy, error) {
	rows, err := s.pool.Query(ctx, `
SELECT `+responseInspectionPolicyColumns+`
FROM juhe_business.response_inspection_policies
ORDER BY priority ASC, updated_at DESC, id ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list response inspection policies: %w", err)
	}
	defer rows.Close()
	policies := make([]port.ResponseInspectionPolicy, 0, min(limit, 100))
	for rows.Next() {
		policy, err := scanResponseInspectionPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate response inspection policies: %w", err)
	}
	return policies, nil
}

func (s *Store) ResponseInspectionPolicyInTx(
	ctx context.Context,
	fn func(context.Context, port.ResponseInspectionPolicyTxStore) error,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin response inspection policy tx: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if err := fn(ctx, responseInspectionPolicyTxStore{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapResponseInspectionPolicyStoreError("commit response inspection policy tx", err)
	}
	committed = true
	return nil
}

type responseInspectionPolicyTxStore struct{ tx pgx.Tx }

func (s responseInspectionPolicyTxStore) CountResponseInspectionPolicies(ctx context.Context, limit int) (int, error) {
	if _, err := s.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('response_inspection_policies.capacity'))`); err != nil {
		return 0, mapResponseInspectionPolicyStoreError("lock response inspection policy capacity", err)
	}
	rows, err := s.tx.Query(ctx, `
SELECT id
FROM juhe_business.response_inspection_policies
LIMIT $1`, limit)
	if err != nil {
		return 0, mapResponseInspectionPolicyStoreError("read response inspection policy capacity", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan response inspection policy capacity: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate response inspection policy capacity: %w", err)
	}
	return count, nil
}

func (s responseInspectionPolicyTxStore) ResponseInspectionProviderSupportsProtocol(
	ctx context.Context,
	providerCode string,
	protocolCode string,
) (bool, error) {
	var exists bool
	err := s.tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM juhe_business.provider_protocol_profiles profiles
  INNER JOIN juhe_business.providers providers ON providers.code = profiles.provider_code
  WHERE profiles.provider_code = $1
    -- Node-owned PostgreSQL deployments retain INTEGER flags while the
    -- Go migration catalog uses BOOLEAN. This read must work with either.
    AND providers.enabled::text IN ('true', '1')
    AND profiles.enabled::text IN ('true', '1')
    AND profiles.protocol_code = $2
)`, providerCode, protocolCode).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("validate response inspection policy provider protocol: %w", err)
	}
	return exists, nil
}

func (s responseInspectionPolicyTxStore) FindResponseInspectionPolicyForUpdate(
	ctx context.Context,
	id string,
) (port.ResponseInspectionPolicy, bool, error) {
	policy, err := scanResponseInspectionPolicy(s.tx.QueryRow(ctx, `
SELECT `+responseInspectionPolicyColumns+`
FROM juhe_business.response_inspection_policies
WHERE id = $1
FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ResponseInspectionPolicy{}, false, nil
	}
	if err != nil {
		return port.ResponseInspectionPolicy{}, false, err
	}
	return policy, true, nil
}

func (s responseInspectionPolicyTxStore) CreateResponseInspectionPolicy(
	ctx context.Context,
	input port.ResponseInspectionPolicyWriteInput,
) (port.ResponseInspectionPolicy, error) {
	matchJSON, err := json.Marshal(input.Match)
	if err != nil {
		return port.ResponseInspectionPolicy{}, fmt.Errorf("marshal response inspection policy match: %w", err)
	}
	policy, err := scanResponseInspectionPolicy(s.tx.QueryRow(ctx, `
INSERT INTO juhe_business.response_inspection_policies (
  id, name, enabled, priority, scope_type, protocol_code, provider_code,
  match_json, action, notes, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING `+responseInspectionPolicyColumns,
		input.ID, input.Name, boolInt(input.Enabled), input.Priority, input.ScopeType,
		input.ProtocolCode, input.ProviderCode, string(matchJSON), input.Action, input.Notes,
		input.CreatedAt, input.UpdatedAt,
	))
	if err != nil {
		return port.ResponseInspectionPolicy{}, mapResponseInspectionPolicyStoreError("create response inspection policy", err)
	}
	return policy, nil
}

func (s responseInspectionPolicyTxStore) UpdateResponseInspectionPolicy(
	ctx context.Context,
	input port.ResponseInspectionPolicyWriteInput,
) (port.ResponseInspectionPolicy, bool, error) {
	matchJSON, err := json.Marshal(input.Match)
	if err != nil {
		return port.ResponseInspectionPolicy{}, false, fmt.Errorf("marshal response inspection policy match: %w", err)
	}
	policy, err := scanResponseInspectionPolicy(s.tx.QueryRow(ctx, `
UPDATE juhe_business.response_inspection_policies
SET name = $2, enabled = $3, priority = $4, scope_type = $5, protocol_code = $6,
    provider_code = $7, match_json = $8, action = $9, notes = $10, updated_at = $11
WHERE id = $1
RETURNING `+responseInspectionPolicyColumns,
		input.ID, input.Name, boolInt(input.Enabled), input.Priority, input.ScopeType,
		input.ProtocolCode, input.ProviderCode, string(matchJSON), input.Action, input.Notes,
		input.UpdatedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ResponseInspectionPolicy{}, false, nil
	}
	if err != nil {
		return port.ResponseInspectionPolicy{}, false, mapResponseInspectionPolicyStoreError("update response inspection policy", err)
	}
	return policy, true, nil
}

func (s responseInspectionPolicyTxStore) DeleteResponseInspectionPolicy(ctx context.Context, id string) (bool, error) {
	result, err := s.tx.Exec(ctx, `
DELETE FROM juhe_business.response_inspection_policies
WHERE id = $1`, id)
	if err != nil {
		return false, mapResponseInspectionPolicyStoreError("delete response inspection policy", err)
	}
	return result.RowsAffected() == 1, nil
}

type responseInspectionPolicyScanner interface{ Scan(...any) error }

func scanResponseInspectionPolicy(row responseInspectionPolicyScanner) (port.ResponseInspectionPolicy, error) {
	var policy port.ResponseInspectionPolicy
	var enabled int
	var matchJSON string
	if err := row.Scan(
		&policy.ID, &policy.Name, &enabled, &policy.Priority, &policy.ScopeType,
		&policy.ProtocolCode, &policy.ProviderCode, &matchJSON, &policy.Action,
		&policy.Notes, &policy.CreatedAt, &policy.UpdatedAt,
	); err != nil {
		return port.ResponseInspectionPolicy{}, err
	}
	if err := json.Unmarshal([]byte(matchJSON), &policy.Match); err != nil {
		return port.ResponseInspectionPolicy{}, fmt.Errorf("decode response inspection policy match: %w", err)
	}
	policy.Enabled = enabled == 1
	policy.DefaultRule = false
	policy.Editable = true
	return policy, nil
}

func mapResponseInspectionPolicyStoreError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001", "40P01", "55P03":
			return fmt.Errorf("%s: %w", operation, port.ErrResponseInspectionPolicyConflict)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ port.ResponseInspectionPolicyStore = (*Store)(nil)
var _ port.ResponseInspectionPolicyTxStore = responseInspectionPolicyTxStore{}
