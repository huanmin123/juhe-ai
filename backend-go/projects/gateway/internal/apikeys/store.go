package apikeys

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ConflictError maps to the Node route family 409 paths: the duplicate API
// key name error and the default/chat delete guards.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the Node apiKeyMutationClientError 400 set
// (missing default route strategy, unselectable strategy, invalid expiry) and
// the quota/schedule normalization messages surfaced through the body
// validation layer.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// CacheInvalidator is the three-family cache invalidation port the Node
// repository triggers after a committed mutation:
//   - InvalidateValidation mirrors notifyGatewayApiKeyValidationCacheInvalidation
//     (required; an error fails the operation with 500),
//   - InvalidateQuota mirrors notifyApiKeyQuotaCacheInvalidation,
//   - InvalidateRuntime mirrors invalidateApiKeyLookupCache +
//     notifyGatewayRuntimeCacheInvalidation (best effort).
//
// BusInvalidator satisfies it with *inval.Bus; nil keeps the slice
// self-contained with no-op invalidation.
type CacheInvalidator interface {
	InvalidateValidation(apiKeyID string, reason string, keyHashes []string) error
	InvalidateQuota(apiKeyID string, reason string)
	InvalidateRuntime(apiKeyID string, reason string)
}

// Node invalidation reason strings (shared/gateway-cache-invalidation.ts).
const (
	ReasonAPIKeySecretRefreshed = "api_key_secret_refreshed"
	ReasonAPIKeyDeleted         = "api_key_deleted"
)

// BusInvalidator adapts the K5 invalidation bus; the apiKey id is appended to
// the reason so subscribers can scope their cache flush.
type BusInvalidator struct{ Bus *inval.Bus }

func (b BusInvalidator) InvalidateValidation(apiKeyID, reason string, _ []string) error {
	if b.Bus == nil {
		return nil
	}
	b.Bus.Invalidate(inval.TopicGatewayAPIKeyValidation, reason+" "+apiKeyID)
	return nil
}

func (b BusInvalidator) InvalidateQuota(apiKeyID, reason string) {
	if b.Bus == nil {
		return
	}
	b.Bus.Invalidate(inval.TopicAPIKeyQuota, reason+" "+apiKeyID)
}

func (b BusInvalidator) InvalidateRuntime(apiKeyID, reason string) {
	if b.Bus == nil {
		return
	}
	b.Bus.Invalidate(inval.TopicGatewayRuntime, reason+" "+apiKeyID)
}

// gptVendorCode mirrors GPT_VENDOR_CODE (domain/provider-protocol.ts): the
// preferred default route strategy binds the owner's default enabled group on
// this provider.
const gptVendorCode = "gpt"

// AccessScope mirrors storage/access-scope.ts for the api-keys slice: admins
// see every row unless a systemAccountId filter narrows the view; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId: admins pass the filter
// through (possibly empty = unscoped), non-admins are pinned to themselves.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// ownerID mirrors manageableSystemAccountId ?? currentSystemAccountId: the
// account stamped on newly created rows.
func (a AccessScope) ownerID() (string, error) {
	if id := a.manageableID(); id != "" {
		return id, nil
	}
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}

// Store is the dual-mode api-keys persistence (SQLite + PostgreSQL). secret
// is the Node runtimeConfig.secret material: api_keys.key_secret_encrypted
// rows written by Node must stay decryptable.
type Store struct {
	db     *sql.DB
	pg     bool
	secret string
	now    func() time.Time
	newI   func(prefix string) string
	inval  CacheInvalidator
}

// NewStore builds the store; inval may be nil (no-op invalidation until the
// bus is wired).
func NewStore(db *sql.DB, postgres bool, secret string, now func() time.Time, newID func(string) string, inval CacheInvalidator) (*Store, error) {
	if db == nil {
		return nil, errors.New("apikeys store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("apikeys store requires the runtime secret")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	return &Store{db: db, pg: postgres, secret: secret, now: now, newI: newID, inval: inval}, nil
}

// randomID mirrors Node newId (random hex suffix).
func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

// datasetTable mirrors the dataset-database placement of
// api_key_record_cleanup_targets (M01 dataset domain); the slice writes it in
// the same database handle/transaction as the business delete.
func (s *Store) datasetTable(name string) string {
	if s.pg {
		return "juhe_dataset." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// revisionFromMillis mirrors apiKeyRevisionFromTimestamp: ISO with six
// fractional digits (microsecond rendering of the millisecond clock).
func revisionFromMillis(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02T15:04:05.000000") + "Z"
}

// nextRevision mirrors nextApiKeyRevision: monotonic versus the stored
// revision (now wins, or previous + 1ms).
func nextRevision(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", fmt.Errorf("API Key revision 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", current)
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return revisionFromMillis(next), nil
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant (offset required,
// UTC/Z output with millisecond precision).
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// QuotaLimit mirrors RequestQuotaLimit/RequestHourlyQuotaLimit (USD limits).
type QuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Limit   float64 `json:"limit"`
	Hours   int     `json:"hours,omitempty"`
}

// QuotaLimits mirrors RequestQuotaLimits; disabled/absent entries are
// stripped by normalization and omitted from JSON.
type QuotaLimits struct {
	Hourly  *QuotaLimit `json:"hourly,omitempty"`
	Daily   *QuotaLimit `json:"daily,omitempty"`
	Weekly  *QuotaLimit `json:"weekly,omitempty"`
	Monthly *QuotaLimit `json:"monthly,omitempty"`
	Total   *QuotaLimit `json:"total,omitempty"`
}

const maxRequestQuotaHourlyWindowHours = 24 * 30

func emptyQuotaLimits() QuotaLimits { return QuotaLimits{} }

func (q QuotaLimits) hasEnabled() bool {
	return (q.Hourly != nil && q.Hourly.Enabled) || (q.Daily != nil && q.Daily.Enabled) ||
		(q.Weekly != nil && q.Weekly.Enabled) || (q.Monthly != nil && q.Monthly.Enabled) ||
		(q.Total != nil && q.Total.Enabled)
}

// ParseQuotaLimitsJSON mirrors parseRequestQuotaLimitsJson.
func ParseQuotaLimitsJSON(raw string) (QuotaLimits, error) {
	if strings.TrimSpace(raw) == "" {
		return emptyQuotaLimits(), nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return emptyQuotaLimits(), &ValidationError{Message: "请求额度限制参数无效"}
	}
	return normalizeQuotaLimits(decoded, emptyQuotaLimits())
}

// QuotaLimitsJSON mirrors requestQuotaLimitsJson: all-disabled → NULL.
func QuotaLimitsJSON(limits QuotaLimits) (string, bool) {
	if !limits.hasEnabled() {
		return "", false
	}
	encoded, err := json.Marshal(limits)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// normalizeQuotaLimits mirrors normalizeRequestQuotaLimits: undefined keeps
// the fallback, null clears, every present entry must be enabled with a
// positive 6-decimal USD limit; disabled entries are stripped.
func normalizeQuotaLimits(input any, fallback QuotaLimits) (QuotaLimits, error) {
	if input == nil {
		return emptyQuotaLimits(), nil
	}
	object, ok := input.(map[string]any)
	if !ok {
		return fallback, &ValidationError{Message: "请求额度限制参数无效"}
	}
	if err := assertQuotaKeys(object); err != nil {
		return fallback, err
	}
	hourly, err := normalizeHourlyQuotaLimit(object["hourly"])
	if err != nil {
		return fallback, err
	}
	daily, err := normalizeQuotaLimit(object["daily"], "日额度")
	if err != nil {
		return fallback, err
	}
	weekly, err := normalizeQuotaLimit(object["weekly"], "周额度")
	if err != nil {
		return fallback, err
	}
	monthly, err := normalizeQuotaLimit(object["monthly"], "月额度")
	if err != nil {
		return fallback, err
	}
	total, err := normalizeQuotaLimit(object["total"], "总额度")
	if err != nil {
		return fallback, err
	}
	limits := QuotaLimits{}
	if hourly != nil {
		limits.Hourly = hourly
	}
	if daily != nil {
		limits.Daily = daily
	}
	if weekly != nil {
		limits.Weekly = weekly
	}
	if monthly != nil {
		limits.Monthly = monthly
	}
	if total != nil {
		limits.Total = total
	}
	return limits, nil
}

func assertQuotaKeys(value map[string]any) error {
	for key := range value {
		switch key {
		case "hourly", "daily", "weekly", "monthly", "total":
		default:
			return &ValidationError{Message: "请求额度限制包含不支持字段：" + key}
		}
	}
	return nil
}

func normalizeQuotaLimit(value any, label string) (*QuotaLimit, error) {
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: label + "参数无效"}
	}
	for key := range object {
		switch key {
		case "enabled", "limit":
		default:
			return nil, &ValidationError{Message: label + "包含不支持字段：" + key}
		}
	}
	if enabled, ok := object["enabled"].(bool); !ok || !enabled {
		return nil, &ValidationError{Message: label + "启用状态必须为 true"}
	}
	limit, err := positiveAmount(object["limit"], label)
	if err != nil {
		return nil, err
	}
	return &QuotaLimit{Enabled: true, Limit: limit}, nil
}

func normalizeHourlyQuotaLimit(value any) (*QuotaLimit, error) {
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "小时额度参数无效"}
	}
	for key := range object {
		switch key {
		case "enabled", "limit", "hours":
		default:
			return nil, &ValidationError{Message: "小时额度包含不支持字段：" + key}
		}
	}
	if enabled, ok := object["enabled"].(bool); !ok || !enabled {
		return nil, &ValidationError{Message: "小时额度启用状态必须为 true"}
	}
	limit, err := positiveAmount(object["limit"], "小时额度")
	if err != nil {
		return nil, err
	}
	rawHours, ok := object["hours"].(float64)
	if !ok || rawHours != float64(int(rawHours)) {
		return nil, &ValidationError{Message: "小时额度窗口必须是数字"}
	}
	hours := int(rawHours)
	if hours < 1 || hours > maxRequestQuotaHourlyWindowHours {
		return nil, &ValidationError{Message: "小时额度窗口必须在 1-" + itoa(maxRequestQuotaHourlyWindowHours) + " 之间"}
	}
	return &QuotaLimit{Enabled: true, Limit: limit, Hours: hours}, nil
}

func positiveAmount(value any, label string) (float64, error) {
	amount, ok := value.(float64)
	if !ok || math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || amount > 1<<53-1 {
		return 0, &ValidationError{Message: label + "金额必须是大于 0 的数字"}
	}
	scaled := amount * 1_000_000
	if math.Round(scaled) != scaled {
		return 0, &ValidationError{Message: label + "金额最多支持 6 位小数"}
	}
	return math.Round(scaled) / 1_000_000, nil
}

// ListUsageSummary mirrors ApiKeyListUsageSummary; the populated variant is
// owned by the J5 stats slice, so the slice renders the zero value.
type ListUsageSummary struct {
	RequestCount int     `json:"requestCount"`
	TotalTokens  int     `json:"totalTokens"`
	TotalCost    float64 `json:"totalCost"`
}

func emptyListUsageSummary() ListUsageSummary { return ListUsageSummary{} }

// ListItem mirrors ApiKeyListItem. The plaintext key never appears: only the
// masked keyPrefix/keySuffix pair.
type ListItem struct {
	ID                   string                `json:"id"`
	SystemAccountID      *string               `json:"systemAccountId,omitempty"`
	SystemAccountName    *string               `json:"systemAccountName,omitempty"`
	Name                 string                `json:"name"`
	Description          *string               `json:"description,omitempty"`
	KeyPrefix            string                `json:"keyPrefix"`
	KeySuffix            string                `json:"keySuffix"`
	Status               string                `json:"status"`
	IsDefault            bool                  `json:"isDefault"`
	Purpose              string                `json:"purpose"`
	RouteStrategyID      string                `json:"routeStrategyId"`
	RouteStrategyName    *string               `json:"routeStrategyName,omitempty"`
	RouteStrategyMode    *string               `json:"routeStrategyMode,omitempty"`
	RouteStrategyStatus  *string               `json:"routeStrategyStatus,omitempty"`
	ExpiresAt            *string               `json:"expiresAt,omitempty"`
	QuotaLimits          QuotaLimits           `json:"quotaLimits"`
	AvailabilitySchedule *AvailabilitySchedule `json:"availabilitySchedule,omitempty"`
	Usage                ListUsageSummary      `json:"usage"`
	Revision             string                `json:"revision"`
}

// ListPageResult mirrors ApiKeyListResult (total is the paged upper bound).
type ListPageResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

// ListOptions mirrors ApiKeyListOptions; the *Set flags distinguish "absent
// query param" (default page size / page 1) from explicit integers that get
// clamped.
type ListOptions struct {
	Page            int
	PageSet         bool
	PageSize        int
	PageSizeSet     bool
	Keyword         string // 'active' | 'disabled' | '' (all)
	Status          string
	RouteStrategyID string
}

// SecretRecord mirrors ApiKeySecretRecord (one-shot reveal payload).
type SecretRecord struct {
	ID              string
	SystemAccountID string
	Name            string
	KeyPrefix       string
	KeySuffix       string
	Key             string
}

// CreateResult mirrors ApiKeyCreateResult: the plaintext key leaves the
// server exactly once.
type CreateResult struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	KeyPrefix string `json:"keyPrefix"`
	KeySuffix string `json:"keySuffix"`
	Revision  string `json:"revision"`
}

// CreateMeta carries the fields the operation log needs beyond the envelope.
type CreateMeta struct {
	OwnerSystemAccountID string
	Name                 string
	Status               string
	RouteStrategyID      string
	AvailabilitySchedule *AvailabilitySchedule
}

// RefreshOutcome mirrors ApiKeyRefreshOutcome.
type RefreshOutcome struct {
	Result               CreateResult
	OwnerSystemAccountID string
	ResourceName         string
	PreviousKeyPrefix    string
	PreviousKeySuffix    string
	ValidationCacheError error
}

// DeleteResult mirrors ApiKeyDeleteResult (deleted branch subset).
type DeleteResult struct {
	Deleted               bool
	CleanupTargetAPIKeyID string
	CleanupTargetOwnerID  string
	OwnerSystemAccountID  string
	ResourceName          string
	ValidationCacheError  error
}

// apiKeyRow is the shared scan target for list/detail rows.
type apiKeyRow struct {
	id                  string
	systemAccountID     string
	systemAccountName   sql.NullString
	routeStrategyID     string
	routeStrategyName   sql.NullString
	routeStrategyMode   sql.NullString
	routeStrategyStatus sql.NullString
	name                string
	description         sql.NullString
	keyPrefix           string
	keySuffix           string
	status              string
	isDefault           int
	purpose             sql.NullString
	expiresAt           sql.NullString
	quotaJSON           sql.NullString
	scheduleJSON        sql.NullString
	updatedAt           string
}

func scanAPIKeyRow(scan func(...any) error, includeOwner bool) (apiKeyRow, error) {
	var row apiKeyRow
	targets := []any{
		&row.id, &row.systemAccountID,
	}
	if includeOwner {
		targets = append(targets, &row.systemAccountName)
	}
	targets = append(targets,
		&row.routeStrategyID, &row.routeStrategyName, &row.routeStrategyMode, &row.routeStrategyStatus,
		&row.name, &row.description, &row.keyPrefix, &row.keySuffix, &row.status, &row.isDefault,
		&row.purpose, &row.expiresAt, &row.quotaJSON, &row.scheduleJSON, &row.updatedAt)
	err := scan(targets...)
	return row, err
}

// apiKeyJoin renders the route-strategy inner join with dialect tables.
func apiKeyJoin(s *Store) string {
	return ` INNER JOIN ` + s.table("route_strategies") + ` route_strategies
		ON route_strategies.id = api_keys.route_strategy_id
		AND route_strategies.system_account_id = api_keys.system_account_id`
}

// ListPage mirrors listApiKeysPageAsync (sqlite sync shape): scope + filters,
// is_default DESC then recency ordering, pageSize+1 probe and the paged total
// upper bound.
func (s *Store) ListPage(ctx context.Context, access AccessScope, options ListOptions) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	normalized := normalizeListOptions(options)
	clauses := []string{}
	args := []any{}
	if scoped := access.manageableID(); scoped != "" {
		clauses = append(clauses, "api_keys.system_account_id = ?")
		args = append(args, scoped)
	}
	if normalized.Keyword != "" {
		clauses = append(clauses, "(api_keys.name >= ? AND api_keys.name < ?)")
		args = append(args, normalized.Keyword, textPrefixUpperBound(normalized.Keyword))
	}
	if normalized.Status != "" {
		clauses = append(clauses, "api_keys.status = ?")
		args = append(args, normalized.Status)
	}
	if normalized.RouteStrategyID != "" {
		clauses = append(clauses, "api_keys.route_strategy_id = ?")
		args = append(args, normalized.RouteStrategyID)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	join := ""
	columns := "api_keys.id, api_keys.system_account_id, "
	if access.canAccessAll() {
		join += ` LEFT JOIN ` + s.table("system_accounts") + ` system_accounts
			ON system_accounts.id = api_keys.system_account_id`
		columns += "system_accounts.display_name AS system_account_name, "
	}
	columns += `api_keys.route_strategy_id, route_strategies.name AS route_strategy_name,
		route_strategies.mode AS route_strategy_mode, route_strategies.status AS route_strategy_status,
		api_keys.name, api_keys.description, api_keys.key_prefix, api_keys.key_suffix, api_keys.status,
		api_keys.is_default, api_keys.purpose, api_keys.expires_at, api_keys.quota_limits_json,
		api_keys.availability_schedule_json, api_keys.updated_at`
	args = append(args, normalized.PageSize+1, (normalized.Page-1)*normalized.PageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+columns+`
		FROM `+s.table("api_keys")+` api_keys`+join+apiKeyJoin(s)+where+`
		ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []apiKeyRow{}
	for rows.Next() {
		row, scanErr := scanAPIKeyRow(rows.Scan, access.canAccessAll())
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(records) > normalized.PageSize
	if hasMore {
		records = records[:normalized.PageSize]
	}
	items := make([]ListItem, 0, len(records))
	for _, row := range records {
		item, mapErr := s.newListItem(row, access)
		if mapErr != nil {
			return nil, mapErr
		}
		items = append(items, item)
	}
	total := (normalized.Page-1)*normalized.PageSize + len(items)
	if hasMore {
		total++
	}
	return &ListPageResult{
		Items: items, Total: total, HasMore: hasMore,
		Page: normalized.Page, PageSize: normalized.PageSize,
	}, nil
}

// normalizeListOptions mirrors normalizeApiKeyListOptions + integerQueryValue:
// integer pages beyond the window clamp, non-integer input falls back.
func normalizeListOptions(options ListOptions) ListOptions {
	pageSize := defaultAPIKeyListPageSize
	if options.PageSizeSet {
		pageSize = minInt(maxAPIKeyListPageSize, maxInt(1, options.PageSize))
	}
	pageCap := maxInt(1, defaultListWindowRowsTotal/pageSize)
	page := 1
	if options.PageSet {
		page = minInt(pageCap, maxInt(1, options.Page))
	}
	keyword := strings.TrimSpace(options.Keyword)
	status := options.Status
	if status != "active" && status != "disabled" {
		status = ""
	}
	return ListOptions{
		Page: page, PageSize: pageSize, Keyword: keyword, Status: status,
		RouteStrategyID: strings.TrimSpace(options.RouteStrategyID),
	}
}

const (
	defaultAPIKeyListPageSize  = 50
	maxAPIKeyListPageSize      = 200
	defaultListWindowRowsTotal = 1000 // pageUpperBoundForWindow(1001 rows)
)

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}

// newListItem mirrors apiKeyListItemsFromRows (usage zero value).
func (s *Store) newListItem(row apiKeyRow, access AccessScope) (ListItem, error) {
	schedule, err := ParseScheduleJSON(row.scheduleJSON.String)
	if err != nil {
		return ListItem{}, err
	}
	quotaLimits, err := ParseQuotaLimitsJSON(row.quotaJSON.String)
	if err != nil {
		return ListItem{}, err
	}
	item := ListItem{
		ID:                   row.id,
		Name:                 row.name,
		Description:          nullPtrString(row.description),
		KeyPrefix:            row.keyPrefix,
		KeySuffix:            row.keySuffix,
		Status:               row.status,
		IsDefault:            row.isDefault == 1,
		Purpose:              normalizePurpose(row.purpose),
		RouteStrategyID:      row.routeStrategyID,
		RouteStrategyName:    nullPtrString(row.routeStrategyName),
		RouteStrategyMode:    normalizedRouteStrategyMode(row.routeStrategyMode),
		RouteStrategyStatus:  normalizedRouteStrategyStatus(row.routeStrategyStatus),
		ExpiresAt:            nullPtrString(row.expiresAt),
		QuotaLimits:          quotaLimits,
		AvailabilitySchedule: schedule,
		Usage:                emptyListUsageSummary(),
		Revision:             row.updatedAt,
	}
	if access.canAccessAll() {
		item.SystemAccountID = &row.systemAccountID
		item.SystemAccountName = nullPtrString(row.systemAccountName)
	}
	return item, nil
}

func normalizePurpose(value sql.NullString) string {
	if value.Valid && value.String == "chat" {
		return "chat"
	}
	return "general"
}

// normalizedRouteStrategyMode mirrors the list mapper: NULL/empty stays
// omitted, unknown values surface as errors (normalizeRouteStrategyMode).
func normalizedRouteStrategyMode(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	switch value.String {
	case "normal", "hybrid_smart", "weighted", "failover", "round_robin":
		mode := value.String
		return &mode
	default:
		return nil
	}
}

func normalizedRouteStrategyStatus(value sql.NullString) *string {
	if value.Valid && (value.String == "active" || value.String == "disabled") {
		status := value.String
		return &status
	}
	return nil
}

// FindDetail mirrors findApiKeySummaryAsync (owner branch): nil when the key
// is missing or outside the access scope (route renders 404).
func (s *Store) FindDetail(ctx context.Context, id string, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	columns := "api_keys.id, api_keys.system_account_id, "
	join := ""
	if access.canAccessAll() {
		join += ` LEFT JOIN ` + s.table("system_accounts") + ` system_accounts
			ON system_accounts.id = api_keys.system_account_id`
		columns += "system_accounts.display_name AS system_account_name, "
	}
	columns += `api_keys.route_strategy_id, route_strategies.name AS route_strategy_name,
		route_strategies.mode AS route_strategy_mode, route_strategies.status AS route_strategy_status,
		api_keys.name, api_keys.description, api_keys.key_prefix, api_keys.key_suffix, api_keys.status,
		api_keys.is_default, api_keys.purpose, api_keys.expires_at, api_keys.quota_limits_json,
		api_keys.availability_schedule_json, api_keys.updated_at`
	where := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND api_keys.system_account_id = ?"
		args = append(args, scoped)
	}
	row, err := scanAPIKeyRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+columns+`
			FROM `+s.table("api_keys")+` api_keys`+join+apiKeyJoin(s)+`
			WHERE api_keys.id = ?`+where+` LIMIT 1`), args...).Scan(targets...)
	}, access.canAccessAll())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item, err := s.newListItem(row, access)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// SecretUnavailableError marks a scope-matched row whose sealed secret is
// absent or undecryptable (Node throws 'API Key 密文缺少完整密钥', rendered
// by the route as 500 API Key 密钥读取失败).
type SecretUnavailableError struct{}

func (e *SecretUnavailableError) Error() string { return "API Key 密文缺少完整密钥" }

// FindSecret mirrors findApiKeySecretAsync: scope-checked row plus AES-GCM
// decryption of key_secret_encrypted.
func (s *Store) FindSecret(ctx context.Context, id string, access AccessScope) (*SecretRecord, error) {
	ctx = ensureCtx(ctx)
	where := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND api_keys.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, ownerID, name, keyPrefix, keySuffix string
	var encrypted sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT api_keys.id, api_keys.system_account_id, api_keys.name,
			api_keys.key_prefix, api_keys.key_suffix, api_keys.key_secret_encrypted
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.id = ?`+where+` LIMIT 1`), args...).
		Scan(&rowID, &ownerID, &name, &keyPrefix, &keySuffix, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var payload secretPayload
	if !encrypted.Valid || strings.TrimSpace(encrypted.String) == "" {
		return nil, &SecretUnavailableError{}
	}
	if err := DecryptJSON(s.secret, encrypted.String, &payload); err != nil {
		return nil, &SecretUnavailableError{}
	}
	if payload.Key == "" {
		return nil, &SecretUnavailableError{}
	}
	return &SecretRecord{
		ID: rowID, SystemAccountID: ownerID, Name: name,
		KeyPrefix: keyPrefix, KeySuffix: keySuffix, Key: payload.Key,
	}, nil
}

// CreateInput is the validated create payload (apiKeyCreateSchema subset the
// store consumes); nil pointers mean the field was absent.
type CreateInput struct {
	Name                 string
	Description          *string
	RouteStrategyID      *string
	Status               *string
	ExpiresAt            *string
	QuotaLimits          any
	AvailabilitySchedule any
}

// Create mirrors createApiKeyRecordAsync: sealed random key, default-strategy
// fallback, owner-scoped unique name, quota-hourly binding sync. The plaintext
// key is returned once and only the hash + AES-GCM seal are persisted.
func (s *Store) Create(ctx context.Context, input CreateInput, access AccessScope) (*CreateResult, *CreateMeta, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := access.ownerID()
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, nil, &ValidationError{Message: "API Key 名称不能为空"}
	}
	description, err := normalizeOptionalDescription(input.Description)
	if err != nil {
		return nil, nil, err
	}
	expiresAt, err := normalizeOptionalExpiresAt(input.ExpiresAt)
	if err != nil {
		return nil, nil, err
	}
	quotaLimits, err := normalizeQuotaLimits(input.QuotaLimits, emptyQuotaLimits())
	if err != nil {
		return nil, nil, err
	}
	schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
	if err != nil {
		return nil, nil, err
	}
	requestedStatus := "active"
	if input.Status != nil {
		if *input.Status != "active" && *input.Status != "disabled" {
			return nil, nil, &ValidationError{Message: "API Key 状态无效"}
		}
		requestedStatus = *input.Status
	}
	status := requestedStatus
	if override, ok := ScheduleStatus(schedule, s.now()); ok {
		status = override
	}
	quotaJSON, quotaJSONValid := QuotaLimitsJSON(quotaLimits)
	quotaJSONValue := sql.NullString{}
	if quotaJSONValid {
		quotaJSONValue = sql.NullString{String: quotaJSON, Valid: true}
	}
	scheduleJSONValue := sql.NullString{}
	if rawSchedule, ok := ScheduleJSON(schedule); ok {
		scheduleJSONValue = sql.NullString{String: rawSchedule, Valid: true}
	}
	nextCheckAt := sql.NullString{}
	if rawNextCheck, ok := NextScheduleCheckAt(schedule, s.now()); ok {
		nextCheckAt = sql.NullString{String: rawNextCheck, Valid: true}
	}

	now := s.now()
	nowISO := isoMillis(now)
	revision := revisionFromMillis(now.UnixMilli())
	key := NewAPIKey()
	keyPrefix := key[:8]
	keySuffix := key[len(key)-8:]
	id := s.newI("key")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	strategy, err := s.resolveRouteStrategyForCreate(ctx, tx, ownerID, input.RouteStrategyID)
	if err != nil {
		return nil, nil, err
	}
	sealed, err := EncryptJSON(s.secret, secretPayload{Key: key})
	if err != nil {
		return nil, nil, err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("api_keys")+`
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json,
		 availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'general', ?, ?, ?, ?, ?, ?)`),
		id, ownerID, strategy.id, name, description, HashSecret(key), keyPrefix, keySuffix,
		sealed, status, expiresAt, quotaJSONValue, scheduleJSONValue, nextCheckAt, nowISO, revision)
	if err != nil {
		if duplicate := duplicateAPIKeyNameError(err, name); duplicate != nil {
			return nil, nil, duplicate
		}
		return nil, nil, err
	}
	if err := s.syncQuotaHourlyWindowBinding(ctx, tx, id, ownerID, quotaJSONValue, status == "active", revision); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &CreateResult{
			ID: id, Key: key, KeyPrefix: keyPrefix, KeySuffix: keySuffix, Revision: revision,
		}, &CreateMeta{
			OwnerSystemAccountID: ownerID,
			Name:                 name,
			Status:               status,
			RouteStrategyID:      strategy.id,
			AvailabilitySchedule: schedule,
		}, nil
}

// routeStrategyReference mirrors apiKeyRouteStrategyReference rows.
type routeStrategyReference struct {
	id     string
	name   string
	mode   sql.NullString
	status sql.NullString
}

// resolveRouteStrategyForCreate mirrors the create branch: absent input falls
// back to the preferred default (gpt default strategy over the owner's
// default enabled group); present input must exist, belong to the owner and
// be active.
func (s *Store) resolveRouteStrategyForCreate(ctx context.Context, q queryer, ownerID string, routeStrategyID *string) (routeStrategyReference, error) {
	if routeStrategyID == nil {
		preferred, err := s.findPreferredDefaultRouteStrategy(ctx, q, ownerID)
		if err != nil {
			return routeStrategyReference{}, err
		}
		if preferred == nil {
			return routeStrategyReference{}, &ValidationError{Message: "当前用户缺少可用的默认策略路由"}
		}
		return *preferred, nil
	}
	candidate := strings.TrimSpace(*routeStrategyID)
	if candidate == "" {
		return routeStrategyReference{}, &ValidationError{Message: "API Key 必须绑定策略路由"}
	}
	reference, err := s.routeStrategyReference(ctx, q, ownerID, candidate)
	if err != nil {
		return routeStrategyReference{}, err
	}
	if reference == nil {
		return routeStrategyReference{}, &ValidationError{Message: "API Key 绑定的策略路由不存在或不属于当前用户"}
	}
	if !reference.status.Valid || reference.status.String != "active" {
		return routeStrategyReference{}, &ValidationError{Message: "API Key 只能绑定启用状态的策略路由"}
	}
	return *reference, nil
}

// findPreferredDefaultRouteStrategy mirrors findPreferredDefaultRouteStrategy
// ReferenceAsync: the first active default strategy bound to an active group
// on the owner's default enabled gpt group, ordered by creation.
func (s *Store) findPreferredDefaultRouteStrategy(ctx context.Context, q queryer, ownerID string) (*routeStrategyReference, error) {
	var reference routeStrategyReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT route_strategies.id, route_strategies.name, route_strategies.mode, route_strategies.status
		FROM `+s.table("route_strategies")+` route_strategies
		INNER JOIN `+s.table("route_strategy_groups")+` route_strategy_groups
			ON route_strategy_groups.route_strategy_id = route_strategies.id
			AND route_strategy_groups.system_account_id = route_strategies.system_account_id
			AND route_strategy_groups.status = 'active'
		INNER JOIN `+s.table("groups")+` groups
			ON groups.id = route_strategy_groups.group_id
			AND groups.system_account_id = route_strategy_groups.system_account_id
			AND groups.enabled = 1
			AND groups.is_default = 1
		WHERE route_strategies.system_account_id = ?
			AND route_strategies.status = 'active'
			AND route_strategies.is_default = 1
			AND groups.provider_code = ?
		ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
		LIMIT 1`), ownerID, gptVendorCode).
		Scan(&reference.id, &reference.name, &reference.mode, &reference.status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reference, nil
}

// routeStrategyReference mirrors assertRouteStrategySelectableForApiKey's
// lookup (id + owner scoped).
func (s *Store) routeStrategyReference(ctx context.Context, q queryer, ownerID, strategyID string) (*routeStrategyReference, error) {
	var reference routeStrategyReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT route_strategies.id, route_strategies.name, route_strategies.mode, route_strategies.status
		FROM `+s.table("route_strategies")+` route_strategies
		WHERE route_strategies.id = ? AND route_strategies.system_account_id = ?
		LIMIT 1`), strategyID, ownerID).
		Scan(&reference.id, &reference.name, &reference.mode, &reference.status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reference, nil
}

// RefreshSecret mirrors refreshApiKeySecretForManagementAsync: optimistic-lock
// rotation of key_hash/prefix/suffix/sealed secret + revision, then the
// REQUIRED validation-cache invalidation (failure fails the operation).
func (s *Store) RefreshSecret(ctx context.Context, id string, access AccessScope) (*RefreshOutcome, error) {
	ctx = ensureCtx(ctx)
	key := NewAPIKey()
	keyHash := HashSecret(key)
	keyPrefix := key[:8]
	keySuffix := key[len(key)-8:]

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	where := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND api_keys.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, ownerID, name, keyHashColumn, previousKeyPrefix, previousKeySuffix, updatedAt string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT api_keys.id, api_keys.system_account_id, api_keys.name,
			api_keys.key_hash, api_keys.key_prefix, api_keys.key_suffix, api_keys.updated_at
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.id = ?`+where+` LIMIT 1`), args...).
		Scan(&rowID, &ownerID, &name, &keyHashColumn, &previousKeyPrefix, &previousKeySuffix, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revision, err := nextRevision(updatedAt, s.now())
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, secretPayload{Key: key})
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("api_keys")+`
		SET key_hash = ?, key_prefix = ?, key_suffix = ?, key_secret_encrypted = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`),
		keyHash, keyPrefix, keySuffix, sealed, revision, rowID, ownerID, updatedAt)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("API Key 已被其他操作修改，请刷新后重试")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	outcome := &RefreshOutcome{
		Result: CreateResult{
			ID: rowID, Key: key, KeyPrefix: keyPrefix, KeySuffix: keySuffix, Revision: revision,
		},
		OwnerSystemAccountID: ownerID,
		ResourceName:         name,
		PreviousKeyPrefix:    previousKeyPrefix,
		PreviousKeySuffix:    previousKeySuffix,
	}
	hashes := []string{keyHash}
	if keyHashColumn != "" {
		hashes = []string{keyHashColumn, keyHash}
	}
	if s.inval != nil {
		if err := s.inval.InvalidateValidation(id, ReasonAPIKeySecretRefreshed, hashes); err != nil {
			outcome.ValidationCacheError = err
		}
		// Triple invalidation: the runtime lookup and quota caches follow the
		// required validation flush (best effort).
		s.inval.InvalidateRuntime(id, ReasonAPIKeySecretRefreshed)
		s.inval.InvalidateQuota(id, ReasonAPIKeySecretRefreshed)
	}
	return outcome, nil
}

// Delete mirrors deleteApiKeyWithRelatedCleanupAsync: scope-checked hard
// delete with default/chat guards, quota binding reset and the
// api_key_record_cleanup_targets upsert inside one transaction, followed by
// the required validation-cache invalidation and best-effort lookup/quota
// invalidation.
func (s *Store) Delete(ctx context.Context, id string, access AccessScope) (*DeleteResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	where := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND api_keys.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, ownerID, name, keyHash string
	var isDefault int
	var purpose sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT api_keys.id, api_keys.system_account_id, api_keys.name,
			api_keys.key_hash, api_keys.is_default, api_keys.purpose
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.id = ?`+where+` LIMIT 1`), args...).
		Scan(&rowID, &ownerID, &name, &keyHash, &isDefault, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if purpose.Valid && purpose.String == "chat" {
		return nil, &ConflictError{Message: "AI 对话 API Key 不允许删除"}
	}
	if isDefault == 1 {
		return nil, &ConflictError{Message: "默认 API Key 不允许删除"}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("api_keys")+`
		WHERE id = ? AND system_account_id = ?`), rowID, ownerID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &DeleteResult{}, nil
	}
	if err := s.syncQuotaHourlyWindowBinding(ctx, tx, rowID, ownerID, sql.NullString{}, false, isoMillis(s.now())); err != nil {
		return nil, err
	}
	updatedAt := isoMillis(s.now())
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.datasetTable("api_key_record_cleanup_targets")+`
		(api_key_id, system_account_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(api_key_id) DO UPDATE SET
			system_account_id = excluded.system_account_id,
			updated_at = excluded.updated_at`), rowID, ownerID, updatedAt, updatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	outcome := &DeleteResult{
		Deleted:               true,
		CleanupTargetAPIKeyID: rowID,
		CleanupTargetOwnerID:  ownerID,
		OwnerSystemAccountID:  ownerID,
		ResourceName:          name,
	}
	if s.inval != nil {
		if err := s.inval.InvalidateValidation(rowID, ReasonAPIKeyDeleted, []string{keyHash}); err != nil {
			outcome.ValidationCacheError = err
		}
		s.inval.InvalidateRuntime(rowID, ReasonAPIKeyDeleted)
		s.inval.InvalidateQuota(rowID, ReasonAPIKeyDeleted)
	}
	return outcome, nil
}

// syncQuotaHourlyWindowBinding mirrors
// syncApiKeyRequestQuotaHourlyWindowScopeBinding: the api_key binding row is
// replaced; an active key with an enabled hourly quota keeps one row with its
// window hours, everything else leaves the table clean.
func (s *Store) syncQuotaHourlyWindowBinding(ctx context.Context, q queryer, apiKeyID, ownerID string, quotaJSON sql.NullString, active bool, timestamp string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("request_quota_hourly_window_scope_bindings")+`
		WHERE source_type = 'api_key' AND source_id = ?`), apiKeyID); err != nil {
		return err
	}
	if !active {
		return nil
	}
	limits, err := ParseQuotaLimitsJSON(quotaJSON.String)
	if err != nil {
		return err
	}
	if limits.Hourly == nil || !limits.Hourly.Enabled ||
		limits.Hourly.Hours < 1 || limits.Hourly.Hours > maxRequestQuotaHourlyWindowHours {
		return nil
	}
	_, err = q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("request_quota_hourly_window_scope_bindings")+`
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES (?, 'api_key', ?, 'api_key', ?, ?, ?, ?)
		ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
			source_type = excluded.source_type,
			source_id = excluded.source_id,
			window_hours = excluded.window_hours,
			updated_at = excluded.updated_at`),
		ownerID, apiKeyID, apiKeyID, limits.Hourly.Hours, timestamp, timestamp)
	return err
}

// normalizeOptionalDescription mirrors normalizeOptionalApiKeyDescription.
func normalizeOptionalDescription(value *string) (sql.NullString, error) {
	if value == nil {
		return sql.NullString{}, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return sql.NullString{}, nil
	}
	if len([]rune(trimmed)) > 200 {
		return sql.NullString{}, &ValidationError{Message: "API Key 说明不能超过 200 个字符"}
	}
	return sql.NullString{String: trimmed, Valid: true}, nil
}

// normalizeOptionalExpiresAt mirrors normalizeOptionalApiKeyExpiresAt: empty
// clears, otherwise an RFC3339 instant with explicit offset is required.
func normalizeOptionalExpiresAt(value *string) (sql.NullString, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return sql.NullString{}, nil
	}
	canonical, ok := canonicalRFC3339(*value)
	if !ok {
		return sql.NullString{}, &ValidationError{Message: "API Key 过期时间必须是有效时间字符串"}
	}
	return sql.NullString{String: canonical, Valid: true}, nil
}

// duplicateAPIKeyNameError mirrors isDuplicateApiKeyNameError.
func duplicateAPIKeyNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_api_keys_owner_name_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: api_keys.system_account_id, api_keys.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.api_keys.system_account_id, juhe_business.api_keys.name") {
		return &ConflictError{Message: "API Key 名称已存在：" + name}
	}
	return nil
}

// queryer abstracts *sql.DB / *sql.Tx so transactional paths never touch
// s.db while a transaction holds the single SQLite test connection.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}
