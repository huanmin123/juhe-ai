package proxyresolve

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Queryer is the subset of *sql.DB used by PostgresStore.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// PostgresStore is the PG-only batched read adapter for the proxy-pool tables.
// It intentionally does not write leases; the gateway supplies LeaseWriter.
type PostgresStore struct {
	db Queryer
}

func NewPostgresStore(db Queryer) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("proxyresolve PostgreSQL store requires a database")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) LoadBindings(ctx context.Context, accountIDs []string) (map[string]AccountBinding, error) {
	ids := uniqueNonEmpty(accountIDs)
	out := make(map[string]AccountBinding, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	query := `SELECT a.id,
       COALESCE(a.authorization_instance_source_account_id, ''),
       COALESCE(a.proxy_profile_id, ''),
       COALESCE(a.proxy_pool_id, ''),
       COALESCE(source_account.proxy_profile_id, ''),
       COALESCE(source_account.proxy_pool_id, '')
FROM juhe_business.accounts a
LEFT JOIN juhe_business.accounts source_account
  ON source_account.id = a.authorization_instance_source_account_id
 AND source_account.deleted_at IS NULL
WHERE a.id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load proxy bindings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var binding AccountBinding
		if err := rows.Scan(
			&binding.AccountID,
			&binding.SourceAccountID,
			&binding.ConfiguredProfileID,
			&binding.ConfiguredPoolID,
			&binding.SourceProfileID,
			&binding.SourcePoolID,
		); err != nil {
			return nil, fmt.Errorf("scan proxy binding: %w", err)
		}
		out[binding.AccountID] = binding
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy bindings: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) LoadProfiles(ctx context.Context, profileIDs []string) (map[string]Profile, error) {
	ids := uniqueNonEmpty(profileIDs)
	out := make(map[string]Profile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	query := `SELECT id, name, type, host, port,
       COALESCE(username, ''), COALESCE(password_encrypted, ''),
       enabled, COALESCE(pool_health_status, 'unknown'), pool_health_failure_streak
FROM juhe_business.proxy_profiles
WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load proxy profiles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var profile Profile
		var health string
		if err := rows.Scan(
			&profile.ID,
			&profile.Name,
			&profile.Type,
			&profile.Host,
			&profile.Port,
			&profile.Username,
			&profile.PasswordEncrypted,
			&profile.Enabled,
			&health,
			&profile.HealthFailureStreak,
		); err != nil {
			return nil, fmt.Errorf("scan proxy profile: %w", err)
		}
		profile.HealthStatus = HealthState(health)
		out[profile.ID] = profile
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy profiles: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) LoadPools(ctx context.Context, poolIDs []string) (map[string]Pool, error) {
	ids := uniqueNonEmpty(poolIDs)
	out := make(map[string]Pool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	query := `SELECT id, name, strategy, enabled
FROM juhe_business.proxy_pools
WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load proxy pools: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pool Pool
		if err := rows.Scan(&pool.ID, &pool.Name, &pool.Strategy, &pool.Enabled); err != nil {
			return nil, fmt.Errorf("scan proxy pool: %w", err)
		}
		out[pool.ID] = pool
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy pools: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) LoadPoolMembers(ctx context.Context, poolIDs []string) (map[string][]Member, error) {
	ids := uniqueNonEmpty(poolIDs)
	out := make(map[string][]Member, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	query := `SELECT pool_id, proxy_profile_id, priority, weight, enabled
FROM juhe_business.proxy_pool_members
WHERE pool_id IN (` + placeholders(len(ids)) + `)
ORDER BY pool_id, priority, proxy_profile_id`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load proxy pool members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.PoolID, &member.ProfileID, &member.Priority, &member.Weight, &member.Enabled); err != nil {
			return nil, fmt.Errorf("scan proxy pool member: %w", err)
		}
		out[member.PoolID] = append(out[member.PoolID], member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy pool members: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) LoadLeases(ctx context.Context, leaseKeys []string) (map[string]Lease, error) {
	keys := uniqueNonEmpty(leaseKeys)
	out := make(map[string]Lease, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	query := `SELECT account_id, pool_id, proxy_profile_id, generation, assignment_reason
FROM juhe_business.account_proxy_leases
WHERE account_id IN (` + placeholders(len(keys)) + `)`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(keys)...)
	if err != nil {
		return nil, fmt.Errorf("load proxy leases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lease Lease
		if err := rows.Scan(&lease.AccountID, &lease.PoolID, &lease.ProxyProfileID, &lease.Generation, &lease.AssignmentReason); err != nil {
			return nil, fmt.Errorf("scan proxy lease: %w", err)
		}
		out[lease.AccountID] = lease
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy leases: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) CountLeases(ctx context.Context, poolIDs []string) (map[string]map[string]int, error) {
	ids := uniqueNonEmpty(poolIDs)
	out := make(map[string]map[string]int, len(ids))
	for _, poolID := range ids {
		out[poolID] = map[string]int{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	query := `SELECT pool_id, proxy_profile_id, COUNT(*)
FROM juhe_business.account_proxy_leases
WHERE pool_id IN (` + placeholders(len(ids)) + `)
GROUP BY pool_id, proxy_profile_id`
	rows, err := s.db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("count proxy leases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var poolID, profileID string
		var count int
		if err := rows.Scan(&poolID, &profileID, &count); err != nil {
			return nil, fmt.Errorf("scan proxy lease count: %w", err)
		}
		out[poolID][profileID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy lease counts: %w", err)
	}
	return out, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	var builder strings.Builder
	for index := 1; index <= count; index++ {
		if index > 1 {
			builder.WriteString(", ")
		}
		builder.WriteString(fmt.Sprintf("$%d", index))
	}
	return builder.String()
}

func stringArgs(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}
