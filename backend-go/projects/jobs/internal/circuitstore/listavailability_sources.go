package circuitstore

// 投影 LoadItems 的 stats 库与业务库辅助读面：对照归档 Node
//   - storage/account-management-list-usage.repository.ts（today/authorization total）
//   - storage/request-quota-checker.ts + storage/request-quota-limits.ts（quota 成本与超限）
//   - storage/account-status-snapshot.repository.ts loadAccountStatusAuthorizationQuotaExceededAsync
//   - storage/account-balance.repository.ts loadAccountBalanceSnapshotRecordsByAccountIdsAsync /
//     accountBalanceSnapshotMatchesConfiguration + accountBalanceSnapshotForList
//   - storage/account-api-key-runtime-state.repository.ts loadAccountApiKeyRuntimeSummariesByAccountIdsAsync
//   - storage/account-circuit-control-plane.repository.ts listAccountCircuitIncidentsByRuntimeKeysInClient
//   - modules/gateway/runtime/account-circuit-control-plane-bridge.ts publicAccountCircuitSummariesFromIncidents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// ---- 通用 helpers ----

// numberValue 对齐 Node numberValue（数值容错 + 默认 0）。
func numberValue(value any, fallback ...int) float64 {
	defaultValue := 0
	if len(fallback) > 0 {
		defaultValue = fallback[0]
	}
	switch typed := value.(type) {
	case nil:
		return float64(defaultValue)
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	case float64:
		return typed
	case float32:
		return float64(typed)
	case []byte:
		return numberValue(string(typed), fallback...)
	case string:
		parsed, ok := finiteNumber(strings.TrimSpace(typed))
		if !ok {
			return float64(defaultValue)
		}
		return parsed
	}
	return float64(defaultValue)
}

func finiteNumber(text string) (float64, bool) {
	if text == "" {
		return 0, false
	}
	var parsed float64
	if _, err := fmt.Sscanf(text, "%g", &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}

// booleanValue 对齐 Node booleanValue（numberValue(v) === 1）。
func booleanValue(value any) bool {
	return numberValue(value) == 1
}

func coalesceAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func convertScanError(err error) error { return err }

// timeParamText 返回与方言匹配的时间绑定（PG timestamptz 用 time.Time，
// SQLite 用 RFC3339Nano 文本）。
func timeParamText(postgres bool, t time.Time) any {
	if postgres {
		return t
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func sha256HexShort(input string, length int) string {
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])[:length]
}

// ---- usage 汇总（account-management-list-usage.repository.ts）----

type usageValue struct {
	RequestCount float64
	TotalTokens  float64
	TotalCost    float64
	LastUsedAt   string
}

type usageScope struct {
	rowKey          string
	systemAccountID string
	scopeType       string
	scopeID         string
}

// loadUsageSummaries 对齐 hydrateAccountManagementStatusSeedsDirect 的
// todayUsage + authorizationTotal 双查询。
func (l *ProjectionItemLoader) loadUsageSummaries(ctx context.Context, rows []managementRow, now time.Time, timezone *time.Location) (map[string]usageValue, map[string]usageValue, error) {
	todayScopes := make([]usageScope, 0, len(rows))
	authorizedScopes := make([]usageScope, 0, len(rows))
	for i := range rows {
		row := rows[i]
		if isAuthorizedRow(row) {
			scope := usageScope{rowKey: row.id, systemAccountID: row.systemAccountID, scopeType: "account_authorization", scopeID: row.authorizationID.String}
			todayScopes = append(todayScopes, scope)
			authorizedScopes = append(authorizedScopes, scope)
			continue
		}
		todayScopes = append(todayScopes, usageScope{rowKey: row.id, systemAccountID: row.systemAccountID, scopeType: "account", scopeID: row.id})
	}
	today, err := l.loadUsage(ctx, todayScopes, usageStatDate(now, timezone))
	if err != nil {
		return nil, nil, err
	}
	totals, err := l.loadUsage(ctx, authorizedScopes, "")
	if err != nil {
		return nil, nil, err
	}
	return today, totals, nil
}

func (l *ProjectionItemLoader) statsTable(name string) string {
	if l.statsPostgres {
		return "juhe_stats." + name
	}
	return name
}

// loadUsage 对齐 loadAccountManagementListUsageAsync（VALUES joined 查询，
// statDate 空串读 usage_stats_totals）。
func (l *ProjectionItemLoader) loadUsage(ctx context.Context, scopes []usageScope, statDate string) (map[string]usageValue, error) {
	output := map[string]usageValue{}
	unique := make([]usageScope, 0, len(scopes))
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		if scope.rowKey == "" || scope.systemAccountID == "" || scope.scopeID == "" {
			continue
		}
		key := scope.rowKey + "\x00" + scope.systemAccountID + "\x00" + scope.scopeType + "\x00" + scope.scopeID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, scope)
	}
	if len(unique) == 0 {
		return output, nil
	}
	table := l.statsTable("usage_stats_daily")
	if statDate == "" {
		table = l.statsTable("usage_stats_totals")
	}
	requestedRows := make([]string, len(unique))
	args := make([]any, 0, len(unique)*4+1)
	for index, scope := range unique {
		requestedRows[index] = "(?, ?, ?, ?)"
		args = append(args, scope.rowKey, scope.systemAccountID, scope.scopeType, scope.scopeID)
	}
	query := `
    WITH requested(row_key, system_account_id, scope_type, scope_id) AS (
      VALUES ` + strings.Join(requestedRows, ", ") + `
    )
    SELECT
      requested.row_key,
      COALESCE(usage_rows.request_count, 0) AS request_count,
      COALESCE(usage_rows.input_tokens, 0) + COALESCE(usage_rows.output_tokens, 0) AS total_tokens,
      COALESCE(usage_rows.total_cost_usd, 0) AS total_cost,
      usage_rows.last_used_at
    FROM requested
    LEFT JOIN ` + table + ` usage_rows
      ON usage_rows.system_account_id = requested.system_account_id
      AND usage_rows.scope_type = requested.scope_type
      AND usage_rows.scope_id = requested.scope_id`
	if statDate != "" {
		query += ` AND usage_rows.stat_date = ?`
		args = append(args, statDate)
	}
	rows, err := l.statsDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rowKey   string
			requests any
			tokens   any
			cost     any
			lastUsed sql.NullString
		)
		if err := rows.Scan(&rowKey, &requests, &tokens, &cost, &lastUsed); err != nil {
			return nil, err
		}
		value := usageValue{
			RequestCount: numberValue(requests),
			TotalTokens:  numberValue(tokens),
			TotalCost:    numberValue(cost),
		}
		if lastUsed.Valid {
			value.LastUsedAt = lastUsed.String
		}
		output[rowKey] = value
	}
	return output, rows.Err()
}

// ---- quota（request-quota-checker + team limits）----

type quotaLimits struct {
	Hourly  *quotaLimitWindow
	Daily   *quotaLimitWindow
	Weekly  *quotaLimitWindow
	Monthly *quotaLimitWindow
	Total   *quotaLimitWindow
}

type quotaLimitWindow struct {
	Enabled bool
	Limit   float64
	Hours   int
}

type quotaCosts struct {
	Hourly  float64
	Daily   float64
	Weekly  float64
	Monthly float64
	Total   float64
}

// parseQuotaLimits 对齐 parseRequestQuotaLimitsJson。
func parseQuotaLimits(raw string) quotaLimits {
	var parsed struct {
		Hourly  *struct {
			Enabled bool    `json:"enabled"`
			Limit   float64 `json:"limit"`
			Hours   int     `json:"hours"`
		} `json:"hourly"`
		Daily   *struct {
			Enabled bool    `json:"enabled"`
			Limit   float64 `json:"limit"`
		} `json:"daily"`
		Weekly  *struct {
			Enabled bool    `json:"enabled"`
			Limit   float64 `json:"limit"`
		} `json:"weekly"`
		Monthly *struct {
			Enabled bool    `json:"enabled"`
			Limit   float64 `json:"limit"`
		} `json:"monthly"`
		Total *struct {
			Enabled bool    `json:"enabled"`
			Limit   float64 `json:"limit"`
		} `json:"total"`
	}
	_ = json.Unmarshal([]byte(raw), &parsed)
	limits := quotaLimits{}
	if parsed.Hourly != nil {
		limits.Hourly = &quotaLimitWindow{Enabled: parsed.Hourly.Enabled, Limit: parsed.Hourly.Limit, Hours: parsed.Hourly.Hours}
	}
	if parsed.Daily != nil {
		limits.Daily = &quotaLimitWindow{Enabled: parsed.Daily.Enabled, Limit: parsed.Daily.Limit}
	}
	if parsed.Weekly != nil {
		limits.Weekly = &quotaLimitWindow{Enabled: parsed.Weekly.Enabled, Limit: parsed.Weekly.Limit}
	}
	if parsed.Monthly != nil {
		limits.Monthly = &quotaLimitWindow{Enabled: parsed.Monthly.Enabled, Limit: parsed.Monthly.Limit}
	}
	if parsed.Total != nil {
		limits.Total = &quotaLimitWindow{Enabled: parsed.Total.Enabled, Limit: parsed.Total.Limit}
	}
	return limits
}

func (l quotaLimits) hasEnabled() bool {
	return (l.Hourly != nil && l.Hourly.Enabled) || (l.Daily != nil && l.Daily.Enabled) ||
		(l.Weekly != nil && l.Weekly.Enabled) || (l.Monthly != nil && l.Monthly.Enabled) ||
		(l.Total != nil && l.Total.Enabled)
}

func (l quotaLimits) exceeded(costs quotaCosts) bool {
	return (l.Hourly != nil && l.Hourly.Enabled && costs.Hourly >= l.Hourly.Limit) ||
		(l.Daily != nil && l.Daily.Enabled && costs.Daily >= l.Daily.Limit) ||
		(l.Weekly != nil && l.Weekly.Enabled && costs.Weekly >= l.Weekly.Limit) ||
		(l.Monthly != nil && l.Monthly.Enabled && costs.Monthly >= l.Monthly.Limit) ||
		(l.Total != nil && l.Total.Enabled && costs.Total >= l.Total.Limit)
}

// parseQuotaLimitsPayload 对齐 parseRequestQuotaLimitsJson 的 payload 形状：
// 空串返回空对象（Node emptyRequestQuotaLimits）；仅保留五类窗口键；
// 非法 JSON/结构报错（调用方走释放重放，与 Node JSON.parse 抛错一致）。
func parseQuotaLimitsPayload(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	output := map[string]any{}
	if trimmed == "" {
		return output, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("授权额度限制参数无效: %w", err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("请求额度限制参数无效")
	}
	for _, key := range []string{"hourly", "daily", "weekly", "monthly", "total"} {
		if value, exists := object[key]; exists {
			output[key] = value
		}
	}
	return output, nil
}

// quotaCostCheck 是 quota 超限检查项（direct + team 继承双 scope）。
type quotaCostCheck struct {
	authorizationID string
	systemAccountID string
	limits          quotaLimits
	scopeType       string
	scopeID         string
	hourlyHours     *int
}

// loadAuthorizationQuotaStatus 对齐 loadAccountStatusAuthorizationQuotaExceededAsync：
// direct limits（account_authorization scope）+ team 继承 limits
// （account_authorization_team scope）双检查，返回 exceeded/resetAt。
func (l *ProjectionItemLoader) loadAuthorizationQuotaStatus(ctx context.Context, rows []managementRow, now time.Time, timezone *time.Location) (map[string]bool, map[string]string, error) {
	exceededByAuth := map[string]bool{}
	resetByAuth := map[string]string{}
	teamLimitsByAuth, err := l.loadTeamLimitJSON(ctx, rows, now)
	if err != nil {
		return nil, nil, err
	}
	var checks []quotaCostCheck
	for i := range rows {
		row := rows[i]
		if !isAuthorizedRow(row) {
			continue
		}
		exceededByAuth[row.authorizationID.String] = false
		appendCheck := func(limits quotaLimits, scopeType, scopeID string) {
			check := quotaCostCheck{
				authorizationID: row.authorizationID.String,
				systemAccountID: row.systemAccountID,
				limits:          limits,
				scopeType:       scopeType,
				scopeID:         scopeID,
			}
			if limits.Hourly != nil && limits.Hourly.Enabled && limits.Hourly.Hours > 0 {
				hours := limits.Hourly.Hours
				check.hourlyHours = &hours
			}
			checks = append(checks, check)
		}
		direct := parseQuotaLimits(row.authorizationLimitsJSON.String)
		if direct.hasEnabled() {
			appendCheck(direct, "account_authorization", row.authorizationID.String)
		}
		if row.authorizationEffectiveSourceTeamID.Valid && row.authorizationEffectiveSourceTeamID.String != "" {
			inherited := parseQuotaLimits(teamLimitsByAuth[row.authorizationID.String])
			if inherited.hasEnabled() {
				appendCheck(inherited, "account_authorization_team", row.id+":"+row.authorizationEffectiveSourceTeamID.String)
			}
		}
	}
	if len(checks) == 0 {
		return exceededByAuth, resetByAuth, nil
	}
	costsByKey, err := l.loadQuotaCosts(ctx, checks, now, timezone)
	if err != nil {
		return nil, nil, err
	}
	for _, check := range checks {
		key := quotaCostKey(check.systemAccountID, check.scopeType, check.scopeID, check.hourlyHours)
		costs, found := costsByKey[key]
		if !found || !check.limits.exceeded(costs) {
			continue
		}
		exceededByAuth[check.authorizationID] = true
		resetAt := quotaResetAtOf(check.limits, costs, now, timezone)
		if resetAt == "" {
			continue
		}
		if current, exists := resetByAuth[check.authorizationID]; !exists || current == "" || resetAt < current {
			resetByAuth[check.authorizationID] = resetAt
		}
	}
	return exceededByAuth, resetByAuth, nil
}

// quotaCostInput 是 quota 成本查询输入维度。
type quotaCostInput struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	hourlyHours     *int
}

// loadQuotaCosts 对齐 loadRequestQuotaCostsBatchAsync 语义（totals/daily/
// weekly/monthly 四表 + hourly window 表；批量以逐 input 查询等价实现，
// 单轮 batch ≤ 2×100 个 scope）。
func (l *ProjectionItemLoader) loadQuotaCosts(ctx context.Context, checks []quotaCostCheck, now time.Time, timezone *time.Location) (map[string]quotaCosts, error) {
	output := map[string]quotaCosts{}
	if len(checks) == 0 {
		return output, nil
	}
	statDate := usageStatDate(now, timezone)
	statWeek := usageWeekKey(now, timezone)
	statMonth := usageMonthKey(now, timezone)
	for _, check := range checks {
		input := quotaCostInput{
			systemAccountID: check.systemAccountID,
			scopeType:       check.scopeType,
			scopeID:         check.scopeID,
			hourlyHours:     check.hourlyHours,
		}
		key := quotaCostKey(input.systemAccountID, input.scopeType, input.scopeID, input.hourlyHours)
		if _, exists := output[key]; exists {
			continue
		}
		costs := quotaCosts{}
		lookups := []struct {
			table  string
			column string
			value  string
		}{
			{l.statsTable("usage_stats_totals"), "", ""},
			{l.statsTable("usage_stats_daily"), "stat_date", statDate},
			{l.statsTable("usage_stats_weekly"), "stat_week", statWeek},
			{l.statsTable("usage_stats_monthly"), "stat_month", statMonth},
		}
		if input.hourlyHours != nil {
			lookups = append(lookups, struct {
				table  string
				column string
				value  string
			}{l.statsTable("usage_quota_hourly_windows"), "window_hours", fmt.Sprintf("%d", maxInt(1, *input.hourlyHours))})
		}
		for _, lookup := range lookups {
			query := `SELECT COALESCE(total_cost_usd, 0) AS total_cost FROM ` + lookup.table + `
          WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?`
			args := []any{input.systemAccountID, input.scopeType, input.scopeID}
			if lookup.column != "" {
				query += ` AND ` + lookup.column + ` = ?`
				args = append(args, lookup.value)
			}
			var cost any
			err := l.statsDB.QueryRowContext(ctx, query, args...).Scan(&cost)
			if err != nil {
				if err == sql.ErrNoRows {
					continue
				}
				return nil, err
			}
			switch {
			case lookup.table == l.statsTable("usage_stats_totals"):
				costs.Total = numberValue(cost)
			case lookup.column == "stat_date":
				costs.Daily = numberValue(cost)
			case lookup.column == "stat_week":
				costs.Weekly = numberValue(cost)
			case lookup.column == "stat_month":
				costs.Monthly = numberValue(cost)
			case lookup.column == "window_hours":
				costs.Hourly = numberValue(cost)
			}
		}
		output[key] = costs
	}
	return output, nil
}

func quotaCostKey(systemAccountID, scopeType, scopeID string, hourlyHours *int) string {
	hours := ""
	if hourlyHours != nil {
		hours = fmt.Sprintf("%d", *hourlyHours)
	}
	return strings.Join([]string{systemAccountID, scopeType, scopeID, hours}, "\x00")
}

// quotaResetAtOf 对齐 requestQuotaResetAt（hourly/daily/weekly/monthly 窗口
// 最早重置点）。
func quotaResetAtOf(limits quotaLimits, costs quotaCosts, now time.Time, timezone *time.Location) string {
	var resets []string
	if limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit {
		resets = append(resets, nextZonedHourBoundary(now, timezone))
	}
	if limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit {
		resets = append(resets, startOfZonedDateKey(usageStatDate(now.AddDate(0, 0, 1), timezone), timezone))
	}
	if limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit {
		resets = append(resets, startOfZonedDateKey(usageStatDate(now.AddDate(0, 0, 7), timezone), timezone))
	}
	if limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit {
		resets = append(resets, startOfNextZonedMonth(now, timezone))
	}
	if len(resets) == 0 {
		return ""
	}
	sort.Strings(resets)
	return resets[0]
}

func nextZonedHourBoundary(now time.Time, timezone *time.Location) string {
	zoned := now.In(timezone)
	boundary := time.Date(zoned.Year(), zoned.Month(), zoned.Day(), zoned.Hour(), 0, 0, 0, timezone).Add(time.Hour)
	return boundary.UTC().Format(time.RFC3339Nano)
}

func startOfZonedDateKey(dateKey string, timezone *time.Location) string {
	parsed, err := time.ParseInLocation("2006-01-02", dateKey, timezone)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func startOfNextZonedMonth(now time.Time, timezone *time.Location) string {
	zoned := now.In(timezone)
	year, month, _ := zoned.Date()
	if month == 12 {
		return time.Date(year+1, 1, 1, 0, 0, 0, 0, timezone).UTC().Format(time.RFC3339Nano)
	}
	return time.Date(year, month+1, 1, 0, 0, 0, 0, timezone).UTC().Format(time.RFC3339Nano)
}

// usageStatDate/WeekKey/MonthKey 对齐 usage-stats-helpers（jobs 侧独立实现，
// 与 statsagg/timekeys.go 同算法）。
func usageStatDate(now time.Time, timezone *time.Location) string {
	zoned := now.In(timezone)
	return fmt.Sprintf("%04d-%02d-%02d", zoned.Year(), int(zoned.Month()), zoned.Day())
}

func usageWeekKey(now time.Time, timezone *time.Location) string {
	zoned := now.In(timezone)
	weekday := time.Date(zoned.Year(), zoned.Month(), zoned.Day(), 12, 0, 0, 0, time.UTC).Weekday()
	daysSinceMonday := (int(weekday) + 6) % 7
	weekStart := time.Date(zoned.Year(), zoned.Month(), zoned.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	return fmt.Sprintf("%04d-%02d-%02d", weekStart.Year(), int(weekStart.Month()), weekStart.Day())
}

func usageMonthKey(now time.Time, timezone *time.Location) string {
	zoned := now.In(timezone)
	return fmt.Sprintf("%04d-%02d", zoned.Year(), int(zoned.Month()))
}

// loadTeamLimitJSON 对齐 loadAccountStatusTeamLimitJsonAsync。
func (l *ProjectionItemLoader) loadTeamLimitJSON(ctx context.Context, rows []managementRow, now time.Time) (map[string]string, error) {
	output := map[string]string{}
	idSet := map[string]struct{}{}
	for i := range rows {
		row := rows[i]
		if isAuthorizedRow(row) && row.authorizationEffectiveSourceTeamID.Valid && row.authorizationEffectiveSourceTeamID.String != "" {
			idSet[row.authorizationID.String] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return output, nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	current := now.UTC().Format(time.RFC3339Nano)
	query := `
      SELECT authorizations.id AS authorization_id, grants.limits_json
      FROM ` + l.table("resource_authorizations") + ` authorizations
      INNER JOIN ` + l.table("resource_authorization_grants") + ` grants
        ON grants.resource_type = authorizations.resource_type
        AND grants.resource_id = authorizations.resource_id
        AND grants.grantee_type = 'team'
        AND grants.grantee_team_id = authorizations.effective_source_team_id
        AND grants.status = 'active'
        AND (grants.expires_at IS NULL OR grants.expires_at > ?)
      WHERE authorizations.status = 'active'
        AND (authorizations.expires_at IS NULL OR authorizations.expires_at > ?)
        AND authorizations.effective_source_team_id IS NOT NULL
        AND authorizations.id IN (` + placeholdersFor(len(ids)) + `)`
	args := []any{current, current}
	for _, id := range ids {
		args = append(args, id)
	}
	rowsQueried, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rowsQueried.Close()
	for rowsQueried.Next() {
		var authorizationID string
		var limitsJSON sql.NullString
		if err := rowsQueried.Scan(&authorizationID, &limitsJSON); err != nil {
			return nil, err
		}
		output[authorizationID] = limitsJSON.String
	}
	return output, rowsQueried.Err()
}

// ---- balance snapshot（account-balance.repository.ts）----

type balanceSnapshotRecord struct {
	snapshot         map[string]any
	snapshotRevision float64
	nextRefreshAfter string
	updatedAt        string
}

// loadBalanceSnapshotRecords 对齐 loadAccountBalanceSnapshotRecordsByAccountIdsAsync
// （juhe_stats.account_usage_snapshots kind='relay_balance'）。
func (l *ProjectionItemLoader) loadBalanceSnapshotRecords(ctx context.Context, accountIDs []string) (map[string]*balanceSnapshotRecord, error) {
	output := map[string]*balanceSnapshotRecord{}
	if len(accountIDs) == 0 {
		return output, nil
	}
	query := `
      SELECT account_id, snapshot_json, next_refresh_after, updated_at
      FROM ` + l.statsTable("account_usage_snapshots") + `
      WHERE kind = 'relay_balance' AND account_id IN (` + placeholdersFor(len(accountIDs)) + `)`
	args := make([]any, 0, len(accountIDs))
	for _, id := range accountIDs {
		args = append(args, id)
	}
	rows, err := l.statsDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var snapshotJSON string
		var nextRefreshAfter sql.NullString
		var updatedAt string
		if err := rows.Scan(&accountID, &snapshotJSON, &nextRefreshAfter, &updatedAt); err != nil {
			return nil, err
		}
		var snapshot map[string]any
		if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			return nil, fmt.Errorf("余额快照 snapshot_json 解析失败: %w", err)
		}
		record := &balanceSnapshotRecord{
			snapshot:         snapshot,
			nextRefreshAfter: nextRefreshAfter.String,
			updatedAt:        updatedAt,
		}
		if revision, ok := snapshot["configRevision"].(float64); ok {
			record.snapshotRevision = revision
		}
		output[accountID] = record
	}
	return output, rows.Err()
}

// balanceSnapshotMatchesConfiguration 对齐 accountBalanceSnapshotMatchesConfiguration
// （configRevision 相等 + nextRefreshAt/nextRefreshAfter 毫秒相等）。
func balanceSnapshotMatchesConfiguration(nextRefreshAt string, configRevision float64, record *balanceSnapshotRecord) bool {
	if record == nil {
		return false
	}
	if record.snapshotRevision != 0 && record.snapshotRevision != configRevision {
		return false
	}
	if nextRefreshAt == "" || record.nextRefreshAfter == "" {
		return nextRefreshAt == "" && record.nextRefreshAfter == ""
	}
	configured, okConfigured := rfc3339Millis(nextRefreshAt)
	persisted, okPersisted := rfc3339Millis(record.nextRefreshAfter)
	return okConfigured && okPersisted && configured == persisted
}

// balanceForListPayload 对齐 accountBalanceSnapshotForList（去 keyBalances）。
func balanceForListPayload(snapshot map[string]any) map[string]any {
	payload := make(map[string]any, len(snapshot))
	for key, value := range snapshot {
		if key == "keyBalances" {
			continue
		}
		payload[key] = value
	}
	return payload
}

// ---- apiKeyRuntime 汇总（account-api-key-runtime-state.repository.ts）----

type apiKeyRuntimeSummary struct {
	Total                int
	Active               int
	TemporaryUnavailable int
	RateLimited          int
	Error                int
	Disabled             int
	Unavailable          int
	AllUnavailable       bool
	NextProbeAt          string
	LastFailureAt        string
	LastErrorCode        string
	LastErrorMessage     string
	LastTraceID          string
}

// publicPayload 对齐 publicAccountApiKeyRuntimeSummary。
func (s *apiKeyRuntimeSummary) publicPayload() map[string]any {
	payload := map[string]any{
		"total":                s.Total,
		"active":               s.Active,
		"temporaryUnavailable": s.TemporaryUnavailable,
		"rateLimited":          s.RateLimited,
		"error":                s.Error,
		"disabled":             s.Disabled,
		"unavailable":          s.Unavailable,
		"allUnavailable":       s.AllUnavailable,
	}
	if s.NextProbeAt != "" {
		payload["nextProbeAt"] = s.NextProbeAt
	}
	if s.LastFailureAt != "" {
		payload["lastFailureAt"] = s.LastFailureAt
		payload["lastErrorCode"] = s.LastErrorCode
		payload["lastErrorMessage"] = s.LastErrorMessage
		payload["lastTraceId"] = s.LastTraceID
	}
	return payload
}

// loadAPIKeyRuntimeSummaries 对齐 loadAccountApiKeyRuntimeSummariesByAccountIdsAsync：
// 解密凭据 → key 池隔离（api_key 类型 + 支持供应商 + key>1）→ 按 fingerprint
// 对齐运行态 → 公共汇总。
func (l *ProjectionItemLoader) loadAPIKeyRuntimeSummaries(ctx context.Context, accountIDs []string) (map[string]*apiKeyRuntimeSummary, error) {
	output := map[string]*apiKeyRuntimeSummary{}
	ids, err := normalizedIDList(accountIDs)
	if err != nil || len(ids) == 0 {
		return output, err
	}
	for _, accountID := range ids {
		var (
			credentialsEncrypted string
			providerCode         string
			protocolCode         string
			protocolVersion      string
			accountType          string
		)
		err := l.db.QueryRowContext(ctx, `
        SELECT a.credentials_encrypted, COALESCE(src.provider_code, a.provider_code),
          COALESCE(src.protocol_code, a.protocol_code), COALESCE(src.protocol_version, a.protocol_version),
          COALESCE(src.type, a.type)
        FROM `+l.table("accounts")+` a
        LEFT JOIN `+l.table("accounts")+` src ON src.id = a.authorization_instance_source_account_id AND src.deleted_at IS NULL
        WHERE a.id = ? AND a.deleted_at IS NULL`, accountID).Scan(
			&credentialsEncrypted, &providerCode, &protocolCode, &protocolVersion, &accountType)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		credentials, err := l.credentials.DecryptCredentials(credentialsEncrypted)
		if err != nil {
			// Node decryptJson 失败 continue（该账户无汇总）。
			continue
		}
		if !apiKeyPoolIsolationEnabled(l.credentials, accountType, providerCode, protocolCode, protocolVersion, credentials) {
			continue
		}
		entries := l.credentials.AccountAPIKeyEntries(credentials)
		if len(entries) < 2 {
			continue
		}
		fingerprints := make([]string, 0, len(entries))
		for _, entry := range entries {
			fingerprints = append(fingerprints, entry.Fingerprint)
		}
		states, err := l.loadAPIKeyRuntimeStates(ctx, fingerprints)
		if err != nil {
			return nil, err
		}
		summary := &apiKeyRuntimeSummary{Total: len(entries)}
		var latestFailure *apiKeyRuntimeStateRow
		for _, entry := range entries {
			state, found := states[entry.Fingerprint]
			if !found || state.status == "active" {
				summary.Active++
				continue
			}
			summary.Unavailable++
			switch state.status {
			case "temporary_unavailable":
				summary.TemporaryUnavailable++
			case "rate_limited":
				summary.RateLimited++
			case "error":
				summary.Error++
			case "disabled":
				summary.Disabled++
			}
			if state.nextProbeAt != "" && apiKeyRuntimeProbeCandidateStatus(state.status) &&
				(summary.NextProbeAt == "" || state.nextProbeAt < summary.NextProbeAt) {
				summary.NextProbeAt = state.nextProbeAt
			}
			if state.lastFailureAt != "" && (latestFailure == nil ||
				state.lastFailureAt > latestFailure.lastFailureAt ||
				(state.lastFailureAt == latestFailure.lastFailureAt && entry.Index < latestFailure.index)) {
				copied := state
				copied.index = entry.Index
				latestFailure = &copied
			}
		}
		summary.AllUnavailable = summary.Unavailable == summary.Total
		if latestFailure != nil {
			summary.LastFailureAt = latestFailure.lastFailureAt
			summary.LastErrorCode = latestFailure.lastErrorCode
			summary.LastErrorMessage = latestFailure.lastErrorMessage
			summary.LastTraceID = latestFailure.lastTraceID
		}
		output[accountID] = summary
	}
	return output, nil
}

type apiKeyRuntimeStateRow struct {
	status          string
	nextProbeAt     string
	lastFailureAt   string
	lastErrorCode   string
	lastErrorMessage string
	lastTraceID     string
	index           int
}

func (l *ProjectionItemLoader) loadAPIKeyRuntimeStates(ctx context.Context, fingerprints []string) (map[string]apiKeyRuntimeStateRow, error) {
	states := map[string]apiKeyRuntimeStateRow{}
	if len(fingerprints) == 0 {
		return states, nil
	}
	query := `
      SELECT key_fingerprint, status, next_probe_at, last_failure_at, last_error_code, last_error_message, last_error_trace_id
      FROM ` + l.table("account_api_key_runtime_states") + `
      WHERE key_fingerprint IN (` + placeholdersFor(len(fingerprints)) + `)`
	args := make([]any, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		args = append(args, fingerprint)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			fingerprint    string
			status         sql.NullString
			nextProbe      sql.NullString
			lastFailure    sql.NullString
			lastErrorCode  sql.NullString
			lastErrorMessage sql.NullString
			lastTraceID    sql.NullString
		)
		if err := rows.Scan(&fingerprint, &status, &nextProbe, &lastFailure, &lastErrorCode, &lastErrorMessage, &lastTraceID); err != nil {
			return nil, err
		}
		states[fingerprint] = apiKeyRuntimeStateRow{
			status:           status.String,
			nextProbeAt:      nextProbe.String,
			lastFailureAt:    lastFailure.String,
			lastErrorCode:    lastErrorCode.String,
			lastErrorMessage: lastErrorMessage.String,
			lastTraceID:      lastTraceID.String,
		}
	}
	return states, rows.Err()
}

// apiKeyRuntimeProbeCandidateStatus 对齐 isAccountApiKeyRuntimeProbeCandidateStatus。
func apiKeyRuntimeProbeCandidateStatus(status string) bool {
	switch status {
	case "temporary_unavailable", "rate_limited", "error":
		return true
	}
	return false
}

// apiKeyPoolIsolationEnabled 对齐 isAccountApiKeyPoolIsolationEnabled（含
// provider 支持面：openai/gpt、deepseek、glm、gemini、anthropic 协议或代码）。
func apiKeyPoolIsolationEnabled(codec CredentialCodec, accountType, providerCode, protocolCode, protocolVersion string, credentials map[string]any) bool {
	if accountType != "api_key" {
		return false
	}
	if len(codec.AccountAPIKeyEntries(credentials)) <= 1 {
		return false
	}
	normalizedProvider := normalizeProviderToken(providerCode)
	switch normalizedProvider {
	case "openai", "gpt", "deepseek", "glm", "gemini", "anthropic":
		return true
	}
	if normalizeProviderToken(protocolCode) == "anthropic" && normalizeProviderToken(protocolVersion) == "v1" {
		return true
	}
	return false
}

func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ---- circuit incidents → 公共摘要（control-plane repository + bridge reducer）----

type publicCircuitSummary struct {
	Status      string
	Reason      string
	Since       string
	NextCheckAt string
}

type circuitIncidentRecord struct {
	accountRuntimeKey  string
	state              string
	lastFailureClass   string
	updatedAtMS        int64
	nextTransitionAtMS *int64
}

// loadCircuitSummaries 对齐 loadProjectedAccountListItems 的 loadCircuitSummaries：
// listAccountCircuitIncidentsByRuntimeKeysInClient + publicAccountCircuitSummariesFromIncidents。
func (l *ProjectionItemLoader) loadCircuitSummaries(ctx context.Context, runtimeKeys []string, now time.Time) (map[string]publicCircuitSummary, error) {
	output := map[string]publicCircuitSummary{}
	keys, err := normalizedIDList(runtimeKeys)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return output, nil
	}
	if len(keys) > 100 {
		return nil, fmt.Errorf("账户 circuit 摘要单次最多查询 100 个运行态键")
	}
	incidents, err := l.listCircuitIncidents(ctx, keys, now)
	if err != nil {
		return nil, err
	}
	grouped := map[string][]circuitIncidentRecord{}
	for _, incident := range incidents {
		grouped[incident.accountRuntimeKey] = append(grouped[incident.accountRuntimeKey], incident)
	}
	for _, key := range keys {
		output[key] = publicCircuitSummaryOf(grouped[key])
	}
	return output, nil
}

func (l *ProjectionItemLoader) listCircuitIncidents(ctx context.Context, keys []string, now time.Time) ([]circuitIncidentRecord, error) {
	query := `
    SELECT circuit_incident.account_runtime_key, circuit_incident.state,
      circuit_incident.last_failure_class, circuit_incident.updated_at_ms,
      circuit_incident.next_transition_at_ms
    FROM ` + l.table("account_circuit_incidents") + ` circuit_incident
    WHERE circuit_incident.account_runtime_key IN (` + placeholdersFor(len(keys)) + `)
      AND circuit_incident.state <> 'CLOSED'
      AND circuit_incident.dispatch_revision = (
        SELECT current_account.dispatch_revision
        FROM ` + l.table("accounts") + ` current_account
        WHERE current_account.id = circuit_incident.account_id
          AND current_account.deleted_at IS NULL
      )
    ORDER BY circuit_incident.account_runtime_key ASC, circuit_incident.updated_at_ms ASC, circuit_incident.circuit_scope_key ASC`
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	incidents := []circuitIncidentRecord{}
	for rows.Next() {
		var (
			record           circuitIncidentRecord
			lastFailureClass sql.NullString
			nextTransitionAt sql.NullInt64
		)
		if err := rows.Scan(&record.accountRuntimeKey, &record.state, &lastFailureClass, &record.updatedAtMS, &nextTransitionAt); err != nil {
			return nil, err
		}
		record.lastFailureClass = lastFailureClass.String
		if nextTransitionAt.Valid {
			value := nextTransitionAt.Int64
			record.nextTransitionAtMS = &value
		}
		incidents = append(incidents, record)
	}
	return incidents, rows.Err()
}

// publicCircuitSummaryOf 对齐 publicAccountCircuitSummary：按 state 优先级
// （OPEN/PERSISTING/SHADOWED_BY_PERSISTENT > HALF_OPEN/SUSPECT > RECOVERING
// > 其他）取最高、同优先级取 updatedAt 最早的一条，nextCheckAt 取全部
// incidents 最早 next_transition_at_ms。
func publicCircuitSummaryOf(incidents []circuitIncidentRecord) publicCircuitSummary {
	if len(incidents) == 0 {
		return publicCircuitSummary{Status: "normal"}
	}
	incidentPriority := func(state string) int {
		switch state {
		case "OPEN", "PERSISTING", "SHADOWED_BY_PERSISTENT":
			return 3
		case "HALF_OPEN", "SUSPECT":
			return 2
		case "RECOVERING":
			return 1
		}
		return 0
	}
	selected := incidents[0]
	for _, incident := range incidents[1:] {
		left, right := incidentPriority(incident.state), incidentPriority(selected.state)
		if left > right || (left == right && incident.updatedAtMS < selected.updatedAtMS) {
			selected = incident
		}
	}
	status := "verifying"
	switch selected.state {
	case "OPEN", "PERSISTING", "SHADOWED_BY_PERSISTENT":
		status = "avoided"
	case "RECOVERING":
		status = "recovering"
	}
	summary := publicCircuitSummary{
		Status: status,
		Reason: selected.lastFailureClass,
		Since:  millisToISO(selected.updatedAtMS),
	}
	var nextCheckMS *int64
	for _, incident := range incidents {
		if incident.nextTransitionAtMS == nil {
			continue
		}
		if nextCheckMS == nil || *incident.nextTransitionAtMS < *nextCheckMS {
			copied := *incident.nextTransitionAtMS
			nextCheckMS = &copied
		}
	}
	if nextCheckMS != nil {
		summary.NextCheckAt = millisToISO(*nextCheckMS)
	}
	return summary
}

func millisToISO(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

var _ = oauthrefresh.ParseScheduleJSON
