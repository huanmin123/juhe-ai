package proxypools

// Read half of the pool family (代理池设计 §8.1/§8.2): the paged admin list
// with member/assignable/lease counts, the pool detail with the ordered
// member projection, and the admin member picker options.

import (
	"context"
	"database/sql"
	"strings"
)

// assignablePredicate is the §6.2 assignable-member SQL approximation:
// member enabled && profile enabled && pool_health_status (NULL -> unknown)
// is not dead && failure streak below the dead threshold (3). Decryption and
// protocol legality can only be validated at resolve time.
const assignablePredicate = `m.enabled = true AND pr.enabled = true
				AND (pr.pool_health_status IS NULL OR pr.pool_health_status <> 'dead')
				AND pr.pool_health_failure_streak < 3`

// PoolSummary mirrors the §8.1 list item contract.
type PoolSummary struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Strategy    string  `json:"strategy"`
	Enabled     bool    `json:"enabled"`
	MemberCount int     `json:"memberCount"`
	// AssignableMemberCount is the §6.2 SQL approximation documented above.
	AssignableMemberCount int    `json:"assignableMemberCount"`
	LeaseCount            int    `json:"leaseCount"`
	CreatedAt             string `json:"createdAt"`
	UpdatedAt             string `json:"updatedAt"`
}

// ListResult mirrors the shared paged envelope.
type ListResult struct {
	Items    []PoolSummary `json:"items"`
	Total    int           `json:"total"`
	HasMore  bool          `json:"hasMore"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}

// MemberDetail mirrors the §8.1 detail member projection: the display fields
// only — never host/port/username/password.
type MemberDetail struct {
	ProxyProfileID          string  `json:"proxyProfileId"`
	Name                    string  `json:"name"`
	Type                    string  `json:"type"`
	Enabled                 bool    `json:"enabled"`
	TestStatus              string  `json:"testStatus"`
	LastTestedAt            *string `json:"lastTestedAt,omitempty"`
	LatencyMs               *int64  `json:"latencyMs,omitempty"`
	PoolHealthStatus        string  `json:"poolHealthStatus"`
	PoolHealthFailureStreak int     `json:"poolHealthFailureStreak"`
	PoolHealthCheckedAt     *string `json:"poolHealthCheckedAt,omitempty"`
	Priority                int     `json:"priority"`
	Weight                  int     `json:"weight"`
	MemberEnabled           bool    `json:"memberEnabled"`
	LeaseCount              int     `json:"leaseCount"`
	Assignable              bool    `json:"assignable"`
}

// PoolDetail mirrors the §8.1 detail contract: pool fields plus the ordered
// members.
type PoolDetail struct {
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	Description           *string        `json:"description,omitempty"`
	Strategy              string         `json:"strategy"`
	Enabled               bool           `json:"enabled"`
	MemberCount           int            `json:"memberCount"`
	AssignableMemberCount int            `json:"assignableMemberCount"`
	LeaseCount            int            `json:"leaseCount"`
	CreatedAt             string         `json:"createdAt"`
	UpdatedAt             string         `json:"updatedAt"`
	Members               []MemberDetail `json:"members"`
}

// poolSummaryCountColumns joins the per-pool counters onto the summary rows
// (assignable via the §6.2 predicate documented on assignablePredicate).
const poolSummaryCountColumns = `
		(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m WHERE m.pool_id = p.id) AS member_count,
		(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m
			JOIN juhe_business.proxy_profiles pr ON pr.id = m.proxy_profile_id
			WHERE m.pool_id = p.id AND ` + assignablePredicate + `) AS assignable_member_count,
		(SELECT COUNT(*) FROM juhe_business.account_proxy_leases l WHERE l.pool_id = p.id) AS lease_count`

// ListPage mirrors the §8.1 list: pageSize 1..200 (default 20), page bounded
// by the 1001-row window, keyword prefix filter on the pool name only,
// updated_at DESC.
func (s *Store) ListPage(ctx context.Context, page, pageSize int, keyword string) (ListResult, error) {
	pageSize = clampInt(pageSize, 1, 200)
	maxPage := (1001 - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	keyword = strings.TrimSpace(keyword)
	clauses := ""
	params := []any{}
	if keyword != "" {
		clauses = " WHERE " + s.keywordFilter()
		params = s.keywordFilterParams(keyword)
	}
	query := `
		SELECT p.id, p.name, p.description, p.strategy, p.enabled,
			to_char(p.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS created_at,
			` + s.revisionSelectExpression() + `,` + poolSummaryCountColumns + `
		FROM juhe_business.proxy_pools p` + clauses + `
		ORDER BY p.updated_at DESC, p.id DESC
		LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(params, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	pools := []PoolSummary{}
	for rows.Next() {
		pool, scanErr := scanPoolSummary(rows)
		if scanErr != nil {
			return ListResult{}, scanErr
		}
		pools = append(pools, pool)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	hasMore := false
	if len(pools) > pageSize {
		pools = pools[:pageSize]
		hasMore = true
	}
	total := (page-1)*pageSize + len(pools)
	if hasMore {
		total++
	}
	return ListResult{
		Items:    pools,
		Total:    total,
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func scanPoolSummary(rows rowScanner) (PoolSummary, error) {
	var (
		pool        PoolSummary
		description sql.NullString
		createdAt   string
		updatedAt   string
		enabled     any
	)
	if err := rows.Scan(&pool.ID, &pool.Name, &description, &pool.Strategy, &enabled,
		&createdAt, &updatedAt, &pool.MemberCount, &pool.AssignableMemberCount, &pool.LeaseCount); err != nil {
		return PoolSummary{}, err
	}
	pool.Description = nullStringPtr(description)
	pool.Enabled = boolFromValue(enabled)
	pool.CreatedAt = createdAt
	pool.UpdatedAt = normalizeRevision(updatedAt)
	return pool, nil
}

// GetDetail loads the pool fields plus the priority-ordered members. nil
// result = pool missing.
func (s *Store) GetDetail(ctx context.Context, id string) (*PoolDetail, error) {
	pool, err := s.loadPoolSummary(ctx, s.db, id, "")
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, nil
	}
	members, counts, err := s.loadMembers(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	pool.Members = members
	pool.MemberCount = counts.memberCount
	pool.AssignableMemberCount = counts.assignableMemberCount
	pool.LeaseCount = counts.leaseCount
	return pool, nil
}

// loadPoolSummary is the row-level load shared by the read path (db) and the
// delete critical section (tx, with the FOR UPDATE lock clause).
func (s *Store) loadPoolSummary(ctx context.Context, querier rowQuerier, id string, lockClause string) (*PoolDetail, error) {
	var (
		pool        PoolDetail
		description sql.NullString
		createdAt   string
		updatedAt   string
		enabled     any
	)
	query := `
		SELECT p.id, p.name, p.description, p.strategy, p.enabled,
			to_char(p.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS created_at,
			` + s.revisionSelectExpression() + `,` + poolSummaryCountColumns + `
		FROM juhe_business.proxy_pools p
		WHERE p.id = ?` + lockClause
	err := querier.QueryRowContext(ctx, s.bind(query), id).Scan(
		&pool.ID, &pool.Name, &description, &pool.Strategy, &enabled,
		&createdAt, &updatedAt, &pool.MemberCount, &pool.AssignableMemberCount, &pool.LeaseCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pool.Description = nullStringPtr(description)
	pool.Enabled = boolFromValue(enabled)
	pool.CreatedAt = createdAt
	pool.UpdatedAt = normalizeRevision(updatedAt)
	pool.Members = []MemberDetail{}
	return &pool, nil
}

type memberCounts struct {
	memberCount           int
	assignableMemberCount int
	leaseCount            int
}

// loadMembers returns the priority-ordered member projection plus the pool
// counters (§8.1 detail fields; assignable reuses the §6.2 predicate).
func (s *Store) loadMembers(ctx context.Context, querier rowQuerier, poolID string) ([]MemberDetail, memberCounts, error) {
	memberQuery := `
		SELECT m.proxy_profile_id, pr.name, pr.type, pr.enabled, pr.test_status,
			to_char(pr.last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS last_tested_at,
			pr.latency_ms,
			pr.pool_health_status, pr.pool_health_failure_streak,
			to_char(pr.pool_health_checked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS pool_health_checked_at,
			m.priority, m.weight, m.enabled,
			(SELECT COUNT(*) FROM juhe_business.account_proxy_leases l
				WHERE l.pool_id = m.pool_id AND l.proxy_profile_id = m.proxy_profile_id) AS lease_count
		FROM juhe_business.proxy_pool_members m
		JOIN juhe_business.proxy_profiles pr ON pr.id = m.proxy_profile_id
		WHERE m.pool_id = ?
		ORDER BY m.priority ASC`
	rows, err := querier.QueryContext(ctx, s.bind(memberQuery), poolID)
	if err != nil {
		return nil, memberCounts{}, err
	}
	members := []MemberDetail{}
	assignable := 0
	for rows.Next() {
		member, scanErr := scanMemberDetail(rows)
		if scanErr != nil {
			rows.Close()
			return nil, memberCounts{}, scanErr
		}
		if member.Assignable {
			assignable++
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, memberCounts{}, err
	}
	rows.Close()
	leaseQuery := `
		SELECT
			(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m WHERE m.pool_id = ?) AS member_count,
			(SELECT COUNT(*) FROM juhe_business.proxy_pool_members m
				JOIN juhe_business.proxy_profiles pr ON pr.id = m.proxy_profile_id
				WHERE m.pool_id = ? AND ` + assignablePredicate + `) AS assignable_member_count,
			(SELECT COUNT(*) FROM juhe_business.account_proxy_leases l WHERE l.pool_id = ?) AS lease_count`
	var counts memberCounts
	if err := querier.QueryRowContext(ctx, s.bind(leaseQuery), poolID, poolID, poolID).
		Scan(&counts.memberCount, &counts.assignableMemberCount, &counts.leaseCount); err != nil {
		return nil, memberCounts{}, err
	}
	return members, counts, nil
}

func scanMemberDetail(rows rowScanner) (MemberDetail, error) {
	var (
		member               MemberDetail
		lastTestedAt         sql.NullString
		latencyMs            sql.NullInt64
		poolHealthStatus     sql.NullString
		poolHealthCheckedAt  sql.NullString
		poolHealthFailureAny any
		profileEnabled       any
		memberEnabled        any
		priorityAny          any
		weightAny            any
		lastTestedPtr        *string
		latencyPtr           *int64
		poolHealthCheckedPtr *string
	)
	if err := rows.Scan(&member.ProxyProfileID, &member.Name, &member.Type, &profileEnabled, &member.TestStatus,
		&lastTestedAt, &latencyMs,
		&poolHealthStatus, &poolHealthFailureAny, &poolHealthCheckedAt,
		&priorityAny, &weightAny, &memberEnabled,
		&member.LeaseCount); err != nil {
		return MemberDetail{}, err
	}
	member.Enabled = boolFromValue(profileEnabled)
	member.MemberEnabled = boolFromValue(memberEnabled)
	if lastTestedAt.Valid {
		value := lastTestedAt.String
		lastTestedPtr = &value
	}
	member.LastTestedAt = lastTestedPtr
	if latencyMs.Valid {
		value := latencyMs.Int64
		latencyPtr = &value
	}
	member.LatencyMs = latencyPtr
	// NULL 按 unknown 处理（§6.2），展示层不再判空。
	member.PoolHealthStatus = "unknown"
	if poolHealthStatus.Valid && poolHealthStatus.String != "" {
		member.PoolHealthStatus = poolHealthStatus.String
	}
	member.PoolHealthFailureStreak = intFromValue(poolHealthFailureAny)
	if poolHealthCheckedAt.Valid {
		value := poolHealthCheckedAt.String
		poolHealthCheckedPtr = &value
	}
	member.PoolHealthCheckedAt = poolHealthCheckedPtr
	member.Priority = intFromValue(priorityAny)
	member.Weight = intFromValue(weightAny)
	// §7.1 可用性公式：成员行/代理启用 + 健康不是 dead + streak 未达 3。
	member.Assignable = member.MemberEnabled && member.Enabled &&
		member.PoolHealthStatus != "dead" && member.PoolHealthFailureStreak < 3
	return member, nil
}

// MemberOptionSummary mirrors the §8.1 member-options contract:
// id/name/type/enabled/testStatus only — never host/port/username/password.
type MemberOptionSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Enabled    bool   `json:"enabled"`
	TestStatus string `json:"testStatus"`
}

// ListMemberOptions mirrors the member picker: the keyword window over ALL
// profiles (the picker displays the enabled flag, so disabled profiles stay
// visible) plus the explicitly selected ids, merged name-ordered. limit
// clamps to 1..50 (default 50) like the account-form options.
func (s *Store) ListMemberOptions(ctx context.Context, keyword string, limit int, selectedIds []string) ([]MemberOptionSummary, error) {
	keyword = strings.TrimSpace(keyword)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	keywordClause := ""
	params := []any{}
	if keyword != "" {
		keywordClause = " AND " + s.keywordFilter()
		params = s.keywordFilterParams(keyword)
	}
	windowQuery := `
		SELECT id, name, type, enabled, test_status,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at
		FROM ` + s.table("proxy_profiles") + `
		WHERE true` + keywordClause + `
		ORDER BY name ASC, updated_at DESC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(windowQuery), append(params, limit)...)
	if err != nil {
		return nil, err
	}
	windowRows, err := scanMemberOptionRows(rows)
	if err != nil {
		return nil, err
	}
	selectedRows := []memberOptionRow{}
	if len(selectedIds) > 0 {
		selectedQuery := `
			SELECT id, name, type, enabled, test_status,
				to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at
			FROM ` + s.table("proxy_profiles") + `
			WHERE id IN (` + placeholders(len(selectedIds)) + `)
			ORDER BY name ASC, updated_at DESC, id ASC`
		selectedQueryRows, err := s.db.QueryContext(ctx, s.bind(selectedQuery), idsToAny(selectedIds)...)
		if err != nil {
			return nil, err
		}
		selectedRows, err = scanMemberOptionRows(selectedQueryRows)
		if err != nil {
			return nil, err
		}
	}
	return mergeMemberOptionRows(windowRows, selectedRows), nil
}

type memberOptionRow struct {
	id         string
	name       string
	typeCode   string
	enabled    bool
	testStatus string
	updatedAt  string
}

func scanMemberOptionRows(rows rowScanner) ([]memberOptionRow, error) {
	defer rows.Close()
	out := []memberOptionRow{}
	for rows.Next() {
		var (
			id, name, typeCode string
			enabled            any
			updatedAt          string
		)
		row := memberOptionRow{testStatus: "unknown"}
		if err := rows.Scan(&id, &name, &typeCode, &enabled, &row.testStatus, &updatedAt); err != nil {
			return nil, err
		}
		row.id, row.name, row.typeCode, row.enabled, row.updatedAt = id, name, typeCode, boolFromValue(enabled), updatedAt
		out = append(out, row)
	}
	return out, rows.Err()
}

// mergeMemberOptionRows dedupes by id and sorts name ASC, updated_at DESC,
// id ASC (same ordering rule as the account-form options).
func mergeMemberOptionRows(windowRows, selectedRows []memberOptionRow) []MemberOptionSummary {
	byID := map[string]memberOptionRow{}
	order := []string{}
	for _, row := range append(append([]memberOptionRow{}, windowRows...), selectedRows...) {
		if _, ok := byID[row.id]; !ok {
			order = append(order, row.id)
		}
		byID[row.id] = row
	}
	merged := make([]memberOptionRow, 0, len(order))
	for _, id := range order {
		merged = append(merged, byID[id])
	}
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0; j-- {
			left, right := merged[j-1], merged[j]
			if left.name != right.name {
				if left.name > right.name {
					merged[j-1], merged[j] = merged[j], merged[j-1]
				}
				continue
			}
			if left.updatedAt != right.updatedAt {
				if left.updatedAt < right.updatedAt {
					merged[j-1], merged[j] = merged[j], merged[j-1]
				}
				continue
			}
			if left.id > right.id {
				merged[j-1], merged[j] = merged[j], merged[j-1]
			}
		}
	}
	options := make([]MemberOptionSummary, 0, len(merged))
	for _, row := range merged {
		options = append(options, MemberOptionSummary{
			ID: row.id, Name: row.name, Type: row.typeCode, Enabled: row.enabled, TestStatus: row.testStatus,
		})
	}
	return options
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
