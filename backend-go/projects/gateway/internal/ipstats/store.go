// Package ipstats owns the M15 vertical slice: the client IP stats list read
// and the four client IP policy write endpoints ported from
// backend/src/modules/ip-stats/ip-stats.routes.ts,
// backend/src/storage/client-ip-stats-list.repository.ts and
// backend/src/storage/client-ip-policy.repository.ts.
//
// Data source: every table lives in the stats database (PostgreSQL schema
// juhe_stats; unqualified in SQLite). The list reads the pre-aggregated
// client_ip_registry + client_ip_usage_range_windows projection plus
// client_ip_policies for status labeling and readiness from stats_job_state —
// strictly read-only. Policy writes replace-then-insert inside one
// transaction so an IP keeps at most one active policy (the partial unique
// index idx_client_ip_policies_active_unique holds), and the production
// pre-aggregation writer/worker stays in Node: this package never writes
// client_ip_registry, daily tables, range windows, dirty markers or
// stats_job_state.
package ipstats

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ValidationError maps the Node repository throw-Error paths that the route
// family renders as 400 (IP 标识无效, IP 不存在, expiresAt 校验).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// PolicyInvalidator is the post-commit cache invalidation port (Node
// notifyClientIpPolicyCacheInvalidated + the W6 shared cache version bump for
// gateway:client-ip-policy-by-ip). *inval.Bus satisfies it; nil keeps the
// slice self-contained with no-op invalidation.
type PolicyInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicClientIPPolicy mirrors the Node shared cache name whose version bump
// invalidates the per-IP policy cache (W6 契约: gateway:client-ip-policy-by-ip).
const TopicClientIPPolicy = "gateway:client-ip-policy-by-ip"

// Policy statuses and types mirror ClientIpPolicyStatus / ClientIpPolicyType.
const (
	PolicyStatusActive   = "active"
	PolicyStatusDisabled = "disabled"
	PolicyTypeBlacklist  = "blacklist"
	PolicyTypeAllowlist  = "allowlist"
)

// List status filters mirror ClientIpPolicyFilter.
const (
	StatusAll         = "all"
	StatusNormal      = "normal"
	StatusBlacklisted = "blacklisted"
	StatusAllowlisted = "allowlisted"
)

// Range/readiness constants mirror usage-stats-helpers and
// client-ip-usage-range-windows.repository.
const (
	maxListWindowRows    = 1001
	maxRangeDays         = 31
	rangeWindowScopeType = "client_ip_range_window"
	rangeWindowJobName   = "client_ip_range_window_refresh"
)

var clientIPHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// TimezoneSource resolves usageStatsTimezone (Node usage-stats-helpers reads
// it from juhe_business.system_settings sys_admin/usageStatsTimezone).
type TimezoneSource func(ctx context.Context) (string, error)

// Store is the dual-mode (SQLite + PostgreSQL) client IP stats persistence.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(prefix string) string
	inval PolicyInvalidator
	tz    TimezoneSource
	// detailAccounts backs the M15 detail endpoint's account/owner name
	// hydration (business database tables). nil degrades to nameless rows.
	detailAccounts DetailAccountLookup
}

// NewStore builds the store; inval and tz may be nil (tz falls back to the
// system_settings source on the same handle, inval becomes a no-op).
func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval PolicyInvalidator, tz TimezoneSource) (*Store, error) {
	if db == nil {
		return nil, errors.New("ipstats store requires a stats database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomID
	}
	if tz == nil {
		tz = NewSystemSettingsTimezoneSource(db, postgres)
	}
	return &Store{db: db, pg: postgres, now: now, newID: newID, inval: inval, tz: tz}, nil
}

// randomID mirrors Node newId('ip_policy') (random hex suffix).
func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

// table qualifies stats-schema tables for PostgreSQL.
func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

// bind rewrites ? placeholders to $N for PostgreSQL.
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
	return fmt.Sprintf("%d", v)
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

// invalidatePolicyCache mirrors notifyClientIpPolicyCacheInvalidated: the
// post-commit, best-effort shared cache version bump.
func (s *Store) invalidatePolicyCache(reason string) {
	if s.inval != nil {
		s.inval.Invalidate(TopicClientIPPolicy, reason)
	}
}

// NewSystemSettingsTimezoneSource reads usageStatsTimezone from the business
// system_settings table (Node usageStatsTimezoneAsync PostgreSQL branch reads
// juhe_business.system_settings; SQLite reads the unqualified table).
func NewSystemSettingsTimezoneSource(db *sql.DB, postgres bool) TimezoneSource {
	table := "system_settings"
	if postgres {
		table = "juhe_business.system_settings"
	}
	return func(ctx context.Context) (string, error) {
		ctx = ensureCtx(ctx)
		var raw sql.NullString
		err := db.QueryRowContext(ctx, `SELECT value_json FROM `+table+`
			WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !raw.Valid) {
			return "", errors.New("系统设置缺少 usageStatsTimezone")
		}
		if err != nil {
			return "", err
		}
		var value any
		if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
			return "", fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", err.Error())
		}
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return "", errors.New("系统设置 usageStatsTimezone 无效")
		}
		if _, err := time.LoadLocation(strings.TrimSpace(name)); err != nil {
			return "", fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", name)
		}
		return strings.TrimSpace(name), nil
	}
}

// Range mirrors AccountUsageStatsRange (normalized response field).
type Range struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

// UsageSummary mirrors ClientIpUsageSummary.
type UsageSummary struct {
	RequestCount        int64    `json:"requestCount"`
	SuccessCount        int64    `json:"successCount"`
	ErrorCount          int64    `json:"errorCount"`
	ErrorRate           float64  `json:"errorRate"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	CacheReadCost       float64  `json:"cacheReadCost"`
	CacheWriteTokens    int64    `json:"cacheWriteTokens"`
	CacheWrite1hTokens  int64    `json:"cacheWrite1hTokens"`
	CacheWriteCost      float64  `json:"cacheWriteCost"`
	ThinkingTokens      int64    `json:"thinkingTokens"`
	InputImageTokens    int64    `json:"inputImageTokens"`
	OutputImageTokens   int64    `json:"outputImageTokens"`
	TotalTokens         int64    `json:"totalTokens"`
	TotalCost           float64  `json:"totalCost"`
	ActiveDays          int64    `json:"activeDays"`
	AverageDurationMs   *float64 `json:"averageDurationMs,omitempty"`
	AverageFirstTokenMs *float64 `json:"averageFirstTokenMs,omitempty"`
	MaxDurationMs       *int64   `json:"maxDurationMs,omitempty"`
	LastUsedAt          *string  `json:"lastUsedAt,omitempty"`
	LastErrorAt         *string  `json:"lastErrorAt,omitempty"`
}

// ListRow mirrors ClientIpStatsRow.
type ListRow struct {
	IPHash         string       `json:"ipHash"`
	AggregateIPKey string       `json:"aggregateIpKey"`
	LastSeenAt     *string      `json:"lastSeenAt,omitempty"`
	Status         string       `json:"status"`
	RangeUsage     UsageSummary `json:"rangeUsage"`
}

// ListResult mirrors ClientIpStatsListResult.
type ListResult struct {
	Items          []ListRow `json:"items"`
	PageUpperBound int       `json:"pageUpperBound"`
	HasMore        bool      `json:"hasMore"`
	Page           int       `json:"page"`
	PageSize       int       `json:"pageSize"`
	Range          Range     `json:"range"`
	RangeReady     bool      `json:"rangeReady"`
}

// ListOptions mirrors the validated ClientIpStatsListOptions the route builds
// from the query string. Page/PageSize arrive already clamped; enums already
// validated; raw dates may be empty or invalid (they fall back to today).
type ListOptions struct {
	Page              int
	PageSize          int
	Keyword           string
	Status            string
	StartDate         string
	EndDate           string
	LastUsedStartDate string
	LastUsedEndDate   string
	SortField         string
	SortOrder         string
	LastUsedSortScope string
}

// PolicySummary mirrors ClientIpPolicySummary.
type PolicySummary struct {
	ID                        string  `json:"id"`
	IPHash                    string  `json:"ipHash"`
	PolicyType                string  `json:"policyType"`
	Status                    string  `json:"status"`
	Reason                    *string `json:"reason,omitempty"`
	ExpiresAt                 *string `json:"expiresAt,omitempty"`
	CreatedBySystemAccountID  string  `json:"createdBySystemAccountId"`
	CreatedAt                 string  `json:"createdAt"`
	UpdatedAt                 string  `json:"updatedAt"`
	DisabledAt                *string `json:"disabledAt,omitempty"`
	DisabledBySystemAccountID *string `json:"disabledBySystemAccountId,omitempty"`
	DisabledReason            *string `json:"disabledReason,omitempty"`
}

// PolicyMutationInput mirrors ClientIpPolicyMutationInput.
type PolicyMutationInput struct {
	IPHash               string
	PolicyType           string
	Reason               *string
	ExpiresAt            *string
	ActorSystemAccountID string
}

// PolicyDisableInput mirrors ClientIpPolicyDisableInput.
type PolicyDisableInput struct {
	IPHash               string
	PolicyType           string // empty = every type
	Reason               *string
	ActorSystemAccountID string
}

// DisableResult mirrors { disabledCount }.
type DisableResult struct {
	DisabledCount int64 `json:"disabledCount"`
}

// NormalizeIPHash mirrors normalizeIpHash: trim, lowercase, 64 hex digits.
func NormalizeIPHash(value string) (string, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	if !clientIPHashPattern.MatchString(text) {
		return "", &ValidationError{Message: "IP 标识无效"}
	}
	return text, nil
}

// normalizePolicyType mirrors normalizeClientIpPolicyType.
func normalizePolicyType(value string) string {
	if value == PolicyTypeAllowlist {
		return PolicyTypeAllowlist
	}
	return PolicyTypeBlacklist
}

// optionalText mirrors normalizeOptionalText: trimmed or nil.
func optionalText(value *string) *string {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil
	}
	return &text
}

// optionalInstant validates the optional RFC3339 instant (Z or numeric
// offset) exactly like optionalRfc3339Instant.
func optionalInstant(value *string, label string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
		return nil, &ValidationError{Message: label + "必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	return &text, nil
}

func activePolicyReplacementReason(nextPolicyType string) string {
	if nextPolicyType == PolicyTypeAllowlist {
		return "被新的白名单策略替换"
	}
	return "被新的封禁策略替换"
}

// CreatePolicy mirrors createClientIpPolicy: verify the registry row, disable
// every active policy of the IP, insert the replacement active policy — all
// inside one transaction — then re-read and return the summary.
func (s *Store) CreatePolicy(ctx context.Context, input PolicyMutationInput) (*PolicySummary, error) {
	ctx = ensureCtx(ctx)
	ipHash, err := NormalizeIPHash(input.IPHash)
	if err != nil {
		return nil, err
	}
	policyType := normalizePolicyType(input.PolicyType)
	if strings.TrimSpace(input.ActorSystemAccountID) == "" {
		return nil, &ValidationError{Message: "缺少操作者上下文"}
	}
	expiresAt, err := optionalInstant(input.ExpiresAt, "Client-IP 策略 expiresAt")
	if err != nil {
		return nil, err
	}
	reason := optionalText(input.Reason)
	now := s.nowISO()
	id := s.newID("ip_policy")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	var registryHash string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT ip_hash FROM `+s.table("client_ip_registry")+`
		WHERE ip_hash = ?`)+lockSuffix, ipHash).Scan(&registryHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "IP 不存在"}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("client_ip_policies")+`
		SET status = 'disabled',
		  disabled_at = ?,
		  disabled_by_system_account_id = ?,
		  disabled_reason = ?,
		  updated_at = ?
		WHERE ip_hash = ?
		  AND status = 'active'`), now, input.ActorSystemAccountID, activePolicyReplacementReason(policyType), now, ipHash); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("client_ip_policies")+`
		(id, ip_hash, policy_type, status, reason, expires_at,
		 created_by_system_account_id, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?)`),
		id, ipHash, policyType, reason, expiresAt, input.ActorSystemAccountID, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.loadPolicy(ctx, id)
	if err != nil {
		return nil, err
	}
	s.invalidatePolicyCache("client_ip_policy_created")
	return summary, nil
}

func (s *Store) loadPolicy(ctx context.Context, id string) (*PolicySummary, error) {
	var (
		row        PolicySummary
		reason     sql.NullString
		expiresAt  sql.NullString
		createdAt  string
		updatedAt  string
		disabledAt sql.NullString
		disabledBy sql.NullString
		disabledRe sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, ip_hash, policy_type, status, reason, expires_at,
		created_by_system_account_id, created_at, updated_at, disabled_at, disabled_by_system_account_id, disabled_reason
		FROM `+s.table("client_ip_policies")+` WHERE id = ?`), id).Scan(
		&row.ID, &row.IPHash, &row.PolicyType, &row.Status, &reason, &expiresAt,
		&row.CreatedBySystemAccountID, &createdAt, &updatedAt, &disabledAt, &disabledBy, &disabledRe)
	if err != nil {
		return nil, err
	}
	row.PolicyType = normalizePolicyType(row.PolicyType)
	if row.Status != PolicyStatusDisabled {
		row.Status = PolicyStatusActive
	}
	row.Reason = nullText(reason)
	row.ExpiresAt = nullText(expiresAt)
	row.CreatedAt = createdAt
	row.UpdatedAt = updatedAt
	row.DisabledAt = nullText(disabledAt)
	row.DisabledBySystemAccountID = nullText(disabledBy)
	row.DisabledReason = nullText(disabledRe)
	return &row, nil
}

func nullText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

// DisablePolicies mirrors disableClientIpPolicies: one bounded UPDATE over
// the active policies of the IP (optionally one type) and the affected row
// count. The Node repository does not consult the registry here; a missing
// policy simply yields disabledCount=0.
func (s *Store) DisablePolicies(ctx context.Context, input PolicyDisableInput) (*DisableResult, error) {
	ctx = ensureCtx(ctx)
	ipHash, err := NormalizeIPHash(input.IPHash)
	if err != nil {
		return nil, err
	}
	policyType := ""
	if input.PolicyType != "" {
		policyType = normalizePolicyType(input.PolicyType)
	}
	now := s.nowISO()
	disabledReason := optionalText(input.Reason)
	if disabledReason == nil {
		disabledReason = ptrString("管理员解除策略")
	}
	query := `UPDATE ` + s.table("client_ip_policies") + `
		SET status = 'disabled',
		  disabled_at = ?,
		  disabled_by_system_account_id = ?,
		  disabled_reason = ?,
		  updated_at = ?
		WHERE ip_hash = ?
		  AND status = 'active'`
	args := []any{now, input.ActorSystemAccountID, disabledReason, now, ipHash}
	if policyType != "" {
		query += ` AND policy_type = ?`
		args = append(args, policyType)
	}
	result, err := s.db.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	s.invalidatePolicyCache("client_ip_policies_disabled")
	return &DisableResult{DisabledCount: affected}, nil
}

func ptrString(value string) *string { return &value }
