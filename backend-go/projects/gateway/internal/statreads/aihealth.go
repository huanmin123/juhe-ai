package statreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// AI-health read family (Node account-health-monitor.repository.ts): the
// hourly status strip per account from account_health_hourly, optionally
// merged with the J1 jobs outcome store (account_health_outcomes).

var statHourPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3])$`)

// HealthOutcomeSource mirrors accountHealthJobsOutcomeStoreSource: the
// jobs-owned outcome store the gateway only reads. PostgreSQL outcome stores
// stay on the Node reader until the shared pool owns that database (see the
// package report notes); nil keeps the merge absent.
type HealthOutcomeSource struct {
	SQLitePath string
}

func (d *Deps) aiHealthListHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		hours, err := boundedQueryInt(values, "hours", 168, 1, 31*24)
		if err != "" {
			kernel.WriteBadRequest(w, "AI 健康监控参数不合法")
			return
		}
		page, err2 := boundedQueryInt(values, "page", 1, 1, 1<<31-1)
		if err2 != "" {
			kernel.WriteBadRequest(w, "AI 健康监控参数不合法")
			return
		}
		pageSize, err3 := boundedQueryInt(values, "pageSize", 20, 10, 50)
		if err3 != "" {
			kernel.WriteBadRequest(w, "AI 健康监控参数不合法")
			return
		}
		keyword := strings.TrimSpace(values.Get("keyword"))
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		payload, readErr := d.aiHealthList(r, scope, aiHealthOptions{
			Hours: hours, Page: page, PageSize: pageSize, Keyword: keyword,
		})
		d.writeSection(w, payload, readErr)
	}
}

func (d *Deps) aiHealthHourDetailHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		accountID := strings.TrimSpace(values.Get("accountId"))
		statHour := strings.TrimSpace(values.Get("statHour"))
		if accountID == "" || accountIDTooLong(accountID) {
			kernel.WriteBadRequest(w, "AI 健康详情参数不合法")
			return
		}
		if !statHourPattern.MatchString(statHour) || !isValidCalendarStatHour(statHour) {
			kernel.WriteBadRequest(w, "AI 健康详情参数不合法")
			return
		}
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		detail, found, err := d.aiHealthHourDetail(r, scope, accountID, statHour)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		if !found {
			kernel.WriteNotFound(w, "AI 账户不存在或不可访问")
			return
		}
		kernel.WriteOK(w, detail, "")
	}
}

func accountIDTooLong(id string) bool { return len(id) > 200 }

// isValidCalendarStatHour mirrors isValidCalendarStatHour.
func isValidCalendarStatHour(value string) bool {
	datePart := strings.SplitN(value, "T", 2)[0]
	_, _, _, ok := parseDateKeyParts(datePart)
	return ok
}

func boundedQueryInt(values urlValues, key string, fallback, minValue, maxValue int) (int, string) {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return fallback, ""
	}
	parsed, err := parseAlphaInt(raw)
	if err != nil {
		return fallback, "invalid"
	}
	if parsed < minValue {
		return minValue, ""
	}
	if parsed > maxValue {
		return maxValue, ""
	}
	return parsed, ""
}

type urlValues interface{ Get(key string) string }

var errAlphaInt = errors.New("not an integer")

func parseAlphaInt(raw string) (int, error) {
	value := 0
	if raw == "" {
		return 0, errAlphaInt
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, errAlphaInt
		}
		value = value*10 + int(char-'0')
		if value > 1<<31-1 {
			return 0, errAlphaInt
		}
	}
	return value, nil
}

type aiHealthOptions struct {
	Hours    int
	Page     int
	PageSize int
	Keyword  string
}

func (d *Deps) aiHealthList(r *http.Request, scope AccessScope, options aiHealthOptions) (any, error) {
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		return nil, err
	}
	pageItems, hasMore, err := d.loadAiHealthAccountPage(r, scope, options)
	if err != nil {
		return nil, err
	}
	now := d.Now()
	hourBuckets := hourBucketsUntilNow(options.Hours, now, location)
	accountIds := make([]string, 0, len(pageItems))
	for _, item := range pageItems {
		accountIds = append(accountIds, item.ID)
	}
	rows, err := d.loadAccountHealthRows(r, accountIds, hourBuckets)
	if err != nil {
		return nil, err
	}
	j1Outcomes, err := d.j1OutcomesForAccounts(r.Context(), accountIds, now, options.Hours, location, hourBuckets)
	if err != nil {
		return nil, err
	}
	rows = append(rows, j1OutcomeHealthRows(j1Outcomes, hourBuckets, location)...)
	return mapAiHealthList(pageItems, hasMore, options, rows, hourBuckets, j1Outcomes), nil
}

func (d *Deps) aiHealthHourDetail(r *http.Request, scope AccessScope, accountID, statHour string) (any, bool, error) {
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		return nil, false, err
	}
	visible, err := d.aiHealthAccountVisible(r, scope, accountID)
	if err != nil || !visible {
		return nil, false, err
	}
	rows, err := d.queryStats(r, `
		SELECT status, last_observed_at, status_code, error_code, error_message
		FROM `+d.statsTable("account_health_hourly")+`
		WHERE account_id = ? AND stat_hour = ?
		LIMIT 1
	`, accountID, statHour)
	if err != nil {
		return nil, false, err
	}
	var detailRow *Row
	if len(rows) > 0 {
		row := rows[0]
		detailRow = &row
	}
	// J1 outcome merge for the same hour (Node j1OutcomeHealthHourDetail):
	// the latest non-stale outcome whose local hour equals statHour.
	outcomes, err := d.j1OutcomesForAccounts(r.Context(), []string{accountID}, d.Now(), 31*24, location, []string{statHour})
	if err != nil {
		return nil, false, err
	}
	if outcome := newestJ1OutcomeHourRow(outcomes, statHour, location); outcome != nil {
		if detailRow == nil || timestampValue(outcome.ObservedAt) >= timestampValue(detailRow.text("last_observed_at")) {
			merged := Row{
				"status":           outcome.Status(),
				"last_observed_at": outcome.ObservedAt,
				"status_code":      nullableAny(outcome.StatusCode),
				"error_code":       nullableAny(outcome.ErrorCode),
				"error_message":    nullableAny(outcome.ErrorMessage),
			}
			detailRow = &merged
		}
	}
	detail := mapAiHealthHourDetail(statHour, nil)
	if detailRow != nil {
		detail = mapAiHealthHourDetail(statHour, detailRow)
	}
	return detail, true, nil
}

// newestJ1OutcomeHourRow mirrors j1OutcomeHealthHourDetail.
func newestJ1OutcomeHourRow(outcomes []j1Outcome, statHour string, location *time.Location) *j1Outcome {
	var newest *j1Outcome
	for index := range outcomes {
		outcome := &outcomes[index]
		if j1OutcomeHealthStatus(*outcome) == "" {
			continue
		}
		observed, err := time.Parse(time.RFC3339Nano, outcome.ObservedAt)
		if err != nil || hourKeyIn(observed, location) != statHour {
			continue
		}
		if newest == nil || timestampValue(outcome.ObservedAt) >= timestampValue(newest.ObservedAt) {
			newest = outcome
		}
	}
	return newest
}

// nullableAny lifts an optional scalar into the driver-value domain.
func nullableAny[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

// loadAiHealthAccountPage mirrors loadAiHealthAccountPage (SQLite variant).
func (d *Deps) loadAiHealthAccountPage(r *http.Request, scope AccessScope, options aiHealthOptions) ([]aiHealthAccount, bool, error) {
	scopeID := scope.scopedID()
	includeSystemAccount := scope.canAccessAll()
	clauses := []string{"accounts.deleted_at IS NULL"}
	params := []any{}
	if scopeID != "" {
		clauses = append(clauses, "accounts.system_account_id = ?")
		params = append(params, scopeID)
	}
	if options.Keyword != "" {
		filterSQL, filterParams := d.aiHealthAccountNameContainsFilter(options.Keyword, scopeID)
		clauses = append(clauses, filterSQL)
		params = append(params, filterParams...)
	}
	systemAccountExpr := "NULL"
	systemAccountJoin := ""
	if includeSystemAccount {
		systemAccountExpr = "COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id)"
		systemAccountJoin = " LEFT JOIN " + d.businessTable("system_accounts") + ` system_accounts
			ON system_accounts.id = accounts.system_account_id`
	}
	rows, err := d.queryBusiness(r, `
		SELECT
			accounts.id,
			`+systemAccountExpr+` AS system_account_name,
			COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
			accounts.name,
			CASE
				WHEN accounts.authorization_instance_authorization_id IS NOT NULL
					AND (
						authorizations.status <> 'active'
						OR (authorizations.expires_at IS NOT NULL AND authorizations.expires_at <= ?)
					)
				THEN 'disabled'
				WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated')
				THEN source_accounts.status
				ELSE accounts.status
			END AS status,
			accounts.last_health_check_at,
			accounts.last_health_success_at,
			accounts.next_health_check_at
		FROM `+d.businessTable("accounts")+` accounts
		LEFT JOIN `+d.businessTable("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		LEFT JOIN `+d.businessTable("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		`+systemAccountJoin+`
		WHERE `+strings.Join(clauses, " AND ")+`
			AND (
				accounts.authorization_instance_authorization_id IS NULL
				OR authorizations.status IN ('active', 'paused', 'expired')
			)
		ORDER BY
			(accounts.last_used_at IS NULL) ASC,
			accounts.last_used_at DESC,
			accounts.name ASC,
			accounts.id ASC
		LIMIT ? OFFSET ?
	`, flatParams([]any{rfc3339Millis(d.Now())}, params, options.PageSize+1, (options.Page-1)*options.PageSize)...)
	if err != nil {
		return nil, false, err
	}
	pageRows, hasMore := takePageRows(rows, options.PageSize)
	items := make([]aiHealthAccount, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, aiHealthAccount{
			ID:                  row.text("id"),
			SystemAccountName:   row.nullText("system_account_name"),
			ProviderCode:        row.text("provider_code"),
			Name:                row.text("name"),
			Status:              row.text("status"),
			LastHealthCheckAt:   row.nullText("last_health_check_at"),
			LastHealthSuccessAt: row.nullText("last_health_success_at"),
			NextHealthCheckAt:   row.nullText("next_health_check_at"),
		})
	}
	return items, hasMore, nil
}

func (d *Deps) aiHealthAccountVisible(r *http.Request, scope AccessScope, accountID string) (bool, error) {
	scopeID := scope.scopedID()
	clauses := []string{"accounts.deleted_at IS NULL", "accounts.id = ?"}
	params := []any{accountID}
	if scopeID != "" {
		clauses = append(clauses, "accounts.system_account_id = ?")
		params = append(params, scopeID)
	}
	rows, err := d.queryBusiness(r, `
		SELECT accounts.id
		FROM `+d.businessTable("accounts")+` accounts
		LEFT JOIN `+d.businessTable("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		WHERE `+strings.Join(clauses, " AND ")+`
			AND (
				accounts.authorization_instance_authorization_id IS NULL
				OR authorizations.status IN ('active', 'paused', 'expired')
			)
		LIMIT 1
	`, params...)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// loadAccountHealthRows mirrors loadAccountHealthRows.
func (d *Deps) loadAccountHealthRows(r *http.Request, accountIds []string, hourBuckets []string) ([]Row, error) {
	if len(accountIds) == 0 || len(hourBuckets) == 0 {
		return nil, nil
	}
	rows := []Row{}
	startHour := hourBuckets[0]
	endHour := hourBuckets[len(hourBuckets)-1]
	for _, chunk := range chunkStrings(accountIds, 900) {
		chunkRows, err := d.queryStats(r, `
			SELECT account_id, stat_hour, status, last_observed_at
			FROM `+d.statsTable("account_health_hourly")+`
			WHERE account_id IN (`+placeholders(len(chunk))+`) AND stat_hour >= ? AND stat_hour <= ?
			ORDER BY account_id ASC, stat_hour ASC
		`, flatParams(idsToAny(chunk), startHour, endHour)...)
		if err != nil {
			return nil, err
		}
		rows = append(rows, chunkRows...)
	}
	return rows, nil
}

// aiHealthAccountNameContainsFilter mirrors aiHealthAccountNameContainsFilter
// over the account_name_search_terms/documents index.
func (d *Deps) aiHealthAccountNameContainsFilter(keyword, scopedAccountID string) (string, []any) {
	terms := accountNameSearchQueryTerms(keyword)
	if len(terms) == 0 {
		return "0 = 1", nil
	}
	normalized := normalizeAccountNameSearchText(keyword)
	scopeClause := ""
	params := []any{}
	if scopedAccountID != "" {
		scopeClause = "search.system_account_id = ? AND "
		params = append(params, scopedAccountID)
	}
	clause := `accounts.id IN (
		SELECT search.account_id
		FROM ` + d.businessTable("account_name_search_terms") + ` search
		INNER JOIN ` + d.businessTable("account_name_search_documents") + ` documents
			ON documents.account_id = search.account_id
			AND documents.system_account_id = search.system_account_id
		WHERE ` + scopeClause + `search.term IN (` + placeholders(len(terms)) + `)
			AND instr(documents.normalized_name, ?) > 0
		GROUP BY search.account_id
		HAVING COUNT(DISTINCT search.term) = ?
	)`
	params = append(params, idsToAny(terms)...)
	params = append(params, normalized, len(terms))
	return clause, params
}

// ---------------------------------------------------------------------------
// J1 outcome merge (SQLite outcome store only).
// ---------------------------------------------------------------------------

type j1Outcome struct {
	OutcomeID    string  `json:"outcomeId"`
	RequestID    string  `json:"requestId"`
	AccountID    string  `json:"accountId"`
	Outcome      string  `json:"outcome"`
	ObservedAt   string  `json:"observedAt"`
	StatusCode   *int64  `json:"statusCode"`
	ErrorCode    *string `json:"errorCode"`
	ErrorMessage *string `json:"errorMessage"`
	NextDueAt    *string `json:"nextDueAt"`
}

func (d *Deps) j1OutcomesForAccounts(ctx context.Context, accountIds []string, now time.Time, hours int, location *time.Location, hourBuckets []string) ([]j1Outcome, error) {
	if d.HealthOutcomes == nil || d.HealthOutcomes.SQLitePath == "" || len(accountIds) == 0 || len(hourBuckets) == 0 {
		return nil, nil
	}
	observedAfter := time.UnixMilli(now.UnixMilli() - int64(hours+2)*int64(time.Hour/time.Millisecond)).UTC().Format("2006-01-02T15:04:05.000Z")
	db, err := sql.Open("sqlite", "file:"+d.HealthOutcomes.SQLitePath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var rows []Row
	// Per account/hour the latest non-stale outcome inside the zoned hour.
	for _, chunk := range chunkStrings(accountIds, 8) {
		tuples := []string{}
		values := []any{}
		for _, accountID := range chunk {
			for _, statHour := range hourBuckets {
				startAt, endAt := zonedHourRange(statHour, location)
				tuples = append(tuples, "(?, ?, ?, ?)")
				values = append(values, accountID, statHour, startAt, endAt)
			}
		}
		chunkRows, err := queryRowsContext(ctx, db, `
			WITH requested(account_id, stat_hour, start_at, end_at) AS (VALUES `+strings.Join(tuples, ", ")+`)
			SELECT chosen.payload, chosen.observed_at AS storage_observed_at
			FROM requested
			JOIN account_health_outcomes chosen
				ON chosen.outcome_id = (
					SELECT candidate.outcome_id
					FROM account_health_outcomes candidate
					WHERE candidate.account_id = requested.account_id
						AND candidate.observed_at >= requested.start_at
						AND candidate.observed_at < requested.end_at
						AND candidate.observed_at >= ?
						AND COALESCE(json_extract(candidate.payload, '$.outcome'), '') <> 'stale'
					ORDER BY candidate.observed_at DESC, candidate.outcome_id DESC
					LIMIT 1
				)
		`, append(values, observedAfter)...)
		if err != nil {
			return nil, err
		}
		rows = append(rows, chunkRows...)
	}
	outcomes := make([]j1Outcome, 0, len(rows))
	for _, row := range rows {
		var outcome j1Outcome
		if err := json.Unmarshal([]byte(row.text("payload")), &outcome); err != nil {
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

// zonedHourRange returns the [start, end) UTC instants of the local hour.
func zonedHourRange(statHour string, location *time.Location) (string, string) {
	base, err := time.ParseInLocation("2006-01-02T15", statHour, location)
	if err != nil {
		return "", ""
	}
	return base.UTC().Format("2006-01-02T15:04:05.000Z"), base.Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
}

func j1OutcomeHealthStatus(outcome j1Outcome) string {
	switch outcome.Outcome {
	case "stale":
		return ""
	case "complete_success":
		return "success"
	default:
		return "failure"
	}
}

// j1OutcomeHealthRows mirrors j1OutcomeHealthRows.
func j1OutcomeHealthRows(outcomes []j1Outcome, hourBuckets []string, location *time.Location) []Row {
	allowed := map[string]bool{}
	for _, statHour := range hourBuckets {
		allowed[statHour] = true
	}
	latest := map[string]Row{}
	for _, outcome := range outcomes {
		status := j1OutcomeHealthStatus(outcome)
		if status == "" {
			continue
		}
		observed, err := time.Parse(time.RFC3339Nano, outcome.ObservedAt)
		if err != nil {
			continue
		}
		statHour := hourKeyIn(observed, location)
		if !allowed[statHour] {
			continue
		}
		candidate := Row{
			"account_id":       outcome.AccountID,
			"stat_hour":        statHour,
			"status":           status,
			"last_observed_at": outcome.ObservedAt,
			"source_order":     int64(0),
		}
		key := outcome.AccountID + "\x00" + statHour
		existing, ok := latest[key]
		if !ok || timestampValue(candidate.text("last_observed_at")) >= timestampValue(existing.text("last_observed_at")) {
			latest[key] = candidate
		}
	}
	values := make([]Row, 0, len(latest))
	for _, row := range latest {
		values = append(values, row)
	}
	return values
}

func timestampValue(value string) int64 {
	if value == "" {
		return -1 << 62
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return -1 << 62
	}
	return parsed.UnixMilli()
}

// mapAiHealthList mirrors mapAiHealthList + mapAiHealthAccount.
func mapAiHealthList(page []aiHealthAccount, hasMore bool, options aiHealthOptions, rows []Row, hourBuckets []string, j1Outcomes []j1Outcome) aiHealthListResult {
	rowsByAccountHour := map[string]Row{}
	for _, row := range rows {
		key := row.text("account_id") + "\x00" + row.text("stat_hour")
		existing, ok := rowsByAccountHour[key]
		if !ok || timestampValue(row.text("last_observed_at")) >= timestampValue(existing.text("last_observed_at")) {
			rowsByAccountHour[key] = row
		}
	}
	items := make([]aiHealthAccountRow, 0, len(page))
	for _, account := range page {
		latest := latestJ1OutcomeForAccount(j1Outcomes, account.ID)
		items = append(items, mapAiHealthAccount(account, hourBuckets, rowsByAccountHour, latest))
	}
	return aiHealthListResult{Items: items, HasMore: hasMore, Page: options.Page, PageSize: options.PageSize}
}

func latestJ1OutcomeForAccount(outcomes []j1Outcome, accountID string) *j1Outcome {
	var latest *j1Outcome
	for index := range outcomes {
		outcome := &outcomes[index]
		if outcome.AccountID != accountID || j1OutcomeHealthStatus(*outcome) == "" {
			continue
		}
		if latest == nil || timestampValue(outcome.ObservedAt) >= timestampValue(latest.ObservedAt) {
			latest = outcome
		}
	}
	return latest
}

func mapAiHealthAccount(account aiHealthAccount, hourBuckets []string, rowsByAccountHour map[string]Row, latest *j1Outcome) aiHealthAccountRow {
	successHours := 0
	failureHours := 0
	hours := make([]aiHealthHourPoint, 0, len(hourBuckets))
	for _, statHour := range hourBuckets {
		row, ok := rowsByAccountHour[account.ID+"\x00"+statHour]
		if !ok {
			hours = append(hours, aiHealthHourPoint{StatHour: statHour, Status: "unknown"})
			continue
		}
		if row.text("status") == "success" {
			successHours++
		} else {
			failureHours++
		}
		hours = append(hours, aiHealthHourPoint{StatHour: statHour, Status: row.text("status")})
	}
	checkedHours := successHours + failureHours
	latestStatus := "unknown"
	for index := len(hours) - 1; index >= 0; index-- {
		if hours[index].Status != "unknown" {
			latestStatus = hours[index].Status
			break
		}
	}
	row := aiHealthAccountRow{
		ID:                account.ID,
		Name:              account.Name,
		ProviderCode:      account.ProviderCode,
		Status:            account.Status,
		SystemAccountName: account.SystemAccountName,
		LatestStatus:      latestStatus,
		SuccessHours:      successHours,
		FailureHours:      failureHours,
		UnknownHours:      len(hours) - checkedHours,
		Hours:             hours,
	}
	if row.UnknownHours < 0 {
		row.UnknownHours = 0
	}
	if checkedHours > 0 {
		rate := float64(int64((float64(successHours)/float64(checkedHours))*10000+0.5)) / 100
		row.HealthRate = &rate
	}
	if (latest != nil && latest.ObservedAt != "") || account.LastHealthCheckAt != nil {
		if latest != nil {
			row.LastHealthCheckAt = &latest.ObservedAt
		} else {
			row.LastHealthCheckAt = account.LastHealthCheckAt
		}
	}
	if latest != nil && latest.Outcome == "complete_success" {
		row.LastHealthSuccessAt = &latest.ObservedAt
	} else if account.LastHealthSuccessAt != nil {
		row.LastHealthSuccessAt = account.LastHealthSuccessAt
	}
	if (latest != nil && latest.NextDueAt != nil) || account.NextHealthCheckAt != nil {
		if latest != nil && latest.NextDueAt != nil {
			row.NextHealthCheckAt = latest.NextDueAt
		} else {
			row.NextHealthCheckAt = account.NextHealthCheckAt
		}
	}
	return row
}

func mapAiHealthHourDetail(statHour string, row *Row) aiHealthHourDetail {
	if row == nil {
		return aiHealthHourDetail{StatHour: statHour, Status: "unknown"}
	}
	detail := aiHealthHourDetail{
		StatHour:       statHour,
		Status:         row.text("status"),
		LastObservedAt: row.nullText("last_observed_at"),
		StatusCode:     row.nullNumber("status_code"),
		ErrorCode:      row.nullText("error_code"),
		ErrorMessage:   row.nullText("error_message"),
	}
	return detail
}

type aiHealthListResult struct {
	Items    []aiHealthAccountRow `json:"items"`
	HasMore  bool                 `json:"hasMore"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

type aiHealthAccount struct {
	ID                  string  `json:"id"`
	SystemAccountName   *string `json:"-"`
	ProviderCode        string  `json:"-"`
	Name                string  `json:"-"`
	Status              string  `json:"-"`
	LastHealthCheckAt   *string `json:"-"`
	LastHealthSuccessAt *string `json:"-"`
	NextHealthCheckAt   *string `json:"-"`
}

type aiHealthAccountRow struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	ProviderCode        string              `json:"providerCode"`
	Status              string              `json:"status"`
	SystemAccountName   *string             `json:"systemAccountName,omitempty"`
	LastHealthCheckAt   *string             `json:"lastHealthCheckAt,omitempty"`
	LastHealthSuccessAt *string             `json:"lastHealthSuccessAt,omitempty"`
	NextHealthCheckAt   *string             `json:"nextHealthCheckAt,omitempty"`
	LatestStatus        string              `json:"latestStatus"`
	SuccessHours        int                 `json:"successHours"`
	FailureHours        int                 `json:"failureHours"`
	UnknownHours        int                 `json:"unknownHours"`
	HealthRate          *float64            `json:"healthRate,omitempty"`
	Hours               []aiHealthHourPoint `json:"hours"`
}

type aiHealthHourPoint struct {
	StatHour string `json:"statHour"`
	Status   string `json:"status"`
}

type aiHealthHourDetail struct {
	StatHour       string  `json:"statHour"`
	Status         string  `json:"status"`
	LastObservedAt *string `json:"lastObservedAt,omitempty"`
	StatusCode     *int64  `json:"statusCode,omitempty"`
	ErrorCode      *string `json:"errorCode,omitempty"`
	ErrorMessage   *string `json:"errorMessage,omitempty"`
}

// accountNameSearchQueryTerms mirrors accountNameSearchQueryTerms: n-grams of
// the normalized name with length min(len, 3).
func accountNameSearchQueryTerms(keyword string) []string {
	normalized := normalizeAccountNameSearchText(keyword)
	if normalized == "" {
		return nil
	}
	if len([]rune(normalized)) > 128 {
		return nil
	}
	chars := []rune(normalized)
	length := len(chars)
	if length > 3 {
		length = 3
	}
	seen := map[string]bool{}
	terms := []string{}
	for index := 0; index+length <= len(chars); index++ {
		term := string(chars[index : index+length])
		if strings.TrimSpace(term) == "" {
			continue
		}
		if !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

func normalizeAccountNameSearchText(value string) string {
	return strings.TrimSpace(value)
}

// Status mirrors outcomeHealthStatus for the merged detail row.
func (o j1Outcome) Status() string { return j1OutcomeHealthStatus(o) }
