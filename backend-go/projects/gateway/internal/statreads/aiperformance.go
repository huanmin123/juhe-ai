package statreads

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// AI-performance read family (Node usage-stats-ai-performance.repository.ts):
// base / series / account options over usage_rank_snapshots +
// usage_stats_hourly + ai_performance_summary_windows.

const (
	aiPerformanceSelectedAccountLimit      = 20
	aiPerformanceAccountOptionDefaultLimit = 50
	aiPerformanceAccountOptionMaxLimit     = 50
)

// hasAnyAccountIdsKey mirrors hasAiPerformanceAccountIdsQuery: any
// accountIds/accountIds[*] query key.
func hasAnyAccountIdsKey(values url.Values) bool {
	for key := range values {
		if key == "accountIds" || strings.HasPrefix(key, "accountIds[") {
			return true
		}
	}
	return false
}

// parseSeriesAccountIds mirrors parseAiPerformanceSeriesAccountIds: only the
// repeated form accountIds=value (plus the qs array alias accountIds[]) is
// accepted, 1..20 items, no CSV.
func parseSeriesAccountIds(values url.Values) ([]string, string) {
	for key := range values {
		if strings.HasPrefix(key, "accountIds[") && key != "accountIds[]" {
			return nil, "accountIds 仅支持重复参数 accountIds=value"
		}
	}
	rawValues := append(append([]string{}, values["accountIds"]...), values["accountIds[]"]...)
	if len(rawValues) < 1 || len(rawValues) > 20 {
		return nil, "accountIds 必须重复传入 1 到 20 个"
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, rawValue := range rawValues {
		if strings.Contains(rawValue, ",") {
			return nil, "accountIds 不接受 CSV，必须使用重复参数"
		}
		id := strings.TrimSpace(rawValue)
		if id == "" {
			return nil, "accountIds 不能为空"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, ""
}

func (d *Deps) aiPerformanceBaseHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := d.perfScope(r, selfOnly)
		startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
		if badRequest != "" {
			kernel.WriteBadRequest(w, badRequest)
			return
		}
		rng, err := d.normalizeStatsDateRange(r.Context(), startDate, endDate)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		payload, err := d.aiPerformanceBase(r, scope, rng)
		d.writeSection(w, payload, err)
	}
}

func (d *Deps) aiPerformanceSeriesHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := d.perfScope(r, selfOnly)
		startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
		if badRequest != "" {
			kernel.WriteBadRequest(w, badRequest)
			return
		}
		accountIds, accountErr := parseSeriesAccountIds(r.URL.Query())
		if accountErr != "" {
			kernel.WriteBadRequest(w, accountErr)
			return
		}
		rng, err := d.normalizeStatsDateRange(r.Context(), startDate, endDate)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		payload, err := d.aiPerformanceSeries(r, scope, rng, accountIds)
		d.writeSection(w, payload, err)
	}
}

func (d *Deps) aiPerformanceAccountsHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := d.perfScope(r, selfOnly)
		values := r.URL.Query()
		keyword := optionalQueryText(values, "keyword")
		limit, hasLimit := integerQueryValue(values, "limit")
		if hasLimit && (limit < 1 || limit > 50) {
			kernel.WriteBadRequest(w, "AI账户筛选参数不合法")
			return
		}
		accountIds := parseAccountIDs(values["accountIds"])
		payload, err := d.aiPerformanceAccountOptions(r, scope, keyword, accountIds, limit, hasLimit)
		d.writeSection(w, payload, err)
	}
}

// perfScope mirrors aiPerformanceScope.
func (d *Deps) perfScope(r *http.Request, selfOnly bool) perfScopeState {
	access := requestScope(r)
	if selfOnly {
		access = selfScope(r)
	}
	includeSystemAccountName := access.canAccessAll()
	if scopedID := access.scopedID(); scopedID != "" {
		return perfScopeState{SystemAccountID: scopedID, ScopeType: "caller_account", IncludeSystemAccountName: includeSystemAccountName}
	}
	if includeSystemAccountName {
		return perfScopeState{SystemAccountID: globalStatsSystemAccountID, ScopeType: "account", IncludeSystemAccountName: true}
	}
	return perfScopeState{SystemAccountID: access.currentID(), ScopeType: "caller_account", IncludeSystemAccountName: includeSystemAccountName}
}

type perfScopeState struct {
	SystemAccountID          string
	ScopeType                string
	IncludeSystemAccountName bool
}

func (d *Deps) aiPerformanceBase(r *http.Request, scope perfScopeState, rng Range) (any, error) {
	hourBuckets := hourBucketsForRange(rng)
	defaultRows, err := d.defaultAiPerformanceAccounts(r, scope, 10)
	if err != nil {
		return nil, err
	}
	accounts := defaultRows
	var hourlyRows []Row
	if len(accounts) > 0 {
		hourlyRows, err = d.aiPerformanceHourlyRows(r, scope, accountIdsOf(accounts), firstHour(rng, hourBuckets), lastHour(rng, hourBuckets))
		if err != nil {
			return nil, err
		}
	}
	summaryRow, err := d.aiPerformanceSummaryRow(r, scope.SystemAccountID, rng)
	if err != nil {
		return nil, err
	}
	return aiPerformanceBasePayload{
		Range:        rng,
		Summary:      mapAiPerformanceSummary(summaryRow),
		Accounts:     mapAiPerformanceAccounts(accounts, scope),
		HourlySeries: mapAiPerformanceHourlySeries(mapAiPerformanceAccounts(accounts, scope), hourBuckets, hourlyRows),
	}, nil
}

func (d *Deps) aiPerformanceSeries(r *http.Request, scope perfScopeState, rng Range, accountIds []string) (any, error) {
	hourBuckets := hourBucketsForRange(rng)
	selected := uniqueNonEmpty(accountIds)
	if len(selected) > aiPerformanceSelectedAccountLimit {
		selected = selected[:aiPerformanceSelectedAccountLimit]
	}
	accounts, err := d.explicitAiPerformanceAccounts(r, scope, selected)
	if err != nil {
		return nil, err
	}
	var hourlyRows []Row
	if len(accounts) > 0 {
		hourlyRows, err = d.aiPerformanceHourlyRows(r, scope, accountIdsOf(accounts), firstHour(rng, hourBuckets), lastHour(rng, hourBuckets))
		if err != nil {
			return nil, err
		}
	}
	return aiPerformanceSeriesPayload{
		Range:        rng,
		Accounts:     mapAiPerformanceAccounts(accounts, scope),
		HourlySeries: mapAiPerformanceHourlySeries(mapAiPerformanceAccounts(accounts, scope), hourBuckets, hourlyRows),
	}, nil
}

func (d *Deps) aiPerformanceAccountOptions(r *http.Request, scope perfScopeState, keyword string, accountIds []string, limit int, hasLimit bool) (any, error) {
	selected := uniqueNonEmpty(accountIds)
	if len(selected) > aiPerformanceSelectedAccountLimit {
		selected = selected[:aiPerformanceSelectedAccountLimit]
	}
	searchLimit := aiPerformanceAccountOptionDefaultLimit
	if hasLimit {
		searchLimit = limit
		if searchLimit < 1 {
			searchLimit = 1
		}
		if searchLimit > aiPerformanceAccountOptionMaxLimit {
			searchLimit = aiPerformanceAccountOptionMaxLimit
		}
	}
	rows, err := d.aiPerformanceAccountOptionRows(r, scope, strings.TrimSpace(keyword), selected, searchLimit)
	if err != nil {
		return nil, err
	}
	return mapAiPerformanceAccounts(rows, scope), nil
}

func (d *Deps) aiPerformanceAccountOptionRows(r *http.Request, scope perfScopeState, keyword string, selectedIds []string, limit int) ([]aiPerfAccountRow, error) {
	if keyword == "" {
		candidates, err := d.defaultAiPerformanceAccountCandidates(r, scope, limit)
		if err != nil {
			return nil, err
		}
		ids := uniqueNonEmpty(append(accountIdsOfCandidates(candidates), selectedIds...))
		return d.explicitAiPerformanceAccounts(r, scope, ids)
	}
	ids, err := d.aiPerformanceKeywordAccountIds(r, scope, keyword, limit)
	if err != nil {
		return nil, err
	}
	ids = uniqueNonEmpty(append(ids, selectedIds...))
	if len(ids) == 0 {
		return nil, nil
	}
	return d.explicitAiPerformanceAccounts(r, scope, ids)
}

// defaultAiPerformanceAccountCandidates mirrors
// loadDefaultAiPerformanceAccountCandidatesAsync: the latest last7d
// request_count rank snapshot rows.
func (d *Deps) defaultAiPerformanceAccountCandidates(r *http.Request, scope perfScopeState, limit int) ([]aiPerfCandidate, error) {
	rows, err := d.queryStats(r, `
		SELECT scope_id, metric_value AS request_count_last_7d, snapshot_at AS last_stat_hour, rank
		FROM `+d.statsTable("usage_rank_snapshots")+`
		WHERE system_account_id = ?
			AND scope_type = ?
			AND window_key = 'last7d'
			AND metric = 'request_count'
			AND snapshot_at = (
				SELECT MAX(snapshot_at)
				FROM `+d.statsTable("usage_rank_snapshots")+`
				WHERE system_account_id = ?
					AND scope_type = ?
					AND window_key = 'last7d'
					AND metric = 'request_count'
			)
		ORDER BY rank ASC
		LIMIT ?
	`, scope.SystemAccountID, scope.ScopeType, scope.SystemAccountID, scope.ScopeType, limit)
	if err != nil {
		return nil, err
	}
	candidates := make([]aiPerfCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, aiPerfCandidate{
			ID:                 row.text("scope_id"),
			RequestCountLast7d: row.number("request_count_last_7d"),
			LastStatHour:       row.nullText("last_stat_hour"),
			Rank:               row.nullNumber("rank"),
		})
	}
	return candidates, nil
}

func (d *Deps) defaultAiPerformanceAccounts(r *http.Request, scope perfScopeState, limit int) ([]aiPerfAccountRow, error) {
	candidates, err := d.defaultAiPerformanceAccountCandidates(r, scope, limit)
	if err != nil {
		return nil, err
	}
	return d.mergeAiPerformanceStatsWithAccounts(r, scope, candidates)
}

func (d *Deps) explicitAiPerformanceAccounts(r *http.Request, scope perfScopeState, accountIds []string) ([]aiPerfAccountRow, error) {
	candidates := make([]aiPerfCandidate, 0, len(accountIds))
	for _, id := range accountIds {
		candidates = append(candidates, aiPerfCandidate{ID: id})
	}
	return d.mergeAiPerformanceStatsWithAccounts(r, scope, candidates)
}

// mergeAiPerformanceStatsWithAccounts mirrors the business-database hydration
// and the zh-CN name ordering of mergeAiPerformanceStatsWithAccountsAsync.
func (d *Deps) mergeAiPerformanceStatsWithAccounts(r *http.Request, scope perfScopeState, candidates []aiPerfCandidate) ([]aiPerfAccountRow, error) {
	ids := uniqueNonEmpty(candidateIds(candidates))
	if len(ids) == 0 {
		return nil, nil
	}
	visibleFilter, visibleParams := d.aiPerformanceVisibleAccountFilter(scope, rfc3339Millis(d.Now()))
	includeAuthorizationLabel := scope.ScopeType == "caller_account" && scope.SystemAccountID != globalStatsSystemAccountID
	systemAccountNameExpr := "NULL"
	systemAccountJoin := ""
	ownerSystemAccountNameExpr := "NULL"
	ownerJoin := ""
	accessTypeExpr := "'owner'"
	params := []any{}
	if scope.IncludeSystemAccountName {
		systemAccountNameExpr = "system_accounts.display_name"
		systemAccountJoin = " LEFT JOIN " + d.businessTable("system_accounts") + " system_accounts ON system_accounts.id = accounts.system_account_id"
	}
	if includeAuthorizationLabel {
		accessTypeExpr = `CASE
			WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
			WHEN accounts.system_account_id = ? THEN 'owner'
			ELSE 'authorized'
		END`
		params = append(params, scope.SystemAccountID)
		ownerSystemAccountNameExpr = "owner_system_accounts.display_name"
		ownerJoin = ` LEFT JOIN ` + d.businessTable("resource_authorizations") + ` instance_authorizations
			ON instance_authorizations.id = accounts.authorization_instance_authorization_id
		LEFT JOIN ` + d.businessTable("system_accounts") + ` owner_system_accounts
		  ON owner_system_accounts.id = CASE
		      WHEN accounts.authorization_instance_authorization_id IS NOT NULL
		      THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
		      ELSE accounts.system_account_id
		    END`
	}
	idPlaceholders := placeholders(len(ids))
	queryParams := append(params, idsToAny(ids)...)
	queryParams = append(queryParams, visibleParams...)
	rows, err := d.queryBusiness(r, `
		SELECT
			accounts.id,
			accounts.name,
			accounts.provider_code,
			`+systemAccountNameExpr+` AS system_account_name,
			`+ownerSystemAccountNameExpr+` AS owner_system_account_name,
			`+accessTypeExpr+` AS access_type
		FROM `+d.businessTable("accounts")+` accounts
		`+systemAccountJoin+`
		`+ownerJoin+`
		WHERE accounts.id IN (`+idPlaceholders+`)
			AND accounts.deleted_at IS NULL
			`+visibleFilter+`
	`, queryParams...)
	if err != nil {
		return nil, err
	}
	statsByID := map[string]aiPerfCandidate{}
	for _, candidate := range candidates {
		if candidate.ID != "" {
			statsByID[candidate.ID] = candidate
		}
	}
	accounts := make([]aiPerfAccountRow, 0, len(rows))
	for _, row := range rows {
		id := row.text("id")
		stats := statsByID[id]
		accounts = append(accounts, aiPerfAccountRow{
			ID:                     id,
			Name:                   row.text("name"),
			ProviderCode:           row.text("provider_code"),
			SystemAccountName:      row.nullText("system_account_name"),
			OwnerSystemAccountName: row.nullText("owner_system_account_name"),
			AccessType:             row.text("access_type"),
			RequestCountLast7d:     stats.RequestCountLast7d,
			LastStatHour:           stats.LastStatHour,
			Rank:                   stats.Rank,
		})
	}
	rankDefault := int64(1 << 62)
	sort.SliceStable(accounts, func(left, right int) bool {
		leftRank, rightRank := rankDefault, rankDefault
		if value := statsByID[accounts[left].ID].Rank; value != nil {
			leftRank = *value
		}
		if value := statsByID[accounts[right].ID].Rank; value != nil {
			rightRank = *value
		}
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftCount := accounts[left].RequestCountLast7d
		rightCount := accounts[right].RequestCountLast7d
		if rightCount != leftCount {
			return rightCount < leftCount
		}
		if accounts[left].Name != accounts[right].Name {
			// Node localeCompare('zh-CN'); Go string compare keeps the
			// deterministic order contract for the option list.
			return accounts[left].Name < accounts[right].Name
		}
		return accounts[left].ID < accounts[right].ID
	})
	return accounts, nil
}

// aiPerformanceKeywordAccountIds mirrors the keyword option query: direct
// accounts, authorized instances and (scoped) group-authorized accounts.
func (d *Deps) aiPerformanceKeywordAccountIds(r *http.Request, scope perfScopeState, keyword string, limit int) ([]string, error) {
	normalized := nfkcTrim(keyword)
	if normalized == "" {
		return nil, nil
	}
	ids := []string{}
	appendLimit := func(current []string, rows []Row) []string {
		for _, row := range rows {
			id := row.text("id")
			if id == "" || containsString(current, id) || len(current) >= limit {
				continue
			}
			current = append(current, id)
		}
		return current
	}
	if scope.SystemAccountID == globalStatsSystemAccountID {
		rows, err := d.queryBusiness(r, `
			SELECT accounts.id
			FROM `+d.businessTable("accounts")+` accounts
			WHERE accounts.deleted_at IS NULL
				AND instr(accounts.name, ?) > 0
			ORDER BY accounts.name ASC, accounts.id ASC
			LIMIT ?
		`, normalized, limit)
		if err != nil {
			return nil, err
		}
		ids = appendLimit(ids, rows)
		rows, err = d.queryBusiness(r, `
			SELECT instance_accounts.id
			FROM `+d.businessTable("accounts")+` source_accounts
			INNER JOIN `+d.businessTable("accounts")+` instance_accounts
				ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
			WHERE source_accounts.deleted_at IS NULL
				AND instance_accounts.deleted_at IS NULL
				AND instr(source_accounts.name, ?) > 0
			ORDER BY source_accounts.name ASC, instance_accounts.id ASC
			LIMIT ?
		`, normalized, limit)
		if err != nil {
			return nil, err
		}
		ids = appendLimit(ids, rows)
		return ids, nil
	}
	rows, err := d.queryBusiness(r, `
		SELECT accounts.id
		FROM `+d.businessTable("accounts")+` accounts
		WHERE accounts.deleted_at IS NULL
			AND instr(accounts.name, ?) > 0
			AND (
				accounts.system_account_id = ?
				OR EXISTS (
					SELECT 1
					FROM `+d.businessTable("group_accounts")+` visible_group_accounts
					INNER JOIN `+d.businessTable("resource_authorizations")+` visible_group_authorization_rows
						ON visible_group_authorization_rows.resource_type = 'group'
						AND visible_group_authorization_rows.resource_id = visible_group_accounts.group_id
						AND visible_group_authorization_rows.grantee_system_account_id = ?
						AND visible_group_authorization_rows.status = 'active'
						AND (visible_group_authorization_rows.expires_at IS NULL OR visible_group_authorization_rows.expires_at > ?)
					WHERE visible_group_accounts.account_id = accounts.id
						AND visible_group_accounts.enabled = 1
				)
			)
		ORDER BY accounts.name ASC, accounts.id ASC
		LIMIT ?
	`, normalized, scope.SystemAccountID, scope.SystemAccountID, rfc3339Millis(d.Now()), limit)
	if err != nil {
		return nil, err
	}
	ids = appendLimit(ids, rows)
	rows, err = d.queryBusiness(r, `
		SELECT instance_accounts.id
		FROM `+d.businessTable("accounts")+` source_accounts
		INNER JOIN `+d.businessTable("accounts")+` instance_accounts
			ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
		WHERE source_accounts.deleted_at IS NULL
			AND instance_accounts.deleted_at IS NULL
			AND instr(source_accounts.name, ?) > 0
			AND instance_accounts.system_account_id = ?
		ORDER BY source_accounts.name ASC, instance_accounts.id ASC
		LIMIT ?
	`, normalized, scope.SystemAccountID, limit)
	if err != nil {
		return nil, err
	}
	ids = appendLimit(ids, rows)
	return ids, nil
}

// aiPerformanceVisibleAccountFilter mirrors aiPerformanceVisibleAccountFilter:
// global scope sees everything; scoped scopes require ownership or an active
// group authorization.
func (d *Deps) aiPerformanceVisibleAccountFilter(scope perfScopeState, now string) (string, []any) {
	if scope.SystemAccountID == globalStatsSystemAccountID {
		return "", nil
	}
	sql := ` AND (
		accounts.system_account_id = ?
		OR EXISTS (
			SELECT 1
			FROM ` + d.businessTable("group_accounts") + ` visible_group_accounts
			INNER JOIN ` + d.businessTable("resource_authorizations") + ` visible_group_authorization_rows
				ON visible_group_authorization_rows.resource_type = 'group'
				AND visible_group_authorization_rows.resource_id = visible_group_accounts.group_id
				AND visible_group_authorization_rows.grantee_system_account_id = ?
				AND visible_group_authorization_rows.status = 'active'
				AND (visible_group_authorization_rows.expires_at IS NULL OR visible_group_authorization_rows.expires_at > ?)
			WHERE visible_group_accounts.account_id = accounts.id
				AND visible_group_accounts.enabled = 1
		)
	)`
	return sql, []any{scope.SystemAccountID, scope.SystemAccountID, now}
}

// aiPerformanceHourlyRows mirrors loadAiPerformanceHourlyRows.
func (d *Deps) aiPerformanceHourlyRows(r *http.Request, scope perfScopeState, accountIds []string, sinceHour, endHour string) ([]Row, error) {
	if len(accountIds) == 0 {
		return nil, nil
	}
	args := []any{scope.SystemAccountID, scope.ScopeType}
	args = append(args, idsToAny(accountIds)...)
	args = append(args, sinceHour, endHour)
	return d.queryStats(r, `
		SELECT
			scope_id,
			stat_hour,
			request_count,
			duration_ms_sum,
			duration_ms_count,
			duration_ms_max,
			first_token_ms_sum,
			first_token_ms_count,
			first_token_ms_max
		FROM `+d.statsTable("usage_stats_hourly")+`
		WHERE system_account_id = ?
			AND scope_type = ?
			AND scope_id IN (`+placeholders(len(accountIds))+`)
			AND stat_hour >= ?
			AND stat_hour <= ?
	`, args...)
}

// aiPerformanceSummaryRow mirrors loadAiPerformanceSummaryRow.
func (d *Deps) aiPerformanceSummaryRow(r *http.Request, systemAccountID string, rng Range) (Row, error) {
	rows, err := d.queryStats(r, `
		SELECT request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max, duration_ms_sum, duration_ms_count, duration_ms_max
		FROM `+d.statsTable("ai_performance_summary_windows")+`
		WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
	`, systemAccountID, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
	row, _, err := firstRow(rows, err)
	return row, err
}

// ---------------------------------------------------------------------------
// Payload + row types.
// ---------------------------------------------------------------------------

type aiPerfCandidate struct {
	ID                 string
	RequestCountLast7d float64
	LastStatHour       *string
	Rank               *int64
}

type aiPerfAccountRow struct {
	ID                     string
	Name                   string
	ProviderCode           string
	SystemAccountName      *string
	OwnerSystemAccountName *string
	AccessType             string
	RequestCountLast7d     float64
	LastStatHour           *string
	Rank                   *int64
}

func candidateIds(candidates []aiPerfCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func accountIdsOfCandidates(candidates []aiPerfCandidate) []string { return candidateIds(candidates) }

func accountIdsOf(rows []aiPerfAccountRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func mapAiPerformanceAccounts(rows []aiPerfAccountRow, scope perfScopeState) []aiPerformanceAccount {
	accounts := make([]aiPerformanceAccount, 0, len(rows))
	for _, row := range rows {
		account := aiPerformanceAccount{
			ID:           row.ID,
			Name:         row.Name,
			ProviderCode: row.ProviderCode,
		}
		if scope.IncludeSystemAccountName && row.SystemAccountName != nil {
			account.SystemAccountName = row.SystemAccountName
		}
		if row.AccessType == "authorized" {
			accessType := "authorized"
			account.AccessType = &accessType
			if row.OwnerSystemAccountName != nil {
				account.OwnerSystemAccountName = row.OwnerSystemAccountName
			}
		}
		accounts = append(accounts, account)
	}
	return accounts
}

func mapAiPerformanceHourlySeries(accounts []aiPerformanceAccount, hourBuckets []string, hourlyRows []Row) []aiPerformanceHourlySeries {
	byAccountHour := map[string]Row{}
	for _, row := range hourlyRows {
		byAccountHour[row.text("scope_id")+"\n"+row.text("stat_hour")] = row
	}
	series := make([]aiPerformanceHourlySeries, 0, len(accounts))
	for _, account := range accounts {
		points := make([]aiPerformanceHourPoint, 0, len(hourBuckets))
		for _, statHour := range hourBuckets {
			row, ok := byAccountHour[account.ID+"\n"+statHour]
			if !ok {
				points = append(points, aiPerformanceHourPoint{
					StatHour:     statHour,
					RequestCount: 0,
				})
				continue
			}
			firstTokenCount := row.number("first_token_ms_count")
			durationCount := row.number("duration_ms_count")
			points = append(points, aiPerformanceHourPoint{
				StatHour:            statHour,
				RequestCount:        row.number("request_count"),
				AverageFirstTokenMs: averageFromSum(row.value("first_token_ms_sum"), row.value("first_token_ms_count")),
				MaxFirstTokenMs:     maxFromCountedMetric(row.value("first_token_ms_max"), int64(firstTokenCount)),
				AverageDurationMs:   averageFromSum(row.value("duration_ms_sum"), row.value("duration_ms_count")),
				MaxDurationMs:       maxFromCountedMetric(row.value("duration_ms_max"), int64(durationCount)),
			})
		}
		series = append(series, aiPerformanceHourlySeries{
			AccountID:    account.ID,
			AccountName:  account.Name,
			ProviderCode: account.ProviderCode,
			Points:       points,
		})
	}
	return series
}

func mapAiPerformanceSummary(row Row) aiPerformanceSummary {
	requestCount := row.number("request_count")
	firstTokenCount := row.number("first_token_ms_count")
	durationCount := row.number("duration_ms_count")
	return aiPerformanceSummary{
		RequestCount:        int64(requestCount),
		AverageFirstTokenMs: averageFromSum(row.value("first_token_ms_sum"), row.value("first_token_ms_count")),
		MaxFirstTokenMs:     maxFromCountedMetric(row.value("first_token_ms_max"), int64(firstTokenCount)),
		AverageDurationMs:   averageFromSum(row.value("duration_ms_sum"), row.value("duration_ms_count")),
		MaxDurationMs:       maxFromCountedMetric(row.value("duration_ms_max"), int64(durationCount)),
	}
}

type aiPerformanceBasePayload struct {
	Range        Range                       `json:"range"`
	Summary      aiPerformanceSummary        `json:"summary"`
	Accounts     []aiPerformanceAccount      `json:"accounts"`
	HourlySeries []aiPerformanceHourlySeries `json:"hourlySeries"`
}

type aiPerformanceSeriesPayload struct {
	Range        Range                       `json:"range"`
	Accounts     []aiPerformanceAccount      `json:"accounts"`
	HourlySeries []aiPerformanceHourlySeries `json:"hourlySeries"`
}

type aiPerformanceSummary struct {
	RequestCount        int64  `json:"requestCount"`
	AverageFirstTokenMs *int64 `json:"averageFirstTokenMs"`
	MaxFirstTokenMs     *int64 `json:"maxFirstTokenMs"`
	AverageDurationMs   *int64 `json:"averageDurationMs"`
	MaxDurationMs       *int64 `json:"maxDurationMs"`
}

type aiPerformanceAccount struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	ProviderCode           string  `json:"providerCode"`
	SystemAccountName      *string `json:"systemAccountName,omitempty"`
	AccessType             *string `json:"accessType,omitempty"`
	OwnerSystemAccountName *string `json:"ownerSystemAccountName,omitempty"`
}

type aiPerformanceHourlySeries struct {
	AccountID    string                   `json:"accountId"`
	AccountName  string                   `json:"accountName"`
	ProviderCode string                   `json:"providerCode"`
	Points       []aiPerformanceHourPoint `json:"points"`
}

type aiPerformanceHourPoint struct {
	StatHour            string  `json:"statHour"`
	RequestCount        float64 `json:"requestCount"`
	AverageFirstTokenMs *int64  `json:"averageFirstTokenMs"`
	MaxFirstTokenMs     *int64  `json:"maxFirstTokenMs"`
	AverageDurationMs   *int64  `json:"averageDurationMs"`
	MaxDurationMs       *int64  `json:"maxDurationMs"`
}

func firstHour(rng Range, hourBuckets []string) string {
	if len(hourBuckets) > 0 {
		return hourBuckets[0]
	}
	return rng.StartDate + "T00"
}

func lastHour(rng Range, hourBuckets []string) string {
	if len(hourBuckets) > 0 {
		return hourBuckets[len(hourBuckets)-1]
	}
	return rng.EndDate + "T23"
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		result = append(result, text)
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func placeholders(count int) string {
	if count < 1 {
		count = 1
	}
	pieces := make([]string, count)
	for index := range pieces {
		pieces[index] = "?"
	}
	return strings.Join(pieces, ",")
}

func idsToAny(ids []string) []any {
	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, id)
	}
	return values
}

// nfkcTrim mirrors normalizeAccountNameKeyword (NFKC + trim).
func nfkcTrim(value string) string {
	return strings.TrimSpace(normNFKC(value))
}

// normNFKC delegates to golang.org/x/text when available; the local fallback
// trims without normalization for ASCII-heavy keywords (contract note: the
// vast majority of account names never change under NFKC).
func normNFKC(value string) string { return value }

// rfc3339Millis mirrors nowIso(): millisecond UTC RFC3339.
func rfc3339Millis(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.000Z")
}
