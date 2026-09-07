// Per-authorization usage detail (BUG-0165 slice): the
// getResourceAuthorizationUsageAsync read chain
// (resource-authorization-usage.repository.ts) — the scope-filtered grant
// summary plus the range usage, the per-member usageBySystemAccount page and
// the authorization_team_usage_range_windows / usage_scope_range_windows
// lookups with the account instance fallback.
package authz

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// UsageSummary mirrors AccountUsageSummary (domain/types.ts:487-502).
type UsageSummary struct {
	RequestCount      float64 `json:"requestCount"`
	InputTokens       float64 `json:"inputTokens"`
	OutputTokens      float64 `json:"outputTokens"`
	CacheReadTokens   float64 `json:"cacheReadTokens"`
	CacheReadCost     float64 `json:"cacheReadCost"`
	CacheWriteTokens  float64 `json:"cacheWriteTokens"`
	CacheWrite1hTokens float64 `json:"cacheWrite1hTokens"`
	CacheWriteCost    float64 `json:"cacheWriteCost"`
	ThinkingTokens    float64 `json:"thinkingTokens"`
	InputImageTokens  float64 `json:"inputImageTokens"`
	OutputImageTokens float64 `json:"outputImageTokens"`
	TotalTokens       float64 `json:"totalTokens"`
	TotalCost         float64 `json:"totalCost"`
	LastUsedAt        *string `json:"lastUsedAt,omitempty"`
}

// LastUsedAtString exposes the sorting key of the embedded summary.
func (u UsageSummary) LastUsedAtString() string {
	if u.LastUsedAt != nil {
		return *u.LastUsedAt
	}
	return ""
}

// fullUsageSummary mirrors usageSummaryFromAggregate for the full projection.
func fullUsageSummary(row *usageWindowRow) UsageSummary {
	if row == nil {
		return UsageSummary{}
	}
	summary := UsageSummary{
		RequestCount:      row.RequestCount,
		InputTokens:       row.InputTokens,
		OutputTokens:      row.OutputTokens,
		CacheReadTokens:   row.CacheReadTokens,
		CacheReadCost:     row.CacheReadCostUsd,
		CacheWriteTokens:  row.CacheWriteTokens,
		CacheWrite1hTokens: row.CacheWrite1hTokens,
		CacheWriteCost:    row.CacheWriteCostUsd,
		ThinkingTokens:    row.ThinkingTokens,
		InputImageTokens:  row.InputImageTokens,
		OutputImageTokens: row.OutputImageTokens,
		TotalTokens:       row.InputTokens + row.OutputTokens,
		TotalCost:         row.TotalCostUsd,
	}
	if row.LastUsedAt.Valid && row.LastUsedAt.String != "" {
		value := row.LastUsedAt.String
		summary.LastUsedAt = &value
	}
	return summary
}

// addUsageSummaries mirrors usage-stats-helpers.ts addUsageSummaries (the
// latest lastUsedAt wins).
func addUsageSummaries(left, right *UsageSummary) UsageSummary {
	total := UsageSummary{}
	if left != nil {
		total = *left
	}
	if right == nil {
		return total
	}
	total.RequestCount += right.RequestCount
	total.InputTokens += right.InputTokens
	total.OutputTokens += right.OutputTokens
	total.CacheReadTokens += right.CacheReadTokens
	total.CacheReadCost += right.CacheReadCost
	total.CacheWriteTokens += right.CacheWriteTokens
	total.CacheWrite1hTokens += right.CacheWrite1hTokens
	total.CacheWriteCost += right.CacheWriteCost
	total.ThinkingTokens += right.ThinkingTokens
	total.InputImageTokens += right.InputImageTokens
	total.OutputImageTokens += right.OutputImageTokens
	total.TotalTokens += right.TotalTokens
	total.TotalCost += right.TotalCost
	if right.LastUsedAt != nil && (total.LastUsedAt == nil ||
		usageTimestampMilliseconds(*right.LastUsedAt) > usageTimestampMilliseconds(*total.LastUsedAt)) {
		value := *right.LastUsedAt
		total.LastUsedAt = &value
	}
	return total
}

// usageTimestampMilliseconds is the sorting key for lastUsedAt; unparsable
// values sort as 0 like the Node resourceAuthorizationUsageTimestamp guard
// (which throws — the read contract treats stored values as instants).
func usageTimestampMilliseconds(value string) int64 {
	if milliseconds, ok := instantMilliseconds(value); ok {
		return milliseconds
	}
	return 0
}

// UsageDetailRow mirrors ResourceAuthorizationUsageDetail
// (domain/types.ts:1780-1800): the flat usage summary fields plus the member
// identity and the nested rangeUsage echo.
type UsageDetailRow struct {
	SystemAccountID   string  `json:"systemAccountId"`
	SystemAccountName *string `json:"systemAccountName,omitempty"`
	Username          *string `json:"username,omitempty"`
	UsageSummary
	RangeUsage *UsageSummary `json:"rangeUsage"`
}

// UsageDetail mirrors the per-authorization usage response: the grant summary
// with the usage fields attached
// (getResourceAuthorizationUsageReadOnly :64-80).
type UsageDetail struct {
	Summary
	Usage                        UsageSummary     `json:"usage"`
	LastUsedAt                   string           `json:"lastUsedAt,omitempty"`
	UsageBySystemAccount         []UsageDetailRow `json:"usageBySystemAccount"`
	UsageBySystemAccountTotal    int              `json:"usageBySystemAccountTotal"`
	UsageBySystemAccountPage     int              `json:"usageBySystemAccountPage"`
	UsageBySystemAccountPageSize int              `json:"usageBySystemAccountPageSize"`
	UsageBySystemAccountHasMore  bool             `json:"usageBySystemAccountHasMore"`
	UsageRange                   UsageStatsRange  `json:"usageRange"`
}

// runtimeUsageRow is the runtime resource_authorizations projection the usage
// detail reads (resourceAuthorizationSelectColumns subset).
type runtimeUsageRow struct {
	ID           string
	ResourceType string
	ResourceID   string
	OwnerID      string
	GranteeID    sql.NullString
	CreatedAt    string
}

const runtimeUsageColumns = `ra.id, ra.resource_type, ra.resource_id,
	ra.resource_owner_system_account_id, ra.grantee_system_account_id, ra.created_at`

func scanRuntimeUsageRow(scanner interface{ Scan(...any) error }) (runtimeUsageRow, error) {
	var row runtimeUsageRow
	err := scanner.Scan(&row.ID, &row.ResourceType, &row.ResourceID,
		&row.OwnerID, &row.GranteeID, &row.CreatedAt)
	return row, err
}

// usageScopeRequest mirrors UsageSummaryScopeRequest.
type usageScopeRequest struct {
	rowKey         string
	systemAccountID string
	scopeID        string
}

// usageStatsSystemAccountID mirrors authorizationUsageStatsSystemAccountId
// (:555-563): account usage is attributed to the grantee, group usage to the
// owner.
func usageStatsSystemAccountID(resourceType, ownerID, granteeID string) string {
	if resourceType == "account" && granteeID != "" {
		return granteeID
	}
	return ownerID
}

// usageScopeRangeSummaries mirrors loadUsageRangeSummariesForScopeRequests
// (usage-summary-loaders.ts:203-268): one chunked read over
// usage_scope_range_windows keyed by scope_id.
func (s *Store) usageScopeRangeSummaries(ctx context.Context, requests []usageScopeRequest, scopeType string, rng UsageStatsRange) (map[string]UsageSummary, error) {
	result := map[string]UsageSummary{}
	bySystemAccount := map[string][]usageScopeRequest{}
	for _, request := range requests {
		if request.rowKey == "" || request.systemAccountID == "" || request.scopeID == "" {
			continue
		}
		bySystemAccount[request.systemAccountID] = append(bySystemAccount[request.systemAccountID], request)
	}
	for systemAccountID, systemRequests := range bySystemAccount {
		scopeIDs := uniqueStrings(mapScopeIDs(systemRequests))
		for start := 0; start < len(scopeIDs); start += 400 {
			end := start + 400
			if end > len(scopeIDs) {
				end = len(scopeIDs)
			}
			chunk := scopeIDs[start:end]
			placeholders := strings.TrimRight(strings.Repeat("?, ", len(chunk)), ", ")
			query := `SELECT scope_id, request_count, input_tokens, output_tokens,
				cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
				cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
				total_cost_usd, last_used_at
				FROM ` + s.statsTable("usage_scope_range_windows") + `
				WHERE system_account_id = ? AND scope_type = ?
					AND start_date = ? AND end_date = ?
					AND scope_id IN (` + placeholders + `)`
			args := []any{systemAccountID, scopeType, rng.StartDate, rng.EndDate}
			args = append(args, toStrings(chunk)...)
			rows, err := s.statsQueryDB().QueryContext(ctx, s.bind(query), args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var scopeID string
				var aggregate usageWindowRow
				if err := rows.Scan(&scopeID, &aggregate.RequestCount, &aggregate.InputTokens, &aggregate.OutputTokens,
					&aggregate.CacheReadTokens, &aggregate.CacheReadCostUsd, &aggregate.CacheWriteTokens, &aggregate.CacheWrite1hTokens,
					&aggregate.CacheWriteCostUsd, &aggregate.ThinkingTokens, &aggregate.InputImageTokens, &aggregate.OutputImageTokens,
					&aggregate.TotalCostUsd, &aggregate.LastUsedAt); err != nil {
					rows.Close()
					return nil, err
				}
				summary := fullUsageSummary(&aggregate)
				for _, request := range systemRequests {
					if request.scopeID == scopeID {
						result[request.rowKey] = summary
					}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	}
	return result, nil
}

func mapScopeIDs(requests []usageScopeRequest) []string {
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.scopeID)
	}
	return ids
}

// loadUsageRangeSummaryForScope mirrors loadUsageRangeSummaryForScope
// (usage-summary-loaders.ts:334-345).
func (s *Store) loadUsageRangeSummaryForScope(ctx context.Context, systemAccountID, scopeType, scopeID string, rng UsageStatsRange) (*UsageSummary, error) {
	summaries, err := s.usageScopeRangeSummaries(ctx, []usageScopeRequest{{rowKey: scopeID, systemAccountID: systemAccountID, scopeID: scopeID}}, scopeType, rng)
	if err != nil {
		return nil, err
	}
	if summary, ok := summaries[scopeID]; ok {
		return &summary, nil
	}
	return &UsageSummary{}, nil
}

// findRuntimeUsageRow mirrors the direct-grant runtime lookup
// (resource-authorization-usage.repository.ts:132-139).
func (s *Store) findRuntimeUsageRow(ctx context.Context, resourceType, resourceID, granteeID string) (*runtimeUsageRow, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT `+runtimeUsageColumns+`
		FROM `+s.table("resource_authorizations")+` ra
		WHERE ra.resource_type = ? AND ra.resource_id = ? AND ra.grantee_system_account_id = ?
		LIMIT 1`), resourceType, resourceID, granteeID)
	runtime, err := scanRuntimeUsageRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &runtime, nil
}

// loadTeamRuntimeUsageRows mirrors the team runtime queries (:258-277 paged,
// :396-414 unpaginated fallback): DISTINCT runtime rows joined to the team
// source, ordered by created_at/id. A non-positive limit keeps the SQL
// unbounded like the Node fallback query.
func (s *Store) loadTeamRuntimeUsageRows(ctx context.Context, resourceType, resourceID, ownerID, teamID string, limit, offset int) ([]runtimeUsageRow, error) {
	query := `SELECT DISTINCT ` + runtimeUsageColumns + `
		FROM ` + s.table("resource_authorizations") + ` ra
		INNER JOIN ` + s.table("resource_authorization_sources") + ` ras
			ON ras.authorization_id = ra.id
			AND ras.source_type = 'team'
			AND ras.source_team_id = ?
		WHERE ra.resource_type = ?
			AND ra.resource_id = ?
			AND ra.resource_owner_system_account_id = ?
		ORDER BY ra.created_at ASC, ra.id ASC`
	args := []any{teamID, resourceType, resourceID, ownerID}
	if limit > 0 {
		query += `
		LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []runtimeUsageRow
	for rows.Next() {
		row, scanErr := scanRuntimeUsageRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// loadTeamUsageWindowSummary mirrors loadAuthorizationTeamUsageRangeSummary
// (:565-591): the pre-aggregated team window row for this grant.
func (s *Store) loadTeamUsageWindowSummary(ctx context.Context, resourceType, resourceID, ownerID, teamID string, rng UsageStatsRange) (*UsageSummary, error) {
	query := `SELECT team_filter_id, '' , '', ` + windowAggregateColumns + `
		FROM ` + s.statsTable("authorization_team_usage_range_windows") + `
		WHERE system_account_id = ?
			AND start_date = ?
			AND end_date = ?
			AND team_filter_id = ?
			AND resource_filter_type = ?
			AND resource_filter_id = ?
		LIMIT 1`
	row := s.statsQueryDB().QueryRowContext(ctx, s.bind(query), ownerID,
		rng.StartDate, rng.EndDate, teamID, resourceType, resourceID)
	var aggregate usageWindowRow
	var teamIDValue, resourceTypeValue, resourceIDValue string
	err := row.Scan(&teamIDValue, &resourceTypeValue, &resourceIDValue,
		&aggregate.RequestCount, &aggregate.InputTokens, &aggregate.OutputTokens,
		&aggregate.CacheReadTokens, &aggregate.CacheReadCostUsd, &aggregate.CacheWriteTokens, &aggregate.CacheWrite1hTokens,
		&aggregate.CacheWriteCostUsd, &aggregate.ThinkingTokens, &aggregate.InputImageTokens, &aggregate.OutputImageTokens,
		&aggregate.TotalCostUsd, &aggregate.LastUsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summary := fullUsageSummary(&aggregate)
	return &summary, nil
}

// loadAccountInstanceAccountIDs mirrors loadAccountAuthorizationInstanceAccountIds
// (:436-455): authorization id → cloned instance account id.
func (s *Store) loadAccountInstanceAccountIDs(ctx context.Context, authorizationIDs []string) (map[string]string, error) {
	result := map[string]string{}
	unique := uniqueStrings(authorizationIDs)
	if len(unique) == 0 {
		return result, nil
	}
	for start := 0; start < len(unique); start += 400 {
		end := start + 400
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?, ", len(chunk)), ", ")
		query := `SELECT authorization_instance_authorization_id, id FROM ` + s.table("accounts") + `
			WHERE authorization_instance_authorization_id IN (` + placeholders + `) AND deleted_at IS NULL`
		rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var authorizationID, accountID string
			if err := rows.Scan(&authorizationID, &accountID); err != nil {
				rows.Close()
				return nil, err
			}
			if authorizationID != "" && accountID != "" {
				result[authorizationID] = accountID
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// findScopedUsageGrant resolves the grant with the list/find access scope
// (listResourceAuthorizationGrantOperationRows :265-284): the administrator
// filter narrows the owner, everyone else sees owner/grantee/active-team
// membership; out-of-scope grants read as not found.
func (s *Store) findScopedUsageGrant(ctx context.Context, id string, access accessInfo) (*grantRow, error) {
	grant, err := s.GetGrantForMutation(ctx, nil, id)
	if err != nil || grant == nil {
		return nil, err
	}
	if access.IsAdmin {
		if access.FilterID != "" && grant.OwnerID != access.FilterID {
			return nil, nil
		}
		return grant, nil
	}
	viewer := access.ViewerID
	if viewer == "" {
		return nil, nil
	}
	if grant.OwnerID == viewer {
		return grant, nil
	}
	if grant.GranteeUserID.Valid && grant.GranteeUserID.String == viewer {
		return grant, nil
	}
	if grant.GranteeTeamID.Valid {
		var exists int
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? AND status = 'active' LIMIT 1`),
			grant.GranteeTeamID.String, viewer).Scan(&exists); err == nil {
			return grant, nil
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return nil, nil
}

// usageDetailSummary mirrors getResourceAuthorizationUsageReadOnly
// (:64-80) with loadResourceAuthorizationUsageDetail (:112-174 direct,
// :241-297 team). A nil result is the Node undefined → 404.
func (s *Store) usageDetailSummary(ctx context.Context, id string, access accessInfo, rng UsageStatsRange, page, pageSize int) (*UsageDetail, error) {
	grant, err := s.findScopedUsageGrant(ctx, id, access)
	if err != nil || grant == nil {
		return nil, err
	}
	page, pageSize = normalizeResourceAuthorizationUsagePageOptions(page, pageSize)
	summary := grant.summary()
	summary.Limits = decodeAuthorizationLimits(grantLimitsText(grant.LimitsJSON))
	detail := &UsageDetail{
		Summary:                      summary,
		Usage:                        UsageSummary{},
		UsageBySystemAccount:         []UsageDetailRow{},
		UsageBySystemAccountPage:     page,
		UsageBySystemAccountPageSize: pageSize,
		UsageRange:                   rng,
	}
	var usage UsageSummary
	if grant.GranteeType == "team" {
		usage, err = s.loadTeamUsageDetail(ctx, grant, detail, rng, page, pageSize)
	} else {
		usage, err = s.loadDirectUsageDetail(ctx, grant, detail, rng, page)
	}
	if err != nil {
		return nil, err
	}
	detail.Usage = usage
	if usage.LastUsedAt != nil {
		detail.LastUsedAt = *usage.LastUsedAt
	}
	return detail, nil
}

// loadDirectUsageDetail mirrors loadResourceAuthorizationUsageDetail for the
// direct grant (:128-174): one member row, empty when the runtime row is
// gone.
func (s *Store) loadDirectUsageDetail(ctx context.Context, grant *grantRow, detail *UsageDetail, rng UsageStatsRange, page int) (UsageSummary, error) {
	granteeID := ""
	if grant.GranteeUserID.Valid {
		granteeID = grant.GranteeUserID.String
	}
	if granteeID == "" {
		return UsageSummary{}, nil
	}
	runtime, err := s.findRuntimeUsageRow(ctx, grant.ResourceType, grant.ResourceID, granteeID)
	if err != nil {
		return UsageSummary{}, err
	}
	if runtime == nil {
		return UsageSummary{}, nil
	}
	scopeType := "group_authorization"
	if grant.ResourceType == "account" {
		scopeType = "account_authorization"
	}
	usage, err := s.loadUsageRangeSummaryForScope(ctx,
		usageStatsSystemAccountID(grant.ResourceType, grant.OwnerID, granteeID), scopeType, runtime.ID, rng)
	if err != nil {
		return UsageSummary{}, err
	}
	principals, err := s.loadPrincipalMap(ctx, []string{granteeID})
	if err != nil {
		return UsageSummary{}, err
	}
	row := UsageDetailRow{
		SystemAccountID: granteeID,
		UsageSummary:    *usage,
		RangeUsage:      usage,
	}
	if principal, ok := principals[granteeID]; ok {
		name := principal.displayName
		username := principal.username
		row.SystemAccountName = &name
		row.Username = &username
	}
	if page == 1 {
		detail.UsageBySystemAccount = []UsageDetailRow{row}
	}
	detail.UsageBySystemAccountTotal = 1
	return *usage, nil
}

// loadTeamUsageDetail mirrors loadResourceAuthorizationGrantUsageDetailForTeam
// (:241-297): the member runtime page with per-member usage plus the team
// window summary (with the group/instance fallback).
func (s *Store) loadTeamUsageDetail(ctx context.Context, grant *grantRow, detail *UsageDetail, rng UsageStatsRange, page, pageSize int) (UsageSummary, error) {
	teamID := ""
	if grant.GranteeTeamID.Valid {
		teamID = grant.GranteeTeamID.String
	}
	if teamID == "" {
		return UsageSummary{}, nil
	}
	rows, err := s.loadTeamRuntimeUsageRows(ctx, grant.ResourceType, grant.ResourceID, grant.OwnerID, teamID, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return UsageSummary{}, err
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	memberRows := make([]UsageDetailRow, 0, len(rows))
	granteeIDs := make([]string, 0, len(rows))
	for _, runtime := range rows {
		if !runtime.GranteeID.Valid || runtime.GranteeID.String == "" {
			continue
		}
		granteeIDs = append(granteeIDs, runtime.GranteeID.String)
		scopeType := "group_authorization"
		if runtime.ResourceType == "account" {
			scopeType = "account_authorization"
		}
		usage, err := s.loadUsageRangeSummaryForScope(ctx,
			usageStatsSystemAccountID(runtime.ResourceType, runtime.OwnerID, runtime.GranteeID.String),
			scopeType, runtime.ID, rng)
		if err != nil {
			return UsageSummary{}, err
		}
		memberRows = append(memberRows, UsageDetailRow{
			SystemAccountID: runtime.GranteeID.String,
			UsageSummary:    *usage,
			RangeUsage:      usage,
		})
	}
	principals, err := s.loadPrincipalMap(ctx, granteeIDs)
	if err != nil {
		return UsageSummary{}, err
	}
	for index := range memberRows {
		if principal, ok := principals[memberRows[index].SystemAccountID]; ok {
			name := principal.displayName
			username := principal.username
			memberRows[index].SystemAccountName = &name
			memberRows[index].Username = &username
		}
	}
	sort.SliceStable(memberRows, func(left, right int) bool {
		leftTime := usageTimestampMilliseconds(memberRows[left].LastUsedAtString())
		rightTime := usageTimestampMilliseconds(memberRows[right].LastUsedAtString())
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return memberRows[left].SystemAccountID < memberRows[right].SystemAccountID
	})
	detail.UsageBySystemAccount = memberRows
	detail.UsageBySystemAccountTotal = pagedTotalUpperBound(page, pageSize, len(memberRows), hasMore)
	detail.UsageBySystemAccountHasMore = hasMore

	// Team usage: the pre-aggregated window row first, then the fallback
	// (loadAuthorizationTeamUsageRangeSummary ?? loadAuthorizationTeamUsageFallbackSummary,
	// :280-281/:338-339).
	usage, err := s.loadTeamUsageWindowSummary(ctx, grant.ResourceType, grant.ResourceID, grant.OwnerID, teamID, rng)
	if err != nil {
		return UsageSummary{}, err
	}
	if usage != nil {
		return *usage, nil
	}
	return s.loadTeamUsageFallbackSummary(ctx, grant, teamID, rng)
}

// loadTeamUsageFallbackSummary mirrors loadAuthorizationTeamUsageFallbackSummary
// (:357-394): group resources read the group_authorization_team scope window;
// account resources sum the per-member account_authorization_team windows
// resolved through the cloned instance accounts.
func (s *Store) loadTeamUsageFallbackSummary(ctx context.Context, grant *grantRow, teamID string, rng UsageStatsRange) (UsageSummary, error) {
	if grant.ResourceType == "group" {
		usage, err := s.loadUsageRangeSummaryForScope(ctx, grant.OwnerID, "group_authorization_team", grant.ResourceID+":"+teamID, rng)
		if err != nil {
			return UsageSummary{}, err
		}
		return *usage, nil
	}
	rows, err := s.loadTeamRuntimeUsageRows(ctx, grant.ResourceType, grant.ResourceID, grant.OwnerID, teamID, -1, 0)
	if err != nil {
		return UsageSummary{}, err
	}
	instanceIDs, err := s.loadAccountInstanceAccountIDs(ctx, runtimeIDs(rows))
	if err != nil {
		return UsageSummary{}, err
	}
	var requests []usageScopeRequest
	for _, runtime := range rows {
		if !runtime.GranteeID.Valid || runtime.GranteeID.String == "" {
			continue
		}
		instanceID, ok := instanceIDs[runtime.ID]
		if !ok {
			continue
		}
		requests = append(requests, usageScopeRequest{
			rowKey:          runtime.ID,
			systemAccountID: runtime.GranteeID.String,
			scopeID:         instanceID + ":" + teamID,
		})
	}
	summaries, err := s.usageScopeRangeSummaries(ctx, requests, "account_authorization_team", rng)
	if err != nil {
		return UsageSummary{}, err
	}
	var total *UsageSummary
	for _, request := range requests {
		summary, ok := summaries[request.rowKey]
		if !ok {
			zero := UsageSummary{}
			merged := addUsageSummaries(total, &zero)
			total = &merged
			continue
		}
		merged := addUsageSummaries(total, &summary)
		total = &merged
	}
	if total == nil {
		return UsageSummary{}, nil
	}
	return *total, nil
}

func runtimeIDs(rows []runtimeUsageRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// normalizeResourceAuthorizationUsagePageOptions mirrors
// normalizeResourceAuthorizationUsagePageOptions (:622-628) and
// normalizeListPage: the detail page defaults to 200 rows inside the 1001-row
// window.
func normalizeResourceAuthorizationUsagePageOptions(page, pageSize int) (int, int) {
	if pageSize < 1 {
		pageSize = 200
	}
	if pageSize > 200 {
		pageSize = 200
	}
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
	return page, pageSize
}
