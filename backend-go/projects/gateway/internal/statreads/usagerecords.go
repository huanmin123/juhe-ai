package statreads

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Usage-records list family (Node usage-records.repository.ts
// listUsageRecordsAsync + usage-records.routes.ts). PostgreSQL reads
// juhe_usage.usage_records directly; SQLite walks the registered usage shard
// files through the usage-catalog registry and merges per-shard windows.

const (
	usageRecordDefaultPageSize    = 50
	usageRecordMaxPageSize        = 200
	usageRecordMaxListWindowRows  = 1001
	usageRecordKeywordMatchLimit  = 200
	usageRecordShardWindowMaxDays = 31
)

var usageRecordTrafficSourceSet = map[string]bool{
	"gateway": true, "manual_account_test": true, "account_health_check": true,
	"runtime_recovery_probe": true, "cooldown_retest": true, "hybrid_scoring": true,
	"hybrid_quality_scoring": true,
}

// allSystemAccountUnsupportedFilterKeys mirror usage-records.routes.ts.
var allSystemAccountUnsupportedFilterKeys = []string{
	"accountKeyword", "result", "statusCode", "clientIp", "groupId", "model",
	"traceId", "trafficSource", "startDate", "endDate",
}

var dateOnlyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (d *Deps) usageRecordsListHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		values := r.URL.Query()
		if scope.canAccessAll() && scope.scopedID() == "" && hasAllSystemAccountUnsupportedFilters(values) {
			kernel.WriteBadRequest(w, "请先选择系统账户后筛选")
			return
		}
		options, err := d.parseUsageRecordListOptions(r)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		payload, err := d.listUsageRecords(r, scope, options)
		d.writeSection(w, payload, err)
	}
}

// hasAllSystemAccountUnsupportedFilters mirrors
// hasAllSystemAccountUnsupportedFilters.
func hasAllSystemAccountUnsupportedFilters(values url.Values) bool {
	for _, key := range allSystemAccountUnsupportedFilterKeys {
		if optionalQueryText(values, key) != "" {
			return true
		}
	}
	sortBy := optionalQueryText(values, "sortBy")
	sortOrder := optionalQueryText(values, "sortOrder")
	return (sortBy != "" && sortBy != "createdAt") || (sortOrder != "" && sortOrder != "desc")
}

type usageRecordListOptions struct {
	Page           int
	PageSize       int
	SortOrder      string
	TraceID        string
	AccountKeyword string
	ClientIp       string
	Result         string
	StatusCode     int
	GroupID        string
	Model          string
	TrafficSource  string
	StartAt        string
	EndAt          string
}

func (d *Deps) parseUsageRecordListOptions(r *http.Request) (usageRecordListOptions, error) {
	values := r.URL.Query()
	options := usageRecordListOptions{}
	rawPage, hasPage := finiteNumberQueryValue(values, "page")
	if hasPage && rawPage == float64(int64(rawPage)) {
		options.Page = int(rawPage)
	}
	rawPageSize, hasPageSize := finiteNumberQueryValue(values, "pageSize")
	if hasPageSize && rawPageSize == float64(int64(rawPageSize)) {
		options.PageSize = int(rawPageSize)
	}
	rawStatusCode, hasStatusCode := finiteNumberQueryValue(values, "statusCode")
	if hasStatusCode && rawStatusCode == float64(int64(rawStatusCode)) && rawStatusCode >= 100 && rawStatusCode <= 599 {
		options.StatusCode = int(rawStatusCode)
	}
	options.TraceID = optionalQueryText(values, "traceId")
	options.AccountKeyword = optionalQueryText(values, "accountKeyword")
	options.ClientIp = optionalQueryText(values, "clientIp")
	if result := values.Get("result"); result == "success" || result == "failed" || result == "all" {
		options.Result = result
	}
	options.GroupID = optionalQueryText(values, "groupId")
	options.Model = optionalQueryText(values, "model")
	if trafficSource := values.Get("trafficSource"); usageRecordTrafficSourceSet[trafficSource] {
		options.TrafficSource = trafficSource
	}
	sortBy := optionalQueryText(values, "sortBy")
	sortOrder := values.Get("sortOrder")
	if sortBy == "createdAt" && sortOrder == "asc" {
		options.SortOrder = "asc"
	} else {
		options.SortOrder = "desc"
	}
	startAt, endAt, err := d.usageRecordDateRange(r.Context(), values)
	if err != nil {
		return usageRecordListOptions{}, err
	}
	options.StartAt, options.EndAt = startAt, endAt
	return options, nil
}

// usageRecordDateRange mirrors dateRangeQueryValue: same-day defaults, swapped
// bounds, [start-of-day, start-of-next-day) instants in the usage timezone.
func (d *Deps) usageRecordDateRange(ctx context.Context, values url.Values) (string, string, error) {
	location, err := d.timezoneLocation(ctx)
	if err != nil {
		return "", "", err
	}
	startDate := dateQueryValue(values.Get("startDate"))
	endDate := dateQueryValue(values.Get("endDate"))
	if startDate == "" && endDate == "" {
		if optionalQueryText(values, "startDate") != "" || optionalQueryText(values, "endDate") != "" {
			return "", "", nil
		}
		todayKey := dateKeyIn(d.Now(), location)
		startAt := startOfZonedDateKeyIso(todayKey, location)
		endAt := startOfZonedDateKeyIso(nextCalendarDateKey(todayKey), location)
		return startAt, endAt, nil
	}
	if startDate == "" {
		startDate = endDate
	}
	if endDate == "" {
		endDate = startDate
	}
	if startDate == "" || endDate == "" {
		return "", "", nil
	}
	rangeStart, rangeEnd := startDate, endDate
	if rangeStart > rangeEnd {
		rangeStart, rangeEnd = rangeEnd, rangeStart
	}
	return startOfZonedDateKeyIso(rangeStart, location), startOfZonedDateKeyIso(nextCalendarDateKey(rangeEnd), location), nil
}

// dateQueryValue mirrors dateQueryValue: strict YYYY-MM-DD + calendar validity.
func dateQueryValue(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" || !dateOnlyPattern.MatchString(text) {
		return ""
	}
	if _, _, _, ok := parseDateKeyParts(text); !ok {
		return ""
	}
	return text
}

// listUsageRecords mirrors listUsageRecordsAsync.
func (d *Deps) listUsageRecords(r *http.Request, scope AccessScope, options usageRecordListOptions) (any, error) {
	page, pageSize := normalizeUsageRecordListOptions(options.Page, options.PageSize)
	offset := (page - 1) * pageSize
	filters, err := d.usageRecordFilters(r, scope, options)
	if err != nil {
		return nil, err
	}
	var rows []Row
	if d.PGDialect {
		rows, err = d.usageRecordRowsPG(r, filters, options.SortOrder, offset+pageSize+1)
	} else {
		rows, err = d.usageRecordRowsSQLite(r, scope, filters, options, offset+pageSize+1)
	}
	if err != nil {
		return nil, err
	}
	pageRows := rows
	hasMore := false
	if len(rows) > offset {
		pageRows = rows[offset:]
	} else {
		pageRows = nil
	}
	if len(pageRows) > pageSize {
		pageRows = pageRows[:pageSize]
		hasMore = true
	}
	items, err := d.hydrateUsageRecordItems(r, scope, pageRows)
	if err != nil {
		return nil, err
	}
	return usageRecordListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// normalizeUsageRecordListOptions mirrors normalizeUsageRecordListOptions:
// only createdAt sorts, desc unless explicitly asc.
func normalizeUsageRecordListOptions(rawPage, rawPageSize int) (int, int) {
	pageSize := usageRecordDefaultPageSize
	if rawPageSize > 0 {
		pageSize = clampInt(rawPageSize, 1, usageRecordMaxPageSize)
	}
	maxPage := (usageRecordMaxListWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	page := 1
	if rawPage > 0 {
		page = clampInt(rawPage, 1, maxPage)
	}
	return page, pageSize
}

// usageRecordFilterSet carries the assembled WHERE clause.
type usageRecordFilterSet struct {
	clause string
	params []any
}

// usageRecordFilters mirrors buildUsageRecordFilters (SQLite binary prefix
// bounds; the accountKeyword path resolves matching account ids first).
func (d *Deps) usageRecordFilters(r *http.Request, scope AccessScope, options usageRecordListOptions) (usageRecordFilterSet, error) {
	filters := usageRecordFilterSet{}
	clauses := []string{}
	params := []any{}
	if scopedID := scope.scopedID(); scopedID != "" {
		clauses = append(clauses, "ur.system_account_id = ?")
		params = append(params, scopedID)
	}
	pushPrefixFilter := func(column, text string) {
		if text == "" {
			return
		}
		clauses = append(clauses, column+" >= ? AND "+column+" < ?")
		params = append(params, text, textPrefixUpperBound(text))
	}
	pushPrefixFilter("ur.trace_id", options.TraceID)
	if options.AccountKeyword != "" {
		accountIds, err := d.usageRecordKeywordAccountIds(r, scope, options.AccountKeyword)
		if err != nil {
			return filters, err
		}
		if len(accountIds) > 0 {
			clauses = append(clauses, "ur.account_id IN ("+placeholders(len(accountIds))+")")
			params = append(params, idsToAny(accountIds)...)
		} else {
			clauses = append(clauses, "1 = 0")
		}
	}
	switch options.Result {
	case "success":
		clauses = append(clauses, "ur.success = 1")
	case "failed":
		clauses = append(clauses, "ur.success = 0")
	}
	if options.StatusCode >= 100 && options.StatusCode <= 599 {
		clauses = append(clauses, "ur.status_code = ?")
		params = append(params, options.StatusCode)
	}
	pushPrefixFilter("ur.client_ip", options.ClientIp)
	if options.GroupID != "" {
		clauses = append(clauses, "ur.group_id = ?")
		params = append(params, options.GroupID)
	}
	if options.StartAt != "" {
		clauses = append(clauses, "ur.created_at >= ?")
		params = append(params, options.StartAt)
	}
	if options.EndAt != "" {
		clauses = append(clauses, "ur.created_at < ?")
		params = append(params, options.EndAt)
	}
	if options.Model != "" {
		clauses = append(clauses, "ur.model = ?")
		params = append(params, options.Model)
	}
	if options.TrafficSource != "" {
		clauses = append(clauses, "ur.traffic_source = ?")
		params = append(params, options.TrafficSource)
	}
	if len(clauses) > 0 {
		filters.clause = "WHERE " + strings.Join(clauses, " AND ")
	}
	filters.params = params
	return filters, nil
}

func textPrefixUpperBound(value string) string {
	chars := []rune(value)
	for index := len(chars) - 1; index >= 0; index-- {
		codePoint := chars[index]
		if codePoint >= 0x10ffff {
			continue
		}
		return string(chars[:index]) + string(codePoint+1)
	}
	return value + "\U0010ffff"
}

// usageRecordKeywordAccountIds mirrors accountIdsForKeyword (SQLite branch).
func (d *Deps) usageRecordKeywordAccountIds(r *http.Request, scope AccessScope, keyword string) ([]string, error) {
	normalized := strings.TrimSpace(keyword)
	upperBound := textPrefixUpperBound(normalized)
	ownerID := scope.scopedID()
	ids := []string{}
	appendIDs := func(rows []Row) {
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		for _, row := range rows {
			id := row.text("id")
			if id == "" || seen[id] || len(ids) >= usageRecordKeywordMatchLimit {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	ownerClause := ""
	ownerParams := []any{}
	if ownerID != "" {
		ownerClause = " AND accounts.system_account_id = ?"
		ownerParams = append(ownerParams, ownerID)
	}
	rows, err := d.queryBusiness(r, `
		SELECT accounts.id
		FROM accounts
		WHERE accounts.deleted_at IS NULL
			AND accounts.name >= ? AND accounts.name < ?`+ownerClause+`
		ORDER BY accounts.name ASC, accounts.id ASC
		LIMIT ?
	`, flatParams([]any{normalized, upperBound}, ownerParams, usageRecordKeywordMatchLimit)...)
	if err != nil {
		return nil, err
	}
	appendIDs(rows)
	rows, err = d.queryBusiness(r, `
		SELECT instance_accounts.id
		FROM accounts source_accounts
		INNER JOIN accounts instance_accounts
			ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
		WHERE source_accounts.deleted_at IS NULL
			AND instance_accounts.deleted_at IS NULL
			AND source_accounts.name >= ? AND source_accounts.name < ?`+ownerClause+`
		ORDER BY source_accounts.name ASC, instance_accounts.id ASC
		LIMIT ?
	`, flatParams([]any{normalized, upperBound}, ownerParams, usageRecordKeywordMatchLimit)...)
	if err != nil {
		return nil, err
	}
	appendIDs(rows)
	if ownerID != "" {
		rows, err = d.queryBusiness(r, `
			SELECT accounts.id
			FROM accounts
			INNER JOIN resource_authorizations ra
				ON ra.resource_type = 'account'
				AND ra.resource_id = accounts.id
				AND ra.grantee_system_account_id = ?
			WHERE accounts.deleted_at IS NULL
				AND accounts.name >= ? AND accounts.name < ?
			ORDER BY accounts.name ASC, accounts.id ASC
			LIMIT ?
		`, ownerID, normalized, upperBound, usageRecordKeywordMatchLimit)
		if err != nil {
			return nil, err
		}
		appendIDs(rows)
		rows, err = d.queryBusiness(r, `
			SELECT accounts.id
			FROM accounts
			INNER JOIN group_accounts ga
				ON ga.account_id = accounts.id
				AND ga.enabled = 1
			INNER JOIN resource_authorizations ra
				ON ra.resource_type = 'group'
				AND ra.resource_id = ga.group_id
				AND ra.grantee_system_account_id = ?
			WHERE accounts.deleted_at IS NULL
				AND accounts.name >= ? AND accounts.name < ?
			ORDER BY accounts.name ASC, accounts.id ASC
			LIMIT ?
		`, ownerID, normalized, upperBound, usageRecordKeywordMatchLimit)
		if err != nil {
			return nil, err
		}
		appendIDs(rows)
	}
	return ids, nil
}

// usageRecordListSelectColumns mirror usageRecordListSelectColumns.
const usageRecordListSelectColumns = `ur.id, ur.system_account_id, ur.trace_id, ur.traffic_source, ur.client_ip,
	ur.api_key_id, ur.group_id, ur.account_id, ur.endpoint, ur.model, ur.upstream_model,
	ur.upstream_response_model, ur.billed_service_tier, ur.effective_reasoning_effort,
	ur.model_mapping_applied, ur.stream, ur.status_code, ur.success, ur.failure_attribution,
	ur.error_code, ur.error_message, ur.first_token_ms, ur.duration_ms, ur.input_tokens,
	ur.output_tokens, ur.cache_read_tokens, ur.cost_usd, ur.created_at`

// usageRecordRowsPG mirrors listPostgresUsageRecordRows.
func (d *Deps) usageRecordRowsPG(r *http.Request, filters usageRecordFilterSet, sortOrder string, limit int) ([]Row, error) {
	direction := "DESC"
	if sortOrder == "asc" {
		direction = "ASC"
	}
	return queryRowsContext(r.Context(), d.Stats, `
		SELECT
			`+usageRecordListSelectColumns+`
		FROM juhe_usage.usage_records ur
		`+filters.clause+`
		ORDER BY ur.created_at `+direction+`, ur.id `+direction+`
		LIMIT ?
	`, append(append([]any{}, filters.params...), limit)...)
}

type usageShardLocation struct {
	ShardKey   string
	BucketDate string
	FilePath   string
}

// usageRecordRowsSQLite mirrors listUsageRecordRowsFromShards: per-shard
// limited reads merged and re-sorted in memory.
func (d *Deps) usageRecordRowsSQLite(r *http.Request, scope AccessScope, filters usageRecordFilterSet, options usageRecordListOptions, perShardLimit int) ([]Row, error) {
	locations, err := d.usageShardLocations(r.Context(), options.StartAt, options.EndAt)
	if err != nil {
		return nil, err
	}
	direction := sortDesc
	if options.SortOrder == "asc" {
		direction = sortAsc
	}
	merged := []Row{}
	for _, location := range locations {
		shardDB, openErr := openUsageShardDB(location.FilePath)
		if openErr != nil {
			continue
		}
		rows, queryErr := queryRowsContext(r.Context(), shardDB, `
			SELECT
				`+usageRecordListSelectColumns+`
			FROM usage_records ur
			`+filters.clause+`
			ORDER BY ur.created_at `+direction.sql()+`, ur.id `+direction.sql()+`
			LIMIT ?
		`, append(append([]any{}, filters.params...), perShardLimit)...)
		if queryErr != nil {
			_ = shardDB.Close()
			continue
		}
		merged = append(merged, rows...)
		_ = shardDB.Close()
	}
	sort.SliceStable(merged, func(left, right int) bool {
		return compareUsageRecordRows(merged[left], merged[right], direction)
	})
	if len(merged) > perShardLimit {
		merged = merged[:perShardLimit]
	}
	return merged, nil
}

type sortDirection int

const (
	sortDesc sortDirection = iota
	sortAsc
)

func (d sortDirection) sql() string {
	if d == sortAsc {
		return "ASC"
	}
	return "DESC"
}

// compareUsageRecordRows mirrors compareUsageRecordRows for createdAt sorts.
func compareUsageRecordRows(left, right Row, direction sortDirection) bool {
	compare := compareUsageTimestamp(left.text("created_at"), right.text("created_at"))
	if direction == sortDesc {
		// DESC keeps the newest first: invert the natural ascending compare.
		compare = -compare
	}
	if compare != 0 {
		return compare < 0
	}
	compare = strings.Compare(left.text("id"), right.text("id"))
	if direction == sortAsc {
		return compare < 0
	}
	return compare > 0
}

func compareUsageTimestamp(left, right string) int {
	leftMs, leftOK := parseRFC3339Millis(left)
	rightMs, rightOK := parseRFC3339Millis(right)
	if !leftOK || !rightOK {
		// Node throws on unparseable timestamps; the route surfaces a 500.
		return 0
	}
	if leftMs == rightMs {
		return 0
	}
	if leftMs > rightMs {
		return 1
	}
	return -1
}

func parseRFC3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// usageShardLocations mirrors listUsageRecordShardLocations: a date-bounded
// bucket window when start/end are known, the full active registry otherwise.
func (d *Deps) usageShardLocations(ctx context.Context, startAt, endAt string) ([]usageShardLocation, error) {
	if d.UsageCatalog == nil {
		// Composition roots that cannot provide the usage catalog (or tests)
		// get the documented "no registered shards" outcome instead of a nil
		// dereference.
		return nil, nil
	}
	clauses := []string{"status = 'active'"}
	params := []any{}
	if startAt != "" || endAt != "" {
		endKey := ""
		startKey := ""
		if endAt != "" {
			endKey = bucketDateKeyFromIso(endAt)
		}
		if startAt != "" {
			startKey = bucketDateKeyFromIso(startAt)
		}
		if endKey == "" {
			endKey = bucketDateKeyFromIso(d.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		if startKey == "" {
			startKey = endKey
		}
		startMs, _ := bucketDateKeyToUTCms(startKey)
		endMs, _ := bucketDateKeyToUTCms(endKey)
		ascendingStart, ascendingEnd := startMs, endMs
		if ascendingStart > ascendingEnd {
			ascendingStart, ascendingEnd = ascendingEnd, ascendingStart
		}
		days := int((ascendingEnd-ascendingStart)/dayMS) + 1
		if days > usageRecordShardWindowMaxDays {
			days = usageRecordShardWindowMaxDays
		}
		if days < 1 {
			days = 1
		}
		boundedStart := ascendingEnd - int64(days-1)*dayMS
		clauses = append(clauses, "bucket_date >= ?", "bucket_date <= ?")
		params = append(params,
			time.UnixMilli(boundedStart).UTC().Format("2006-01-02"),
			time.UnixMilli(ascendingEnd).UTC().Format("2006-01-02"),
		)
	}
	rows, qerr := queryRowsContext(ctx, d.UsageCatalog, `
		SELECT shard_key, bucket_date, shard_id, file_path
		FROM usage_record_shards
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY bucket_date ASC, shard_id ASC
	`, params...)
	if qerr != nil {
		return nil, qerr
	}
	locations := []usageShardLocation{}
	for _, row := range rows {
		shardKey := strings.TrimSpace(row.text("shard_key"))
		bucketDate := strings.TrimSpace(row.text("bucket_date"))
		filePath := strings.TrimSpace(row.text("file_path"))
		shardID, hasShardID := toFloat(row.value("shard_id"))
		if shardKey == "" || bucketDate == "" || filePath == "" || !hasShardID || shardID != float64(int64(shardID)) {
			continue
		}
		locations = append(locations, usageShardLocation{ShardKey: shardKey, BucketDate: bucketDate, FilePath: filePath})
	}
	return locations, nil
}

func bucketDateKeyFromIso(value string) string {
	if len(value) < 10 {
		return ""
	}
	return strings.ReplaceAll(value[:10], "-", "")
}

func bucketDateKeyToUTCms(key string) (int64, bool) {
	if len(key) != 8 {
		return 0, false
	}
	year, errYear := strconv.Atoi(key[0:4])
	month, errMonth := strconv.Atoi(key[4:6])
	day, errDay := strconv.Atoi(key[6:8])
	if errYear != nil || errMonth != nil || errDay != nil {
		return 0, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli(), true
}

// openUsageShardDB opens a shard file read-write handle with the Node busy
// timeout pragma.
func openUsageShardDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
}

// hydrateUsageRecordItems mirrors hydrateUsageRecordNames +
// usageRecordListItemFromRow.
func (d *Deps) hydrateUsageRecordItems(r *http.Request, scope AccessScope, rows []Row) ([]usageRecordListItem, error) {
	apiKeyIDs := collectColumn(rows, "api_key_id")
	groupIDs := collectColumn(rows, "group_id")
	accountIDs := collectColumn(rows, "account_id")
	systemAccountIDs := collectColumn(rows, "system_account_id")
	apiKeyNames, err := d.loadBusinessNameMap(r, "api_keys", apiKeyIDs)
	if err != nil {
		return nil, err
	}
	groupNames, err := d.loadBusinessNameMap(r, "groups", groupIDs)
	if err != nil {
		return nil, err
	}
	accountNames, err := d.loadBusinessNameMap(r, "accounts", accountIDs)
	if err != nil {
		return nil, err
	}
	var systemAccountNames map[string]*string
	if scope.canAccessAll() {
		systemAccountNames, err = d.loadSystemAccountNameMap(r, systemAccountIDs)
		if err != nil {
			return nil, err
		}
	}
	items := make([]usageRecordListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, d.mapUsageRecordListItem(row, scope, apiKeyNames, groupNames, accountNames, systemAccountNames))
	}
	return items, nil
}

func collectColumn(rows []Row, column string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		value := optionalText(row[column])
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		ids = append(ids, value)
	}
	return ids
}

func optionalText(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(toText(value))
}

// loadBusinessNameMap mirrors loadApiKeyNameMap/loadGroupNameMap/loadAccount
// NameMap: raw id -> name rows over the business database.
func (d *Deps) loadBusinessNameMap(r *http.Request, table string, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	for _, chunk := range chunkStrings(ids, 900) {
		rows, err := d.queryBusiness(r, `
			SELECT id, name
			FROM `+d.businessTable(table)+`
			WHERE id IN (`+placeholders(len(chunk))+`)
		`, idsToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			names[row.text("id")] = row.text("name")
		}
	}
	return names, nil
}

// loadSystemAccountNameMap mirrors loadSystemAccountNameMapByIds: display_name
// wins over username.
func (d *Deps) loadSystemAccountNameMap(r *http.Request, ids []string) (map[string]*string, error) {
	names := map[string]*string{}
	if len(ids) == 0 {
		return names, nil
	}
	for _, chunk := range chunkStrings(ids, 900) {
		rows, err := d.queryBusiness(r, `
			SELECT id, username, display_name
			FROM `+d.businessTable("system_accounts")+`
			WHERE id IN (`+placeholders(len(chunk))+`)
		`, idsToAny(chunk)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			displayName := row.nullText("display_name")
			if displayName == nil {
				username := row.nullText("username")
				names[row.text("id")] = username
				continue
			}
			names[row.text("id")] = displayName
		}
	}
	return names, nil
}

// mapUsageRecordListItem mirrors usageRecordListItemFromRow.
func (d *Deps) mapUsageRecordListItem(row Row, scope AccessScope, apiKeyNames, groupNames, accountNames map[string]string, systemAccountNames map[string]*string) usageRecordListItem {
	upstreamModel := row.nullText("upstream_model")
	upstreamResponseModel := row.nullText("upstream_response_model")
	success := row.boolLike("success")
	item := usageRecordListItem{
		ID:                       row.text("id"),
		TraceID:                  row.text("trace_id"),
		TrafficSource:            row.nullText("traffic_source"),
		ClientIp:                 row.nullText("client_ip"),
		ApiKeyId:                 row.nullText("api_key_id"),
		GroupId:                  row.nullText("group_id"),
		AccountId:                row.nullText("account_id"),
		Endpoint:                 row.nullText("endpoint"),
		Model:                    row.nullText("model"),
		UpstreamModel:            upstreamModel,
		UpstreamResponseModel:    upstreamResponseModel,
		UpstreamModelMismatch:    upstreamModelMismatch(upstreamModel, upstreamResponseModel),
		BilledServiceTier:        row.nullText("billed_service_tier"),
		EffectiveReasoningEffort: row.nullText("effective_reasoning_effort"),
		ModelMappingApplied:      row.boolLike("model_mapping_applied"),
		Stream:                   row.boolLike("stream"),
		StatusCode:               row.nullNumber("status_code"),
		Success:                  success,
		FailureAttribution:       failureAttributionOrUndefined(row.nullText("failure_attribution")),
		FailureReason:            usageRecordListFailureReason(row),
		ErrorCode:                row.nullText("error_code"),
		ErrorMessage:             row.nullText("error_message"),
		FirstTokenMs:             row.nullNumber("first_token_ms"),
		DurationMs:               row.nullNumber("duration_ms"),
		InputTokens:              row.nullNumber("input_tokens"),
		OutputTokens:             row.nullNumber("output_tokens"),
		CacheReadTokens:          row.nullNumber("cache_read_tokens"),
		CostUsd:                  row.nullFloat("cost_usd"),
		CreatedAt:                row.text("created_at"),
	}
	if apiKeyID := optionalText(row["api_key_id"]); apiKeyID != "" {
		if name := apiKeyNames[apiKeyID]; name != "" {
			value := name
			item.ApiKeyName = &value
		}
	}
	if groupID := optionalText(row["group_id"]); groupID != "" {
		if name := groupNames[groupID]; name != "" {
			value := name
			item.GroupName = &value
		}
	}
	if accountID := optionalText(row["account_id"]); accountID != "" {
		if name := accountNames[accountID]; name != "" {
			value := name
			item.AccountName = &value
		}
	}
	if scope.canAccessAll() {
		if systemAccountID := optionalText(row["system_account_id"]); systemAccountID != "" {
			value := systemAccountID
			item.SystemAccountID = &value
			if name, ok := systemAccountNames[systemAccountID]; ok {
				item.SystemAccountName = name
			}
		}
	}
	return item
}

// upstreamModelMismatch mirrors hasUpstreamResponseModelMismatch.
func upstreamModelMismatch(upstreamModel, upstreamResponseModel *string) bool {
	sent := strings.TrimSpace(derefText(upstreamModel))
	response := strings.TrimSpace(derefText(upstreamResponseModel))
	return sent != "" && response != "" && sent != response
}

func derefText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// failureAttributionOrUndefined mirrors usageFailureAttribution (unknown
// values map to undefined).
func failureAttributionOrUndefined(value *string) *string {
	if value == nil {
		return nil
	}
	switch *value {
	case "account_upstream", "account_dependency", "opaque_upstream", "gateway_capacity", "gateway_policy", "downstream_closed":
		return value
	default:
		return nil
	}
}

var usageFailureReasonByErrorCode = map[string]string{
	"request_timeout":                       "请求体上传未完成",
	"request_too_large":                     "请求体超过网关限制",
	"request_body_too_large":                "请求体超过网关限制",
	"gateway_body_in_flight_limit_exceeded": "网关正在处理过多请求体",
	"gateway_json_parser_busy":              "网关请求体解析繁忙",
	"gateway_json_parser_failed":            "网关请求体解析失败",
	"rate_limit_exceeded":                   "请求被限流",
	"user_request_limit_exceeded":           "请求超过用户限额",
	"no_available_upstream_account":         "没有可调度的上游账户",
	"account_concurrency_limit":             "上游账户并发已满",
	"normal_route_first_byte_timeout":       "上游未在首段时限内响应",
	"upstream_retryable_error":              "上游暂时不可用",
	"unproven_upstream_transport_failure":   "上游传输失败，具体原因未确认",
	"upstream_protocol_failure":             "上游响应返回失败终态",
	"upstream_protocol_error":               "上游响应协议异常",
	"invalid_api_key":                       "API Key 无效",
	"forbidden":                             "请求无权限",
	"invalid_json":                          "请求 JSON 无效",
	"model_not_routable_for_api_key":        "当前 API Key 无权使用该模型",
	"model_route_ambiguous":                 "模型路由不唯一",
	"model_route_unavailable":               "模型当前不可用",
	"model_target_group_not_bound":          "模型未绑定可用分组",
	"model_target_group_unavailable":        "模型目标分组不可用",
	"proxy_unavailable":                     "账户代理不可用",
	"server_overloaded":                     "网关当前负载过高",
}

// usageRecordListFailureReason mirrors usageRecordListFailureReason.
func usageRecordListFailureReason(row Row) *string {
	if row.boolLike("success") {
		return nil
	}
	errorCode := optionalText(row["error_code"])
	errorMessage := boundedUsageFailureMessage(optionalText(row["error_message"]))
	attribution := optionalText(row["failure_attribution"])
	if errorCode == "downstream_connection_closed" || attribution == "downstream_closed" {
		reason := "下游连接关闭"
		return &reason
	}
	facts := []string{}
	if errorCode != "" {
		facts = append(facts, errorCode)
	}
	if errorMessage != nil {
		facts = append(facts, *errorMessage)
	}
	if len(facts) > 0 {
		reason := strings.Join(facts, " | ")
		return &reason
	}
	if reason := usageFailureReasonByErrorCode[errorCode]; reason != "" {
		return &reason
	}
	switch attribution {
	case "account_dependency":
		reason := "账户依赖不可用"
		return &reason
	case "opaque_upstream":
		reason := "上游失败，未返回可解析的错误详情"
		return &reason
	case "account_upstream":
		reason := "上游请求失败"
		return &reason
	case "gateway_capacity":
		reason := "网关容量不足"
		return &reason
	case "gateway_policy":
		reason := "网关策略拒绝请求"
		return &reason
	default:
		reason := "请求未正常完成"
		return &reason
	}
}

func boundedUsageFailureMessage(value string) *string {
	if value == "" {
		return nil
	}
	const limit = 500
	if len([]rune(value)) > limit {
		trimmed := string([]rune(value)[:limit]) + " [已截断]"
		return &trimmed
	}
	return &value
}

type usageRecordListResult struct {
	Items    []usageRecordListItem `json:"items"`
	Total    int                   `json:"total"`
	HasMore  bool                  `json:"hasMore"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type usageRecordListItem struct {
	ID                       string   `json:"id"`
	SystemAccountID          *string  `json:"systemAccountId,omitempty"`
	SystemAccountName        *string  `json:"systemAccountName,omitempty"`
	TraceID                  string   `json:"traceId"`
	TrafficSource            *string  `json:"trafficSource,omitempty"`
	ClientIp                 *string  `json:"clientIp,omitempty"`
	ApiKeyId                 *string  `json:"apiKeyId,omitempty"`
	ApiKeyName               *string  `json:"apiKeyName,omitempty"`
	GroupId                  *string  `json:"groupId,omitempty"`
	GroupName                *string  `json:"groupName,omitempty"`
	AccountId                *string  `json:"accountId,omitempty"`
	AccountName              *string  `json:"accountName,omitempty"`
	Endpoint                 *string  `json:"endpoint,omitempty"`
	Model                    *string  `json:"model,omitempty"`
	UpstreamModel            *string  `json:"upstreamModel,omitempty"`
	UpstreamResponseModel    *string  `json:"upstreamResponseModel,omitempty"`
	UpstreamModelMismatch    bool     `json:"upstreamModelMismatch"`
	BilledServiceTier        *string  `json:"billedServiceTier,omitempty"`
	EffectiveReasoningEffort *string  `json:"effectiveReasoningEffort,omitempty"`
	ModelMappingApplied      bool     `json:"modelMappingApplied"`
	Stream                   bool     `json:"stream"`
	StatusCode               *int64   `json:"statusCode,omitempty"`
	Success                  bool     `json:"success"`
	FailureAttribution       *string  `json:"failureAttribution,omitempty"`
	FailureReason            *string  `json:"failureReason,omitempty"`
	ErrorCode                *string  `json:"errorCode,omitempty"`
	ErrorMessage             *string  `json:"errorMessage,omitempty"`
	FirstTokenMs             *int64   `json:"firstTokenMs,omitempty"`
	DurationMs               *int64   `json:"durationMs,omitempty"`
	InputTokens              *int64   `json:"inputTokens,omitempty"`
	OutputTokens             *int64   `json:"outputTokens,omitempty"`
	CacheReadTokens          *int64   `json:"cacheReadTokens,omitempty"`
	CostUsd                  *float64 `json:"costUsd,omitempty"`
	CreatedAt                string   `json:"createdAt"`
}

func errToString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
