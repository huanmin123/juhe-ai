package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementClientIPPolicyQueries interface {
	LockManagementClientIPRegistry(
		ctx context.Context,
		ipHash string,
	) (postgresqueries.LockManagementClientIPRegistryRow, error)
	DisableActiveManagementClientIPPolicies(
		ctx context.Context,
		arg postgresqueries.DisableActiveManagementClientIPPoliciesParams,
	) (int64, error)
	InsertManagementClientIPAllowlistPolicy(
		ctx context.Context,
		arg postgresqueries.InsertManagementClientIPAllowlistPolicyParams,
	) (postgresqueries.JuheStatsClientIpPolicy, error)
	DisableActiveManagementClientIPAllowlistPolicies(
		ctx context.Context,
		arg postgresqueries.DisableActiveManagementClientIPAllowlistPoliciesParams,
	) (int64, error)
}

func (s *Store) ManagementClientIPPolicyInTx(
	ctx context.Context,
	fn func(context.Context, port.ManagementClientIPPolicyStore) error,
) error {
	return managementClientIPPolicyInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementClientIPPolicyQueries {
			return s.queries().WithTx(tx)
		},
		fn,
	)
}

func managementClientIPPolicyInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementClientIPPolicyQueries,
	fn func(context.Context, port.ManagementClientIPPolicyStore) error,
) error {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin management client IP policy tx: %w", err)
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

	txStore := managementClientIPPolicyTxStore{
		queries: queriesForTx(tx),
	}
	if err := fn(ctx, txStore); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit management client IP policy tx rolled back: %w", err)
		}
		return fmt.Errorf("commit management client IP policy tx: %w", err)
	}
	committed = true
	return nil
}

type managementClientIPPolicyTxStore struct {
	queries managementClientIPPolicyQueries
}

func (s managementClientIPPolicyTxStore) LockManagementClientIPRegistry(
	ctx context.Context,
	ipHash string,
) (port.ManagementClientIPRegistryRow, bool, error) {
	return lockManagementClientIPRegistry(ctx, s.queries, ipHash)
}

func (s managementClientIPPolicyTxStore) DisableActiveManagementClientIPPolicies(
	ctx context.Context,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	return disableActiveManagementClientIPPolicies(ctx, s.queries, input)
}

func (s managementClientIPPolicyTxStore) InsertManagementClientIPAllowlistPolicy(
	ctx context.Context,
	input port.ManagementClientIPAllowlistCreateInput,
) (port.ManagementClientIPPolicySummary, error) {
	return insertManagementClientIPAllowlistPolicy(ctx, s.queries, input)
}

func (s managementClientIPPolicyTxStore) DisableActiveManagementClientIPAllowlistPolicies(
	ctx context.Context,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	return disableActiveManagementClientIPAllowlistPolicies(ctx, s.queries, input)
}

func lockManagementClientIPRegistry(
	ctx context.Context,
	q managementClientIPPolicyQueries,
	ipHash string,
) (port.ManagementClientIPRegistryRow, bool, error) {
	row, err := q.LockManagementClientIPRegistry(ctx, ipHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementClientIPRegistryRow{}, false, nil
	}
	if err != nil {
		return port.ManagementClientIPRegistryRow{}, false, fmt.Errorf(
			"lock management client IP registry: %w",
			err,
		)
	}
	return port.ManagementClientIPRegistryRow{
		IPHash:   row.IpHash,
		ClientIP: row.ClientIp,
	}, true, nil
}

func disableActiveManagementClientIPPolicies(
	ctx context.Context,
	q managementClientIPPolicyQueries,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	now := managementClientIPPolicyTimeText(input.Now)
	count, err := q.DisableActiveManagementClientIPPolicies(
		ctx,
		postgresqueries.DisableActiveManagementClientIPPoliciesParams{
			DisabledAt:                now,
			DisabledBySystemAccountID: input.ActorSystemAccountID,
			DisabledReason:            input.Reason,
			UpdatedAt:                 now,
			IpHash:                    input.IPHash,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("disable active management client IP policies: %w", err)
	}
	return count, nil
}

func insertManagementClientIPAllowlistPolicy(
	ctx context.Context,
	q managementClientIPPolicyQueries,
	input port.ManagementClientIPAllowlistCreateInput,
) (port.ManagementClientIPPolicySummary, error) {
	now := managementClientIPPolicyTimeText(input.Now)
	row, err := q.InsertManagementClientIPAllowlistPolicy(
		ctx,
		postgresqueries.InsertManagementClientIPAllowlistPolicyParams{
			ID:                       input.ID,
			IpHash:                   input.IPHash,
			Reason:                   pgTextPtr(input.Reason),
			CreatedBySystemAccountID: input.ActorSystemAccountID,
			CreatedAt:                now,
			UpdatedAt:                now,
		},
	)
	if err != nil {
		return port.ManagementClientIPPolicySummary{}, fmt.Errorf(
			"insert management client IP allowlist policy: %w",
			err,
		)
	}
	return managementClientIPPolicySummary(row)
}

func disableActiveManagementClientIPAllowlistPolicies(
	ctx context.Context,
	q managementClientIPPolicyQueries,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	now := managementClientIPPolicyTimeText(input.Now)
	count, err := q.DisableActiveManagementClientIPAllowlistPolicies(
		ctx,
		postgresqueries.DisableActiveManagementClientIPAllowlistPoliciesParams{
			DisabledAt:                now,
			DisabledBySystemAccountID: input.ActorSystemAccountID,
			DisabledReason:            input.Reason,
			UpdatedAt:                 now,
			IpHash:                    input.IPHash,
		},
	)
	if err != nil {
		return 0, fmt.Errorf(
			"disable active management client IP allowlist policies: %w",
			err,
		)
	}
	return count, nil
}

func managementClientIPPolicySummary(
	row postgresqueries.JuheStatsClientIpPolicy,
) (port.ManagementClientIPPolicySummary, error) {
	createdAt, err := managementClientIPPolicyParseTime(row.CreatedAt)
	if err != nil {
		return port.ManagementClientIPPolicySummary{}, fmt.Errorf(
			"parse management client IP policy created_at: %w",
			err,
		)
	}
	updatedAt, err := managementClientIPPolicyParseTime(row.UpdatedAt)
	if err != nil {
		return port.ManagementClientIPPolicySummary{}, fmt.Errorf(
			"parse management client IP policy updated_at: %w",
			err,
		)
	}
	expiresAt, err := managementClientIPPolicyParseOptionalTime(row.ExpiresAt)
	if err != nil {
		return port.ManagementClientIPPolicySummary{}, fmt.Errorf(
			"parse management client IP policy expires_at: %w",
			err,
		)
	}
	disabledAt, err := managementClientIPPolicyParseOptionalTime(row.DisabledAt)
	if err != nil {
		return port.ManagementClientIPPolicySummary{}, fmt.Errorf(
			"parse management client IP policy disabled_at: %w",
			err,
		)
	}
	return port.ManagementClientIPPolicySummary{
		ID:                        row.ID,
		IPHash:                    row.IpHash,
		PolicyType:                port.ManagementClientIPPolicyType(row.PolicyType),
		Status:                    port.ManagementClientIPPolicyStatus(row.Status),
		Reason:                    textPtr(row.Reason),
		ExpiresAt:                 expiresAt,
		CreatedBySystemAccountID:  row.CreatedBySystemAccountID,
		CreatedAt:                 createdAt,
		UpdatedAt:                 updatedAt,
		DisabledAt:                disabledAt,
		DisabledBySystemAccountID: textPtr(row.DisabledBySystemAccountID),
		DisabledReason:            textPtr(row.DisabledReason),
	}, nil
}

func managementClientIPPolicyTimeText(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func managementClientIPPolicyParseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func managementClientIPPolicyParseOptionalTime(value pgtype.Text) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := managementClientIPPolicyParseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

var _ port.ManagementClientIPPolicyTransactor = (*Store)(nil)
var _ port.ManagementClientIPPolicyStore = managementClientIPPolicyTxStore{}
