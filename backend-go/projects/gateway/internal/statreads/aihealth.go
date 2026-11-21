package statreads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// AI-health read family (Node account-health-monitor.repository.ts): the
// hourly status strip per account from account_health_hourly is the primary
// read surface, optionally gap-filled from the J1 jobs outcome store
// (account_health_outcomes). The J1 read only covers contiguous missing-hour
// ranges plus a recent freshness overlap, followed by one latest-quality probe
// for accounts whose ranges do not span the full window; the per-hour
// latest-wins merge lives in j1OutcomeHealthRows / newestJ1OutcomeHourRow.

var statHourPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3])$`)

// aiHealthAccountNameMaxLength 是 AI 健康列表名称关键字参与匹配的最大
// rune 数（与账户名称上限同口径）；超长关键字按空集处理而非报错。
const aiHealthAccountNameMaxLength = 128

// HealthOutcomeSource mirrors accountHealthJobsOutcomeStoreSource: the
// jobs-owned outcome store the gateway only reads. It points at the jobs
// database URL (outcomePostgresUrl) and reads juhe_jobs.account_health_outcomes
// only for the missing-hour ranges plus the recent freshness overlap, and for
// one latest quality row per account whose ranges do not span the full window.
// The merge semantics
// (j1OutcomeHealthRows / newestJ1OutcomeHourRow) are source-independent. An
// absent/unconfigured source keeps the merge absent exactly like Node.
type HealthOutcomeSource struct {
	PostgresURL string
}

func (d *Deps) aiHealthListHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		// Node aiHealthQuerySchema (stats.routes.ts:102-107): integer hours
		// 1..31*24, page >= 1, pageSize 10..50, keyword trim + max 200; any
		// violation answers 400 instead of clamping.
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
		keyword, keywordTooLong := boundedKeyword(values, "keyword")
		if keywordTooLong {
			kernel.WriteBadRequest(w, "AI 健康监控参数不合法")
			return
		}
		// M3 域筛选（计划 §M3 配套适配）：可选 domain 四枚举；缺省/"all" =
		// 不过滤（默认行为零变化），非法值 400。
		capabilityDomain, domainOK := optionalCapabilityDomainFilter(values.Get(capabilityDomainFilterParam))
		if !domainOK {
			kernel.WriteBadRequest(w, "AI 健康监控参数不合法")
			return
		}
		scope, ok := d.requireReadScope(w, r, selfOnly)
		if !ok {
			return
		}
		payload, readErr := d.aiHealthList(r, scope, aiHealthOptions{
			Hours: hours, Page: page, PageSize: pageSize, Keyword: keyword,
			Domain: capabilityDomain,
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
		scope, ok := d.requireReadScope(w, r, selfOnly)
		if !ok {
			return
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

// boundedQueryInt mirrors the zod `z.coerce.number().int().min().max()`
// optional query schema: absent or empty keeps the fallback; a non-integer or
// out-of-range value is reported as a bad request instead of clamping
// (Node aiHealthQuerySchema, stats.routes.ts:102-107).
func boundedQueryInt(values urlValues, key string, fallback, minValue, maxValue int) (int, string) {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return fallback, ""
	}
	parsed, err := parseAlphaInt(raw)
	if err != nil {
		return fallback, "invalid"
	}
	if parsed < minValue || parsed > maxValue {
		return fallback, "range"
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
	// Domain 是可选能力域筛选（M3）：空 = 不过滤；非空 = 仅该域账户。
	Domain string
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
	j1Outcomes, err := d.j1OutcomesForAccountPage(r.Context(), accountIds, now, options.Hours, hourBuckets, rows, location)
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
	// J1 outcome merge for the same hour: only the target hour's quality
	// outcomes are read, never the full retention window.
	outcomes, err := d.j1OutcomesForHour(r.Context(), accountID, statHour, location)
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
		// 作用域条件只在上面追加一次；这里只返回名称包含条件，避免重复绑定。
		filterSQL, filterParams := d.aiHealthAccountNameContainsFilter(options.Keyword)
		clauses = append(clauses, filterSQL)
		params = append(params, filterParams...)
	}
	// M3 域筛选：读自身行 capability_domain（授权实例域恒等于源域，写路径
	// 继承保证），与关键词条件叠加（AND 语义）。
	if options.Domain != "" {
		clauses = append(clauses, capabilityDomainClause("accounts"))
		params = append(params, options.Domain)
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
			COALESCE(source_accounts.vendor_code, accounts.vendor_code, '') AS provider_code,
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
			accounts.next_health_check_at,
			-- 后台自动探测总开关（总开关设计 §9 健康监控「手动」标记）：实例行
			-- 跟随来源账户值，来源缺失保留自身行值，存量 NULL 兜底 1。
			COALESCE(source_accounts.background_probe_enabled, accounts.background_probe_enabled, 1) AS background_probe_enabled
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
			ID:                     row.text("id"),
			SystemAccountName:      row.nullText("system_account_name"),
			ProviderCode:           row.text("provider_code"),
			Name:                   row.text("name"),
			Status:                 row.text("status"),
			LastHealthCheckAt:      row.nullText("last_health_check_at"),
			LastHealthSuccessAt:    row.nullText("last_health_success_at"),
			NextHealthCheckAt:      row.nullText("next_health_check_at"),
			BackgroundProbeEnabled: row.text("background_probe_enabled") == "1",
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

// aiHealthAccountNameContainsFilter 直读 accounts.name 做包含匹配（账户
// 名称搜索去索引化）：`C` collation 与 pg_trgm 索引同形，ILIKE 提供 ASCII
// 大小写不敏感语义，`%` / `_` / `\` 由 postgresSubstringLikePattern 转义为
// 字面字符。空关键字或超过 128 rune 的关键字保持恒假空集语义。作用域条件
// 由 loadAiHealthAccountPage 统一追加一次，本函数只返回名称条件。
func (d *Deps) aiHealthAccountNameContainsFilter(keyword string) (string, []any) {
	trimmed := strings.TrimSpace(keyword)
	if trimmed == "" || len([]rune(trimmed)) > aiHealthAccountNameMaxLength {
		return "0 = 1", nil
	}
	return `accounts.name COLLATE "C" ILIKE ? ESCAPE '\'`,
		[]any{"%" + postgresSubstringLikePattern(trimmed) + "%"}
}

// ---------------------------------------------------------------------------
// J1 outcome merge (source-independent; the only configured source is the
// jobs-owned PostgreSQL outcome store).
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

// j1OutcomeRange is one per-account J1 read range. The ranges are the
// contiguous missing-hour runs plus the recent freshness overlap; rows outside
// them are covered by the hourly strip.
type j1OutcomeRange struct {
	AccountID      string
	ObservedAfter  string
	ObservedBefore string
}

func (d *Deps) j1OutcomesForAccountPage(ctx context.Context, accountIds []string, now time.Time, hours int, hourBuckets []string, hourlyRows []Row, location *time.Location) ([]j1Outcome, error) {
	// 空 URL 与 nil 同义：未配置的源保持合并缺席（Node absent-source 语义），
	// 不得下探 sql.Open("")——那会拨驱动默认地址并把整个读面打成 500（B5）。
	if d.HealthOutcomes == nil || d.HealthOutcomes.PostgresURL == "" || len(accountIds) == 0 || len(hourBuckets) == 0 {
		return nil, nil
	}
	windowStart := j1ObservedAfter(now, hours)
	windowEnd := j1WindowEnd(now)
	freshnessFloor := now.Add(-j1ListFreshnessOverlap).UTC().Format("2006-01-02T15:04:05.000Z")
	ranges := j1OutcomeRangesForHourlyRows(accountIds, hourBuckets, hourlyRows, windowStart, windowEnd, freshnessFloor, location)
	if len(ranges) == 0 {
		return nil, nil
	}
	return d.j1PostgresOutcomes(ctx, ranges, windowStart, windowEnd)
}

// j1ListFreshnessOverlap keeps the most recent two hours in the J1 read even
// when the hourly strip already covers them. The hourly projection is
// near-real-time but not synchronous; the overlap absorbs late outcomes and
// projection lag without returning to a full-window scan.
const j1ListFreshnessOverlap = 2 * time.Hour

// j1PostgresOutcomeRangeLimit bounds one gap query's VALUES list. Larger
// pages are split into multiple chunks instead of falling back to a
// full-window read, so a page with many isolated missing hours keeps the
// incremental read shape.
const j1PostgresOutcomeRangeLimit = 2048

// j1ObservedAfter is the full list-window lower bound, retaining the old
// reader's +2h margin for timezone and projection lag.
func j1ObservedAfter(now time.Time, hours int) string {
	return time.UnixMilli(now.UnixMilli() - int64(hours+2)*int64(time.Hour/time.Millisecond)).UTC().Format("2006-01-02T15:04:05.000Z")
}

// j1WindowEnd mirrors the old reader's open upper end with a +2h margin; the
// hourly strip's own bucket filter discards anything beyond the requested
// window.
func j1WindowEnd(now time.Time) string {
	return now.Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
}

// j1OutcomeRangesForHourlyRows treats account_health_hourly as the primary
// strip and computes per-account J1 gaps: each contiguous missing-hour run plus
// the freshness range from the earlier of the newest hourly observation and
// the two-hour floor. Accounts without hourly rows fall back to the full
// window; an account with a malformed hourly timestamp also falls back so a
// bad bound can never hide a newer J1 quality outcome.
func j1OutcomeRangesForHourlyRows(accountIds []string, hourBuckets []string, hourlyRows []Row, windowStart, windowEnd, freshnessFloor string, location *time.Location) []j1OutcomeRange {
	if len(accountIds) == 0 || len(hourBuckets) == 0 {
		return nil
	}
	present := make(map[string]map[string]bool, len(accountIds))
	latest := make(map[string]string, len(accountIds))
	invalidObserved := make(map[string]bool, len(accountIds))
	for _, row := range hourlyRows {
		accountID := row.text("account_id")
		if accountID == "" {
			continue
		}
		if present[accountID] == nil {
			present[accountID] = make(map[string]bool, len(hourBuckets))
		}
		present[accountID][row.text("stat_hour")] = true
		observed, ok := normalizeJ1ObservedAfter(row.text("last_observed_at"))
		if !ok {
			invalidObserved[accountID] = true
			continue
		}
		if timestampValue(observed) > timestampValue(latest[accountID]) {
			latest[accountID] = observed
		}
	}
	ranges := make([]j1OutcomeRange, 0, len(accountIds))
	seen := make(map[string]bool, len(accountIds))
	for _, accountID := range accountIds {
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		hoursPresent := present[accountID]
		if len(hoursPresent) == 0 {
			ranges = append(ranges, j1OutcomeRange{AccountID: accountID, ObservedAfter: windowStart, ObservedBefore: windowEnd})
			continue
		}
		accountRanges := make([]j1OutcomeRange, 0, 2)
		if invalidObserved[accountID] {
			accountRanges = append(accountRanges, j1OutcomeRange{AccountID: accountID, ObservedAfter: windowStart, ObservedBefore: windowEnd})
		} else {
			runStart, runEnd := "", ""
			flushRun := func() {
				if runStart == "" {
					return
				}
				accountRanges = append(accountRanges, j1OutcomeRange{AccountID: accountID, ObservedAfter: runStart, ObservedBefore: runEnd})
				runStart, runEnd = "", ""
			}
			for _, hour := range hourBuckets {
				if hoursPresent[hour] {
					flushRun()
					continue
				}
				start := hourBucketStartUTC(hour, location)
				end := hourBucketEndUTC(hour, location)
				if start == "" || end == "" {
					continue
				}
				if runStart == "" {
					runStart, runEnd = start, end
					continue
				}
				if timestampValue(start) <= timestampValue(runEnd) {
					if timestampValue(end) > timestampValue(runEnd) {
						runEnd = end
					}
					continue
				}
				flushRun()
				runStart, runEnd = start, end
			}
			flushRun()
			freshnessStart := earlierJ1Timestamp(latest[accountID], freshnessFloor)
			if freshnessStart == "" || timestampValue(freshnessStart) < timestampValue(windowStart) {
				freshnessStart = windowStart
			}
			if timestampValue(freshnessStart) < timestampValue(windowEnd) {
				accountRanges = append(accountRanges, j1OutcomeRange{AccountID: accountID, ObservedAfter: freshnessStart, ObservedBefore: windowEnd})
			}
		}
		ranges = append(ranges, mergeJ1OutcomeRanges(accountRanges)...)
	}
	return ranges
}

func mergeJ1OutcomeRanges(ranges []j1OutcomeRange) []j1OutcomeRange {
	if len(ranges) <= 1 {
		return ranges
	}
	sort.SliceStable(ranges, func(left, right int) bool {
		return timestampValue(ranges[left].ObservedAfter) < timestampValue(ranges[right].ObservedAfter)
	})
	merged := make([]j1OutcomeRange, 0, len(ranges))
	for _, current := range ranges {
		if len(merged) == 0 {
			merged = append(merged, current)
			continue
		}
		last := &merged[len(merged)-1]
		if timestampValue(current.ObservedAfter) <= timestampValue(last.ObservedBefore) {
			if timestampValue(current.ObservedBefore) > timestampValue(last.ObservedBefore) {
				last.ObservedBefore = current.ObservedBefore
			}
			continue
		}
		merged = append(merged, current)
	}
	return merged
}

// normalizeJ1ObservedAfter renders any RFC3339 instant as the millisecond UTC
// form used by both the hourly table and the J1 reader. Invalid legacy text is
// ignored by the caller, which then falls back to a wider lower bound instead
// of failing the page.
func normalizeJ1ObservedAfter(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), true
}

// localHourBoundsUTC returns the half-open UTC range covering every instant
// whose local hour key equals statHour. DST transitions make the mapping
// non-trivial: a fall-back hour can occur twice (or for 30/45-minute shifts,
// in two segments), a spring-forward hour can be absent or start at :30/:45.
// The fast path is the ordinary one-hour case; around a transition a bounded
// minute scan finds the first and last matching instant and lets the existing
// hourKeyIn merge filter discard any adjacent-hour rows read by the wider
// range.
const (
	localHourScanRadius  = 4 * time.Hour
	localHourScanStep    = time.Minute
	minLocalStatHourYear = 1970
	maxLocalStatHourYear = 9998
)

func localHourBoundsUTC(statHour string, location *time.Location) (time.Time, time.Time, bool) {
	if location == nil {
		location = time.UTC
	}
	parsed, err := time.Parse("2006-01-02T15", statHour)
	if err != nil || parsed.Year() < minLocalStatHourYear || parsed.Year() > maxLocalStatHourYear {
		return time.Time{}, time.Time{}, false
	}
	start := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), 0, 0, 0, location)
	end := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour()+1, 0, 0, 0, location)
	if start.Minute() == 0 && start.Second() == 0 && end.Minute() == 0 && end.Second() == 0 {
		nextHour := parsed.Add(time.Hour).Format("2006-01-02T15")
		_, startOffset := start.Zone()
		_, endOffset := end.Zone()
		_, nextOffset := end.Add(time.Hour).Zone()
		if hourKeyIn(start, location) == statHour && hourKeyIn(end, location) == nextHour &&
			startOffset == endOffset && endOffset == nextOffset {
			return start, end, true
		}
	}
	var first, last time.Time
	scanEnd := start.Add(localHourScanRadius)
	for candidate := start.Add(-localHourScanRadius); !candidate.After(scanEnd); candidate = candidate.Add(localHourScanStep) {
		if hourKeyIn(candidate, location) != statHour {
			continue
		}
		if first.IsZero() {
			first = candidate
		}
		last = candidate
	}
	if first.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return first, last.Add(localHourScanStep), true
}

func hourBucketStartUTC(statHour string, location *time.Location) string {
	start, _, ok := localHourBoundsUTC(statHour, location)
	if !ok {
		return ""
	}
	return start.UTC().Format("2006-01-02T15:04:05.000Z")
}

func hourBucketEndUTC(statHour string, location *time.Location) string {
	_, end, ok := localHourBoundsUTC(statHour, location)
	if !ok {
		return ""
	}
	return end.UTC().Format("2006-01-02T15:04:05.000Z")
}

func earlierJ1Timestamp(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	if timestampValue(right) < timestampValue(left) {
		return right
	}
	return left
}

// ---------------------------------------------------------------------------
// PostgreSQL outcome reader: one incremental ranged query for the page's
// missing hours plus one latest-quality probe per account; the per-hour
// latest-wins merge stays in the Go merge layer.
// ---------------------------------------------------------------------------

// j1PostgresOutcomeBudget mirrors the Node reader's 5s statement_timeout /
// query_timeout; one budget covers the connection and both list queries.
const j1PostgresOutcomeBudget = 5 * time.Second

const j1PostgresOutcomeAccountLimit = 50

// j1PostgresOutcomes reads the page's J1 gap ranges and one latest quality row
// for every account whose ranges did not already span the full window. The
// latest probe is a single backward index scan with LIMIT 1 per account; it is
// deliberately not skipped just because an earlier gap row exists, since the
// newest quality outcome may sit in a covered hour outside the read ranges.
func (d *Deps) j1PostgresOutcomes(ctx context.Context, ranges []j1OutcomeRange, windowStart, windowEnd string) ([]j1Outcome, error) {
	db, err := sql.Open("pgx", d.HealthOutcomes.PostgresURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	readCtx, cancel := context.WithTimeout(ctx, j1PostgresOutcomeBudget)
	defer cancel()
	outcomes := make([]j1Outcome, 0, len(ranges))
	for _, chunk := range j1OutcomeRangeChunks(ranges, j1PostgresOutcomeRangeLimit) {
		gapQuery, gapArgs, err := j1PostgresOutcomeGapQuery(chunk)
		if err != nil {
			return nil, err
		}
		gapRows, err := queryRowsContext(readCtx, db, gapQuery, gapArgs...)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, buildJ1ListOutcomeRows(gapRows)...)
	}
	latestAccountIds := j1AccountsNeedingLatestProbe(ranges, windowStart, windowEnd)
	if len(latestAccountIds) == 0 {
		return outcomes, nil
	}
	latestQuery, latestArgs, err := j1PostgresLatestQualityQuery(latestAccountIds, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}
	latestRows, err := queryRowsContext(readCtx, db, latestQuery, latestArgs...)
	if err != nil {
		// 表头探针失败不应丢弃已经读到的缺失小时数据；小时行仍可兜底表头。
		slog.Default().Warn("AI 健康最新质量 outcome 读取失败，表头回退小时行",
			"event", "ai_health_latest_probe_failed", "error", err)
		return outcomes, nil
	}
	return append(outcomes, buildJ1ListOutcomeRows(latestRows)...), nil
}

// j1OutcomeRangeChunks splits the page's ranges into SQL-sized chunks. The
// ranges stay incremental; unlike a full-window fallback, every chunk keeps
// the missing-hour shape.
func j1OutcomeRangeChunks(ranges []j1OutcomeRange, limit int) [][]j1OutcomeRange {
	if limit < 1 {
		limit = 1
	}
	chunks := make([][]j1OutcomeRange, 0, (len(ranges)+limit-1)/limit)
	for start := 0; start < len(ranges); start += limit {
		end := start + limit
		if end > len(ranges) {
			end = len(ranges)
		}
		chunks = append(chunks, ranges[start:end])
	}
	return chunks
}

// j1AccountsNeedingLatestProbe returns the accounts whose ranges did not span
// the full window. An earlier gap row does not prove the header's latest
// quality outcome is in the gap: a newer quality outcome may live in a covered
// hour between two ranges. Only a single range that covers both window bounds
// proves the gap read was exhaustive.
func j1AccountsNeedingLatestProbe(ranges []j1OutcomeRange, windowStart, windowEnd string) []string {
	fullWindow := make(map[string]bool, len(ranges))
	windowStartMillis := timestampValue(windowStart)
	windowEndMillis := timestampValue(windowEnd)
	for _, r := range ranges {
		if timestampValue(r.ObservedAfter) <= windowStartMillis && timestampValue(r.ObservedBefore) >= windowEndMillis {
			fullWindow[r.AccountID] = true
		}
	}
	seen := make(map[string]bool, len(ranges))
	missing := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if seen[r.AccountID] {
			continue
		}
		seen[r.AccountID] = true
		if fullWindow[r.AccountID] {
			continue
		}
		missing = append(missing, r.AccountID)
	}
	return missing
}

// j1PostgresOutcomeGapQuery reads only the per-account ranges above the hourly
// strip. The VALUES + LATERAL shape lets PostgreSQL use the
// (account_id, observed_at) covering index once per range, and the non-quality
// predicate keeps the scan index-only and the row count small.
func j1PostgresOutcomeGapQuery(ranges []j1OutcomeRange) (string, []any, error) {
	normalized, err := normalizeJ1OutcomeRanges(ranges)
	if err != nil {
		return "", nil, err
	}
	var values strings.Builder
	args := make([]any, 0, len(normalized)*3)
	for index, r := range normalized {
		if index > 0 {
			values.WriteString(",\n\t\t\t")
		}
		base := index*3 + 1
		fmt.Fprintf(&values, "($%d::text, $%d::timestamptz, $%d::timestamptz)", base, base+1, base+2)
		args = append(args, r.AccountID, r.ObservedAfter, r.ObservedBefore)
	}
	query := `
		SELECT o.outcome_id, o.account_id, o.outcome,
			to_char(timezone('UTC', o.observed_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at,
			to_char(timezone('UTC', o.next_due_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS next_due_at
		FROM (VALUES
			` + values.String() + `
		) AS b(account_id, observed_after, observed_before)
		CROSS JOIN LATERAL (
			SELECT outcome_id, account_id, outcome, observed_at, next_due_at
			FROM juhe_jobs.account_health_outcomes
			WHERE account_id = b.account_id
				AND observed_at >= b.observed_after
				AND observed_at < b.observed_before
				AND ` + j1NonQualityOutcomeSQLPredicate + `
		) AS o
		ORDER BY o.observed_at ASC, o.outcome_id ASC`
	return query, args, nil
}

func normalizeJ1OutcomeRanges(ranges []j1OutcomeRange) ([]j1OutcomeRange, error) {
	if len(ranges) == 0 {
		return nil, errors.New("J1 outcome 增量查询至少需要一个区间")
	}
	if len(ranges) > j1PostgresOutcomeRangeLimit {
		return nil, errors.New("J1 outcome 增量查询区间数超过上限")
	}
	byAccount := make(map[string][]j1OutcomeRange, len(ranges))
	order := make([]string, 0, len(ranges))
	for _, r := range ranges {
		accountID := strings.TrimSpace(r.AccountID)
		if accountID == "" {
			return nil, errors.New("J1 outcome 账户 ID 不能为空")
		}
		observedAfter, ok := normalizeJ1ObservedAfter(r.ObservedAfter)
		if !ok {
			return nil, errors.New("J1 outcome 区间起点必须是 RFC3339 时间")
		}
		observedBefore, ok := normalizeJ1ObservedAfter(r.ObservedBefore)
		if !ok {
			return nil, errors.New("J1 outcome 区间终点必须是 RFC3339 时间")
		}
		if timestampValue(observedAfter) >= timestampValue(observedBefore) {
			return nil, errors.New("J1 outcome 区间起点必须早于终点")
		}
		if _, exists := byAccount[accountID]; !exists {
			order = append(order, accountID)
		}
		byAccount[accountID] = append(byAccount[accountID], j1OutcomeRange{
			AccountID:      accountID,
			ObservedAfter:  observedAfter,
			ObservedBefore: observedBefore,
		})
	}
	normalized := make([]j1OutcomeRange, 0, len(ranges))
	for _, accountID := range order {
		normalized = append(normalized, mergeJ1OutcomeRanges(byAccount[accountID])...)
	}
	return normalized, nil
}

// j1PostgresLatestQualityQuery reads the newest quality outcome per account
// over the same half-open window as the gap read. It drives the header's
// last/next check fields without widening the strip read; the backward index
// scan stops at the first quality row per account.
func j1PostgresLatestQualityQuery(accountIds []string, observedAfter, observedBefore string) (string, []any, error) {
	if len(accountIds) == 0 {
		return "", nil, errors.New("J1 最新质量 outcome 查询至少需要一个账户")
	}
	if len(accountIds) > j1PostgresOutcomeAccountLimit {
		return "", nil, errors.New("J1 outcome 账户查询最多允许 50 个账户")
	}
	normalized := make([]string, 0, len(accountIds))
	seen := make(map[string]bool, len(accountIds))
	for _, accountID := range accountIds {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return "", nil, errors.New("J1 outcome 账户 ID 不能为空")
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		normalized = append(normalized, accountID)
	}
	windowStart, ok := normalizeJ1ObservedAfter(observedAfter)
	if !ok {
		return "", nil, errors.New("J1 outcome 窗口起点必须是 RFC3339 时间")
	}
	windowEnd, ok := normalizeJ1ObservedAfter(observedBefore)
	if !ok {
		return "", nil, errors.New("J1 outcome 窗口终点必须是 RFC3339 时间")
	}
	if timestampValue(windowEnd) <= timestampValue(windowStart) {
		return "", nil, errors.New("J1 outcome 窗口终点必须晚于起点")
	}
	var values strings.Builder
	args := make([]any, 0, len(normalized)+2)
	for index, accountID := range normalized {
		if index > 0 {
			values.WriteString(", ")
		}
		fmt.Fprintf(&values, "($%d::text)", index+1)
		args = append(args, accountID)
	}
	afterPlaceholder := fmt.Sprintf("$%d", len(normalized)+1)
	beforePlaceholder := fmt.Sprintf("$%d", len(normalized)+2)
	args = append(args, windowStart, windowEnd)
	query := `
		SELECT o.account_id, o.outcome_id, o.outcome,
			to_char(timezone('UTC', o.observed_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at,
			to_char(timezone('UTC', o.next_due_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS next_due_at
		FROM (VALUES
			` + values.String() + `
		) AS b(account_id)
		CROSS JOIN LATERAL (
			SELECT account_id, outcome_id, outcome, observed_at, next_due_at
			FROM juhe_jobs.account_health_outcomes
			WHERE account_id = b.account_id
				AND observed_at >= ` + afterPlaceholder + `::timestamptz
				AND observed_at < ` + beforePlaceholder + `::timestamptz
				AND ` + j1NonQualityOutcomeSQLPredicate + `
			ORDER BY observed_at DESC, outcome_id DESC
			LIMIT 1
		) AS o`
	return query, args, nil
}

// j1OutcomesForHour reads one target hour's quality outcomes for the detail
// endpoint; the old 31-day read is not repeated here.
func (d *Deps) j1OutcomesForHour(ctx context.Context, accountID, statHour string, location *time.Location) ([]j1Outcome, error) {
	if d.HealthOutcomes == nil || d.HealthOutcomes.PostgresURL == "" || accountID == "" || statHour == "" {
		return nil, nil
	}
	hourStart, hourEnd, ok := localHourBoundsUTC(statHour, location)
	if !ok {
		// 不存在的本地小时（spring-forward 跳过）或越界年份没有 J1 行；
		// 详情仍可用小时表展示，避免把无效输入打成 500。
		return nil, nil
	}
	query, args, err := j1PostgresOutcomeHourQuery(
		accountID,
		hourStart.UTC().Format("2006-01-02T15:04:05.000Z"),
		hourEnd.UTC().Format("2006-01-02T15:04:05.000Z"),
	)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", d.HealthOutcomes.PostgresURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	readCtx, cancel := context.WithTimeout(ctx, j1PostgresOutcomeBudget)
	defer cancel()
	rows, err := queryRowsContext(readCtx, db, query, args...)
	if err != nil {
		return nil, err
	}
	return buildJ1OutcomeRows(rows), nil
}

// j1PostgresOutcomeHourQuery keeps the detail's full entity projection
// (including the error columns) but bounds it to [hourStart, hourEnd).
func j1PostgresOutcomeHourQuery(accountID, hourStart, hourEnd string) (string, []any, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", nil, errors.New("J1 小时详情账户 ID 不能为空")
	}
	start, ok := normalizeJ1ObservedAfter(hourStart)
	if !ok {
		return "", nil, errors.New("J1 小时详情起点必须是 RFC3339 时间")
	}
	end, ok := normalizeJ1ObservedAfter(hourEnd)
	if !ok {
		return "", nil, errors.New("J1 小时详情终点必须是 RFC3339 时间")
	}
	query := `
		SELECT outcome_id, request_id, account_id, outcome,
			to_char(timezone('UTC', observed_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at,
			status_code::bigint AS status_code, error_code, error_message,
			to_char(timezone('UTC', next_due_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS next_due_at
		FROM juhe_jobs.account_health_outcomes
		WHERE account_id = $1::text
			AND observed_at >= $2::timestamptz
			AND observed_at < $3::timestamptz
			AND ` + j1NonQualityOutcomeSQLPredicate + `
		ORDER BY observed_at ASC, outcome_id ASC`
	return query, []any{accountID, start, end}, nil
}

// buildJ1OutcomeRows 从范围读取的实体列构造合并层输入；可空列（status_code、
// error_code、error_message、next_due_at）为 NULL 时保持 nil。不再有「坏
// payload 跳过」分支：字段全部来自有类型的实体列，无解码失败路径。
func buildJ1OutcomeRows(rows []Row) []j1Outcome {
	outcomes := make([]j1Outcome, 0, len(rows))
	for _, row := range rows {
		outcomes = append(outcomes, j1Outcome{
			OutcomeID:    row.text("outcome_id"),
			RequestID:    row.text("request_id"),
			AccountID:    row.text("account_id"),
			Outcome:      row.text("outcome"),
			ObservedAt:   row.text("storage_observed_at"),
			StatusCode:   row.nullNumber("status_code"),
			ErrorCode:    row.nullText("error_code"),
			ErrorMessage: row.nullText("error_message"),
			NextDueAt:    row.nullText("next_due_at"),
		})
	}
	return outcomes
}

// buildJ1ListOutcomeRows maps the narrow list projection. The strip merge and
// the header only need identity, outcome, observed time and next due; error
// columns stay exclusive to the detail endpoint.
func buildJ1ListOutcomeRows(rows []Row) []j1Outcome {
	outcomes := make([]j1Outcome, 0, len(rows))
	for _, row := range rows {
		outcomes = append(outcomes, j1Outcome{
			OutcomeID:  row.text("outcome_id"),
			AccountID:  row.text("account_id"),
			Outcome:    row.text("outcome"),
			ObservedAt: row.text("storage_observed_at"),
			NextDueAt:  row.nullText("next_due_at"),
		})
	}
	return outcomes
}

// j1NonQualityOutcomes 是不得计入 AI 健康页健康率的非质量终态
// （docs/functions/模型价格与用量统计口径.md §2）：它们只进入诊断和通用错误
// 聚合，不构成 AI 账户近期质量的正样本或负样本。映射回空串与 stale 同臂，
// 即该小时不计入 successHours/failureHours（无其它观测时页面显示 unknown）。
var j1NonQualityOutcomes = map[string]bool{
	"stale": true,
	// upstream_http_failure 是 J1 自 2026-11-04 起的当前结果名；
	// framing_complete_neutral 是同一类结果的历史别名，仅用于读取存量行。
	"upstream_http_failure":    true,
	"framing_complete_neutral": true,
	"opaque_upstream":          true,
	"gateway_capacity":         true,
	"gateway_policy":           true,
	"downstream_closed":        true,
}

// j1NonQualityOutcomeSQLPredicate is the SQL form of j1NonQualityOutcomes.
// Building it from the map keeps the row-reduction filter and the Go merge
// semantic authority in sync; it also guarantees the covering index's
// index-only scan never has to revive a non-quality row.
var j1NonQualityOutcomeSQLPredicate = buildJ1NonQualityOutcomeSQLPredicate()

func buildJ1NonQualityOutcomeSQLPredicate() string {
	names := make([]string, 0, len(j1NonQualityOutcomes))
	for name := range j1NonQualityOutcomes {
		names = append(names, name)
	}
	sort.Strings(names)
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, "'"+strings.ReplaceAll(name, "'", "''")+"'")
	}
	return "outcome NOT IN (" + strings.Join(quoted, ", ") + ")"
}

// j1OutcomeHealthStatus 把 J1 探针终态映射为健康率口径：complete_success 是
// 唯一正样本；非质量终态返回 ""（不计入）；其余终态（upstream_failure、
// probe_task_failure，以及带 account_upstream / account_dependency 归因的
// 真实账号失败）保持 failure。
func j1OutcomeHealthStatus(outcome j1Outcome) string {
	switch outcome.Outcome {
	case "complete_success":
		return "success"
	}
	if j1NonQualityOutcomes[outcome.Outcome] {
		return ""
	}
	return "failure"
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
		ID:                     account.ID,
		Name:                   account.Name,
		ProviderCode:           account.ProviderCode,
		Status:                 account.Status,
		SystemAccountName:      account.SystemAccountName,
		LatestStatus:           latestStatus,
		SuccessHours:           successHours,
		FailureHours:           failureHours,
		UnknownHours:           len(hours) - checkedHours,
		BackgroundProbeEnabled: account.BackgroundProbeEnabled,
		Hours:                  hours,
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
	ID                     string  `json:"id"`
	SystemAccountName      *string `json:"-"`
	ProviderCode           string  `json:"-"`
	Name                   string  `json:"-"`
	Status                 string  `json:"-"`
	LastHealthCheckAt      *string `json:"-"`
	LastHealthSuccessAt    *string `json:"-"`
	NextHealthCheckAt      *string `json:"-"`
	BackgroundProbeEnabled bool    `json:"-"`
}

type aiHealthAccountRow struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	ProviderCode        string   `json:"providerCode"`
	Status              string   `json:"status"`
	SystemAccountName   *string  `json:"systemAccountName,omitempty"`
	LastHealthCheckAt   *string  `json:"lastHealthCheckAt,omitempty"`
	LastHealthSuccessAt *string  `json:"lastHealthSuccessAt,omitempty"`
	NextHealthCheckAt   *string  `json:"nextHealthCheckAt,omitempty"`
	LatestStatus        string   `json:"latestStatus"`
	SuccessHours        int      `json:"successHours"`
	FailureHours        int      `json:"failureHours"`
	UnknownHours        int      `json:"unknownHours"`
	HealthRate          *float64 `json:"healthRate,omitempty"`
	// BackgroundProbeEnabled 回显后台自动探测总开关（总开关设计 §9）：健康页
	// 「手动」标记依据。恒输出。
	BackgroundProbeEnabled bool                `json:"backgroundProbeEnabled"`
	Hours                  []aiHealthHourPoint `json:"hours"`
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

// Status mirrors outcomeHealthStatus for the merged detail row.
func (o j1Outcome) Status() string { return j1OutcomeHealthStatus(o) }
