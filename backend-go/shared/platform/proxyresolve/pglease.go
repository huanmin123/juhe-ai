package proxyresolve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrMemberUnavailable means the selected pool member stopped being
// assignable between selection and the lease write. The caller should treat the
// attempt as pool-unavailable and let the next request reselect.
var ErrMemberUnavailable = errors.New("proxy pool member unavailable")

// TxBeginner is the database subset used by PGLeaseWriter (*sql.DB satisfies
// it). Tests can inject a fake without opening a real transaction.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// PGLeaseWriter is the gateway-only request-time lease writer. It locks the
// pool and account rows, verifies the member is still assignable, and applies
// the generation CAS. A concurrent assignment for the same account returns the
// winner's lease instead of overwriting it, so one request never steals another
// request's member.
type PGLeaseWriter struct {
	db TxBeginner
}

func NewPGLeaseWriter(db TxBeginner) (*PGLeaseWriter, error) {
	if db == nil {
		return nil, errors.New("proxyresolve lease writer requires a database")
	}
	return &PGLeaseWriter{db: db}, nil
}

func (w *PGLeaseWriter) AssignLease(
	ctx context.Context,
	leaseKey, poolID, profileID, reason string,
	expected *Lease,
) (Lease, error) {
	leaseKey = strings.TrimSpace(leaseKey)
	poolID = strings.TrimSpace(poolID)
	profileID = strings.TrimSpace(profileID)
	if leaseKey == "" || poolID == "" || profileID == "" {
		return Lease{}, errors.New("proxy lease assignment requires account, pool and profile")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = AssignmentInitial
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, fmt.Errorf("begin proxy lease assignment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockProxyPool(ctx, tx, poolID); err != nil {
		return Lease{}, err
	}
	if err := lockAccount(ctx, tx, leaseKey, poolID); err != nil {
		return Lease{}, err
	}
	if err := ensureMemberAssignable(ctx, tx, poolID, profileID); err != nil {
		return Lease{}, err
	}
	current, hasCurrent, err := currentLeaseForUpdate(ctx, tx, leaseKey)
	if err != nil {
		return Lease{}, err
	}
	if hasCurrent && current.PoolID == poolID {
		if expected == nil || current.Generation != expected.Generation ||
			current.PoolID != expected.PoolID || current.ProxyProfileID != expected.ProxyProfileID {
			// Another request already repaired/assigned this lease inside the
			// same pool. Use its member; do not churn the generation.
			return current, nil
		}
	}
	assigned, err := upsertLease(ctx, tx, leaseKey, poolID, profileID, reason)
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, fmt.Errorf("commit proxy lease assignment: %w", err)
	}
	return assigned, nil
}

func lockProxyPool(ctx context.Context, tx *sql.Tx, poolID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM juhe_business.proxy_pools
WHERE id = $1 AND enabled = TRUE
FOR UPDATE`, poolID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 代理池不存在或已停用", ErrMemberUnavailable)
	}
	if err != nil {
		return fmt.Errorf("lock proxy pool: %w", err)
	}
	return nil
}

func lockAccount(ctx context.Context, tx *sql.Tx, accountID, poolID string) error {
	var id, currentPoolID string
	err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(proxy_pool_id, '') FROM juhe_business.accounts
WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE`, accountID).Scan(&id, &currentPoolID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 租约账户不存在", ErrMemberUnavailable)
	}
	if err != nil {
		return fmt.Errorf("lock proxy lease account: %w", err)
	}
	// A concurrent account rebind must win: never write a lease for a pool the
	// account no longer references (proxy design §8.3 / pool-bind-07).
	if strings.TrimSpace(currentPoolID) != poolID {
		return fmt.Errorf("%w: 账户代理池绑定已变更", ErrMemberUnavailable)
	}
	return nil
}

func ensureMemberAssignable(ctx context.Context, tx *sql.Tx, poolID, profileID string) error {
	var memberEnabled, profileEnabled bool
	var health string
	var streak int
	err := tx.QueryRowContext(ctx, `SELECT member.enabled, profile.enabled,
       COALESCE(profile.pool_health_status, 'unknown'), profile.pool_health_failure_streak
FROM juhe_business.proxy_pool_members member
JOIN juhe_business.proxy_profiles profile ON profile.id = member.proxy_profile_id
WHERE member.pool_id = $1 AND member.proxy_profile_id = $2
FOR UPDATE OF member, profile`, poolID, profileID).Scan(&memberEnabled, &profileEnabled, &health, &streak)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 池成员不存在", ErrMemberUnavailable)
	}
	if err != nil {
		return fmt.Errorf("lock proxy pool member: %w", err)
	}
	if !memberEnabled || !profileEnabled || HealthState(strings.TrimSpace(health)) == HealthDead ||
		streak >= deadFailureStreak {
		return fmt.Errorf("%w: 池成员已停用或健康不可用", ErrMemberUnavailable)
	}
	return nil
}

func currentLeaseForUpdate(ctx context.Context, tx *sql.Tx, accountID string) (Lease, bool, error) {
	var lease Lease
	err := tx.QueryRowContext(ctx, `SELECT account_id, pool_id, proxy_profile_id, generation, assignment_reason
FROM juhe_business.account_proxy_leases
WHERE account_id = $1
FOR UPDATE`, accountID).Scan(
		&lease.AccountID, &lease.PoolID, &lease.ProxyProfileID, &lease.Generation, &lease.AssignmentReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("lock proxy lease: %w", err)
	}
	return lease, true, nil
}

func upsertLease(ctx context.Context, tx *sql.Tx, leaseKey, poolID, profileID, reason string) (Lease, error) {
	var lease Lease
	err := tx.QueryRowContext(ctx, `INSERT INTO juhe_business.account_proxy_leases
    (account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
VALUES ($1, $2, $3, nextval('juhe_business.account_proxy_lease_generation_seq'), $4, now(), now())
ON CONFLICT (account_id) DO UPDATE SET
    pool_id = EXCLUDED.pool_id,
    proxy_profile_id = EXCLUDED.proxy_profile_id,
    generation = nextval('juhe_business.account_proxy_lease_generation_seq'),
    assignment_reason = EXCLUDED.assignment_reason,
    assigned_at = CASE
        WHEN account_proxy_leases.proxy_profile_id IS DISTINCT FROM EXCLUDED.proxy_profile_id THEN now()
        ELSE account_proxy_leases.assigned_at
    END,
    updated_at = now()
RETURNING account_id, pool_id, proxy_profile_id, generation, assignment_reason`,
		leaseKey, poolID, profileID, reason).Scan(
		&lease.AccountID, &lease.PoolID, &lease.ProxyProfileID, &lease.Generation, &lease.AssignmentReason)
	if err != nil {
		return Lease{}, fmt.Errorf("upsert proxy lease: %w", err)
	}
	return lease, nil
}
