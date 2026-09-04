package oauthrefresh

import (
	"context"
	"database/sql"
)

// MaxAuthorizationExpirySweepBatchSize mirrors
// maxAuthorizationExpirySweepBatchSize (authorization-sweep-limits.ts).
const MaxAuthorizationExpirySweepBatchSize = 20

// GrantFinalizer receives each grant flipped to expired inside the sweep
// transaction's semantics (Node syncResourceAuthorizationGrantRuntimeAsync:
// user-grant/team-member runtime sync, quota window scope bindings and the
// account-health-jobs input fanout). The jobs process wires platform-owned
// implementations; nil keeps the sweep's narrow ownership (grant expiry only).
type GrantFinalizer interface {
	FinalizeExpiredGrant(ctx context.Context, grant ResourceAuthorizationGrant, actor string) error
}

// ResourceAuthorizationGrant mirrors ResourceAuthorizationGrantRow (the
// columns the sweep and the finalizer need).
type ResourceAuthorizationGrant struct {
	ID                   string
	ResourceType         string
	ResourceID           string
	OwnerSystemAccountID string
	GranteeType          string
	GranteeID            string
	Status               string
	RevokedAt            string
	RevokedBy            string
	CreatedBy            string
	ExpiresAt            string
	UpdatedAt            string
}

// SweepResult reports the sweep outcome.
type SweepResult struct {
	Expired int
}

// RunAuthorizationExpirySweep mirrors expireDueResourceAuthorizations(Async):
// grants with status active/paused and expires_at <= now become expired with
// revoked_at = COALESCE(revoked_at, now), ordered expires_at/updated_at/id,
// batched (default 20). Postgres locks the batch with FOR UPDATE SKIP LOCKED.
func (s *Store) RunAuthorizationExpirySweep(ctx context.Context, finalizer GrantFinalizer, limit int) (SweepResult, error) {
	ctx = ensureCtx(ctx)
	if limit <= 0 {
		limit = MaxAuthorizationExpirySweepBatchSize
	}
	now := s.nowISO()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SweepResult{}, err
	}
	defer tx.Rollback()

	query := `SELECT id, resource_type, resource_id, owner_system_account_id, grantee_type, grantee_id,
		status, revoked_at, revoked_by, created_by, expires_at, updated_at
	FROM ` + s.table("resource_authorization_grants") + `
	WHERE status IN ('active', 'paused')
		AND expires_at IS NOT NULL
		AND expires_at <= ?
	ORDER BY expires_at ASC, updated_at ASC, id ASC
	LIMIT ?`
	if s.pg {
		query += `
	FOR UPDATE SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), now, limit)
	if err != nil {
		return SweepResult{}, err
	}
	due := []ResourceAuthorizationGrant{}
	for rows.Next() {
		var (
			grant     ResourceAuthorizationGrant
			revokedAt sql.NullString
			revokedBy sql.NullString
		)
		if err := rows.Scan(&grant.ID, &grant.ResourceType, &grant.ResourceID, &grant.OwnerSystemAccountID, &grant.GranteeType, &grant.GranteeID,
			&grant.Status, &revokedAt, &revokedBy, &grant.CreatedBy, &grant.ExpiresAt, &grant.UpdatedAt); err != nil {
			rows.Close()
			return SweepResult{}, err
		}
		grant.RevokedAt = revokedAt.String
		grant.RevokedBy = revokedBy.String
		due = append(due, grant)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SweepResult{}, err
	}
	if len(due) == 0 {
		return SweepResult{Expired: 0}, tx.Rollback()
	}

	for _, grant := range due {
		actor := grant.RevokedBy
		if actor == "" {
			actor = grant.CreatedBy
		}
		nextRevokedAt := grant.RevokedAt
		if nextRevokedAt == "" {
			nextRevokedAt = now
		}
		updated, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'expired',
				revoked_at = COALESCE(revoked_at, ?),
				updated_at = ?
			WHERE id = ?
				AND status IN ('active', 'paused')`), now, now, grant.ID)
		if err != nil {
			return SweepResult{}, err
		}
		if affected, _ := updated.RowsAffected(); affected != 1 {
			continue
		}
		if finalizer != nil {
			grant.Status = "expired"
			grant.RevokedAt = nextRevokedAt
			grant.UpdatedAt = now
			if err := finalizer.FinalizeExpiredGrant(ctx, grant, actor); err != nil {
				return SweepResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return SweepResult{}, err
	}
	return SweepResult{Expired: len(due)}, nil
}

// FinalizerFunc adapts a function to GrantFinalizer.
type FinalizerFunc func(ctx context.Context, grant ResourceAuthorizationGrant, actor string) error

// FinalizeExpiredGrant implements GrantFinalizer.
func (f FinalizerFunc) FinalizeExpiredGrant(ctx context.Context, grant ResourceAuthorizationGrant, actor string) error {
	return f(ctx, grant, actor)
}
