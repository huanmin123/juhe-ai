// Package settings owns the M12 vertical slice: the system_settings
// repository port of backend/src/storage/settings.repository.ts plus the
// /settings route family from backend/src/modules/settings/settings.routes.ts
// and system-api-app.ts. The slice covers the full systemSettingKeys
// whitelist read (GET /settings, requireAdmin), the strict-key PATCH with
// per-key value validation and the online usageStatsTimezone guard, the
// login-free GET /settings/public global-brand subset, the
// settings:system snapshot provider for later ratelimit/jobs wiring, the
// /settings/global brand family and the /settings/sections/:key management
// sections (managementSettingsSectionCatalog).
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// ValidationError maps to the throw-Error paths of settings.repository.ts
// that the route renders as 400 badRequest(error.message).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it; nil
// keeps the slice self-contained with no-op invalidation.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicGatewayRuntime mirrors the Node gateway runtime cache topic constant.
const TopicGatewayRuntime = "topic:gateway_runtime_cache"

// settingsUpdatedReason is the fixed invalidation reason Node passes for
// settings writes.
const settingsUpdatedReason = "settings_updated"

// SystemSettingsAccountID mirrors SYSTEM_SETTINGS_ACCOUNT_ID.
const SystemSettingsAccountID = "sys_admin"

// settingsCacheTTL mirrors settingsCacheTtlMs (60s app cache).
const settingsCacheTTL = 60 * time.Second

// SystemSettingKeys mirrors systemSettingKeys in settings.repository.ts
// (order preserved). Note: the current Node source carries 60 keys; the "53
// key" figure in the migration plan predates later key additions and is
// superseded by this list.
var SystemSettingKeys = []string{
	"gatewayTextRawBodyLimitMegabytes",
	"accountCircuitConfirmationFailuresRequired",
	"gatewayUserRequestLimitPerMinute",
	"gatewayUserRequestLimitPerDay",
	"gatewayUserRequestLimitPerWeek",
	"gatewayUserRequestLimitPerMonth",
	"userAiAccountLimit",
	"systemApiRateLimitIpReadPerMinute",
	"systemApiRateLimitIpReadBurstPer10Seconds",
	"systemApiRateLimitIpWritePerMinute",
	"systemApiRateLimitIpWriteBurstPer10Seconds",
	"systemApiRateLimitUserReadPerMinute",
	"systemApiRateLimitUserWritePerMinute",
	"defaultTemporaryUnschedulableMinutes",
	"temporaryUnschedulableRetryIntervalSeconds",
	"temporaryUnschedulableRetryAttempts",
	"textFirstResponseTimeoutSeconds",
	"textStreamIdleTimeoutSeconds",
	"textUncommittedAttemptMaxLifetimeSeconds",
	"imageFirstResponseTimeoutSeconds",
	"imageStreamIdleTimeoutSeconds",
	"imageUncommittedAttemptMaxLifetimeSeconds",
	"imageRequestWallTimeoutSeconds",
	"chatImageGenerationTotalTimeoutSeconds",
	"noAvailableAccountWaitTimeoutSeconds",
	"streamFailureThresholdCount",
	"streamFailureThresholdWindowMinutes",
	"operationLogRetentionDays",
	"operationLogMaxChangesPerRecord",
	"statsAggregationIntervalSeconds",
	"statsAggregationBatchSize",
	"statsAggregationMaxBatchesPerRun",
	"usageHotWindowRefreshIntervalSeconds",
	"groupAccountStatsRefreshIntervalSeconds",
	"systemMetricsSampleIntervalSeconds",
	"tableMonitorMaxTablesPerRun",
	"accountQualityRefreshIntervalSeconds",
	"accountQualityWindowMinutes",
	"accountHealthCheckIntervalHours",
	"accountHealthCheckJitterMinutes",
	"accountHealthCheckFailureThreshold",
	"cooldownAccountRetestIntervalSeconds",
	"cooldownAccountRetestMaxBackoffHours",
	"oauthAccessTokenRefreshIntervalSeconds",
	"oauthAccessTokenRefreshLeadSeconds",
	"oauthAccessTokenRefreshBatchSize",
	"oauthAccessTokenRefreshRetryBackoffSeconds",
	"modelCheckRetentionDays",
	"runtimeLogIndexRetentionDays",
	"publicApiLogRetentionDays",
	"usageRecordRetentionDays",
	"usageStatsTimezone",
	"usageStatsMinuteRetentionHours",
	"usageStatsHourlyRetentionDays",
	"usageStatsDailyRetentionDays",
	"usageStatsWeeklyRetentionWeeks",
	"usageStatsMonthlyRetentionMonths",
	"usageRankSnapshotRetentionDays",
	"systemMetricsRetentionDays",
	"systemMetricsHourlyRetentionDays",
}

var systemSettingKeySet = func() map[string]bool {
	set := make(map[string]bool, len(SystemSettingKeys))
	for _, key := range SystemSettingKeys {
		set[key] = true
	}
	return set
}()

// settingSpec mirrors one SYSTEM_SETTING_VALIDATORS entry: either an integer
// with a closed range or the timezone validator.
type settingSpec struct {
	integer bool
	min     int
	max     int
}

// systemSettingSpecs mirrors SYSTEM_SETTING_VALIDATORS (integerSetting(min,max)
// plus timezoneSetting for usageStatsTimezone).
var systemSettingSpecs = map[string]settingSpec{
	"gatewayTextRawBodyLimitMegabytes":           {integer: true, min: 1, max: 64},
	"accountCircuitConfirmationFailuresRequired": {integer: true, min: 1, max: 5},
	"gatewayUserRequestLimitPerMinute":           {integer: true, min: 0, max: 1000000000},
	"gatewayUserRequestLimitPerDay":              {integer: true, min: 0, max: 1000000000},
	"gatewayUserRequestLimitPerWeek":             {integer: true, min: 0, max: 1000000000},
	"gatewayUserRequestLimitPerMonth":            {integer: true, min: 0, max: 1000000000},
	"userAiAccountLimit":                         {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitIpReadPerMinute":          {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitIpReadBurstPer10Seconds":  {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitIpWritePerMinute":         {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitIpWriteBurstPer10Seconds": {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitUserReadPerMinute":        {integer: true, min: 0, max: 1000000},
	"systemApiRateLimitUserWritePerMinute":       {integer: true, min: 0, max: 1000000},
	"defaultTemporaryUnschedulableMinutes":       {integer: true, min: 1, max: 1440},
	"temporaryUnschedulableRetryIntervalSeconds": {integer: true, min: 0, max: 3600},
	"temporaryUnschedulableRetryAttempts":        {integer: true, min: 0, max: 10},
	"textFirstResponseTimeoutSeconds":            {integer: true, min: 10, max: 3600},
	"textStreamIdleTimeoutSeconds":               {integer: true, min: 1, max: 3600},
	"textUncommittedAttemptMaxLifetimeSeconds":   {integer: true, min: 60, max: 86400},
	"imageFirstResponseTimeoutSeconds":           {integer: true, min: 10, max: 3600},
	"imageStreamIdleTimeoutSeconds":              {integer: true, min: 1, max: 3600},
	"imageUncommittedAttemptMaxLifetimeSeconds":  {integer: true, min: 60, max: 86400},
	"imageRequestWallTimeoutSeconds":             {integer: true, min: 60, max: 86400},
	"chatImageGenerationTotalTimeoutSeconds":     {integer: true, min: 60, max: 86400},
	"noAvailableAccountWaitTimeoutSeconds":       {integer: true, min: 10, max: 3600},
	"streamFailureThresholdCount":                {integer: true, min: 1, max: 100},
	"streamFailureThresholdWindowMinutes":        {integer: true, min: 1, max: 1440},
	"operationLogRetentionDays":                  {integer: true, min: 1, max: 3650},
	"operationLogMaxChangesPerRecord":            {integer: true, min: 1, max: 500},
	"statsAggregationIntervalSeconds":            {integer: true, min: 5, max: 3600},
	"statsAggregationBatchSize":                  {integer: true, min: 100, max: 10000},
	"statsAggregationMaxBatchesPerRun":           {integer: true, min: 1, max: 100},
	"usageHotWindowRefreshIntervalSeconds":       {integer: true, min: 60, max: 3600},
	"groupAccountStatsRefreshIntervalSeconds":    {integer: true, min: 5, max: 3600},
	"systemMetricsSampleIntervalSeconds":         {integer: true, min: 5, max: 3600},
	"tableMonitorMaxTablesPerRun":                {integer: true, min: 0, max: 100},
	"accountQualityRefreshIntervalSeconds":       {integer: true, min: 60, max: 3600},
	"accountQualityWindowMinutes":                {integer: true, min: 1, max: 60},
	"accountHealthCheckIntervalHours":            {integer: true, min: 1, max: 168},
	"accountHealthCheckJitterMinutes":            {integer: true, min: 0, max: 1440},
	"accountHealthCheckFailureThreshold":         {integer: true, min: 1, max: 10},
	"cooldownAccountRetestIntervalSeconds":       {integer: true, min: 1, max: 3600},
	"cooldownAccountRetestMaxBackoffHours":       {integer: true, min: 1, max: 720},
	"oauthAccessTokenRefreshIntervalSeconds":     {integer: true, min: 10, max: 3600},
	"oauthAccessTokenRefreshLeadSeconds":         {integer: true, min: 60, max: 86400},
	"oauthAccessTokenRefreshBatchSize":           {integer: true, min: 1, max: 200},
	"oauthAccessTokenRefreshRetryBackoffSeconds": {integer: true, min: 0, max: 86400},
	"modelCheckRetentionDays":                    {integer: true, min: 1, max: 365},
	"runtimeLogIndexRetentionDays":               {integer: true, min: 1, max: 90},
	"publicApiLogRetentionDays":                  {integer: true, min: 1, max: 365},
	"usageRecordRetentionDays":                   {integer: true, min: 1, max: 180},
	"usageStatsTimezone":                         {integer: false},
	"usageStatsMinuteRetentionHours":             {integer: true, min: 1, max: 24 * 14},
	"usageStatsHourlyRetentionDays":              {integer: true, min: 1, max: 180},
	"usageStatsDailyRetentionDays":               {integer: true, min: 1, max: 800},
	"usageStatsWeeklyRetentionWeeks":             {integer: true, min: 1, max: 260},
	"usageStatsMonthlyRetentionMonths":           {integer: true, min: 1, max: 60},
	"usageRankSnapshotRetentionDays":             {integer: true, min: 1, max: 365},
	"systemMetricsRetentionDays":                 {integer: true, min: 1, max: 7},
	"systemMetricsHourlyRetentionDays":           {integer: true, min: 1, max: 30},
}

// compatibleSystemSettingDefaults mirrors compatibleSystemSettingDefaults:
// legacy databases may miss these rows and the loader fills them in.
var compatibleSystemSettingDefaults = map[string]int{
	"gatewayUserRequestLimitPerMinute": 0,
	"gatewayUserRequestLimitPerDay":    0,
	"gatewayUserRequestLimitPerWeek":   0,
	"gatewayUserRequestLimitPerMonth":  0,
	"userAiAccountLimit":               100,
}

// GlobalSettingKeys mirrors globalSettingKeys — the brand subset served by
// GET /settings/public (Node listPublicGlobalSettings).
var GlobalSettingKeys = []string{"appName", "appIcon"}

var globalSettingKeySet = func() map[string]bool {
	set := make(map[string]bool, len(GlobalSettingKeys))
	for _, key := range GlobalSettingKeys {
		set[key] = true
	}
	return set
}()

// Settings section storage domains (managementSettingsSectionCatalog.domain).
const (
	settingsSectionDomainGlobal = "global"
	settingsSectionDomainSystem = "system"
)

// ManagementSettingsSection mirrors one managementSettingsSectionCatalog
// entry: the storage domain plus the exact setting key list served and
// accepted for the section.
type ManagementSettingsSection struct {
	Domain string
	Keys   []string
}

// ManagementSettingsSectionCatalog mirrors managementSettingsSectionCatalog
// (settings.repository.ts): the seven management sections and their key
// lists. brand lives in global_settings, everything else in system_settings.
var ManagementSettingsSectionCatalog = map[string]ManagementSettingsSection{
	"brand": {Domain: settingsSectionDomainGlobal, Keys: GlobalSettingKeys},
	"gateway-core": {Domain: settingsSectionDomainSystem, Keys: []string{
		"gatewayTextRawBodyLimitMegabytes",
		"accountCircuitConfirmationFailuresRequired",
		"defaultTemporaryUnschedulableMinutes",
		"temporaryUnschedulableRetryIntervalSeconds",
		"temporaryUnschedulableRetryAttempts",
		"textFirstResponseTimeoutSeconds",
		"textStreamIdleTimeoutSeconds",
		"textUncommittedAttemptMaxLifetimeSeconds",
		"imageFirstResponseTimeoutSeconds",
		"imageStreamIdleTimeoutSeconds",
		"imageUncommittedAttemptMaxLifetimeSeconds",
		"imageRequestWallTimeoutSeconds",
		"chatImageGenerationTotalTimeoutSeconds",
		"noAvailableAccountWaitTimeoutSeconds",
	}},
	"user-request-limit": {Domain: settingsSectionDomainSystem, Keys: []string{
		"gatewayUserRequestLimitPerMinute",
		"gatewayUserRequestLimitPerDay",
		"gatewayUserRequestLimitPerWeek",
		"gatewayUserRequestLimitPerMonth",
		"userAiAccountLimit",
	}},
	"account-health": {Domain: settingsSectionDomainSystem, Keys: []string{
		"accountHealthCheckIntervalHours",
		"accountHealthCheckJitterMinutes",
		"accountHealthCheckFailureThreshold",
	}},
	"api-rate-limit": {Domain: settingsSectionDomainSystem, Keys: []string{
		"systemApiRateLimitIpReadPerMinute",
		"systemApiRateLimitIpReadBurstPer10Seconds",
		"systemApiRateLimitIpWritePerMinute",
		"systemApiRateLimitIpWriteBurstPer10Seconds",
		"systemApiRateLimitUserReadPerMinute",
		"systemApiRateLimitUserWritePerMinute",
	}},
	"cooldown-retest": {Domain: settingsSectionDomainSystem, Keys: []string{
		"cooldownAccountRetestMaxBackoffHours",
	}},
	"data-retention": {Domain: settingsSectionDomainSystem, Keys: []string{
		"usageRecordRetentionDays",
		"runtimeLogIndexRetentionDays",
		"publicApiLogRetentionDays",
	}},
}

// ManagementSettingsSectionKeys mirrors the managementSettingsSectionCatalog
// insertion order for deterministic iteration.
var ManagementSettingsSectionKeys = []string{
	"brand",
	"gateway-core",
	"user-request-limit",
	"account-health",
	"api-rate-limit",
	"cooldown-retest",
	"data-retention",
}

// UnknownSettingsSectionError mirrors InvalidSettingsSectionError
// (settings.routes.ts): the GET section route renders it as 400 while every
// other failure takes next(error) — the generic 500.
type UnknownSettingsSectionError struct{ Key string }

func (e *UnknownSettingsSectionError) Error() string { return "未知设置分区：" + e.Key }

// resolveSettingsSection mirrors parseSectionKey: only catalog keys pass.
func resolveSettingsSection(value string) (ManagementSettingsSection, error) {
	section, ok := ManagementSettingsSectionCatalog[value]
	if !ok {
		return ManagementSettingsSection{}, &UnknownSettingsSectionError{Key: value}
	}
	return section, nil
}

// usageStatsDataTables mirrors the usageStatsDataExists probe list.
var usageStatsDataTables = []string{
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	"authorization_team_usage_summary_daily",
	"authorization_team_usage_range_windows",
	"authorization_user_usage_summary_daily",
	"authorization_user_usage_range_windows",
	"usage_overview_summary_windows",
	"usage_overview_trend_windows",
	"usage_model_rank_windows",
	"usage_error_rank_windows",
	"ai_performance_summary_windows",
	"usage_quota_hourly_windows",
	"usage_scope_range_windows",
	"system_metrics_trend_windows",
}

// Store is the dual-mode settings persistence behind the M12 route family.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	inval RuntimeInvalidator
	// statsDB backs the usageStatsTimezone guard probe. Node runs
	// usageStatsDataExists against getStatsDatabase(); in the six-database
	// split the usage_stats_* tables live in the dedicated stats database,
	// never the business file. Optional: nil keeps the single-handle legacy
	// behavior.
	statsDB *sql.DB

	mu             sync.Mutex
	cached         map[string]any
	cachedAt       time.Time
	globalCached   map[string]any
	globalCachedAt time.Time
}

// NewStore builds the store; inval may be nil (no-op invalidation until K5
// wires the bus).
func NewStore(db *sql.DB, postgres bool, now func() time.Time, inval RuntimeInvalidator) (*Store, error) {
	if db == nil {
		return nil, errors.New("settings store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, pg: postgres, now: now, inval: inval}, nil
}

// SetStatsDatabase points the usageStatsDataExists probe at the dedicated
// stats database (Node getStatsDatabase(), SQLite six-database split). The
// composition wires the same stats handle ipstats reads; PostgreSQL mode never
// probes (the guard rejects online timezone changes first), so callers there
// can leave it unset.
func (s *Store) SetStatsDatabase(db *sql.DB) {
	s.statsDB = db
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
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

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// SettingsProvider supplies the full settings:system snapshot for packages
// that adapt system settings into their own typed configuration (ratelimit,
// background jobs). *Store implements it with the 60s TTL snapshot cache.
type SettingsProvider interface {
	SettingsSnapshot(ctx context.Context) (map[string]any, error)
}

var _ SettingsProvider = (*Store)(nil)

// SettingsSnapshot returns the cached settings:system snapshot (loading it on
// miss/expire). Consumers copy or read-only scan the returned map.
func (s *Store) SettingsSnapshot(ctx context.Context) (map[string]any, error) {
	return s.Load(ctx)
}

// Load mirrors getSettingsAsync (memory-cache branch): full whitelist read
// for sys_admin, per-key normalization, compatible defaults and the
// all-keys-present assertion. A stored unknown/invalid/missing row is a
// storage anomaly the route renders as 500.
func (s *Store) Load(ctx context.Context) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	s.mu.Lock()
	if s.cached != nil && s.now().Sub(s.cachedAt) < settingsCacheTTL {
		cached := s.cached
		s.mu.Unlock()
		return copySettings(cached), nil
	}
	s.mu.Unlock()

	settings, err := s.loadFromDatabase(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cached = settings
	s.cachedAt = s.now()
	s.mu.Unlock()
	return copySettings(settings), nil
}

func (s *Store) loadFromDatabase(ctx context.Context) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT key, value_json FROM `+s.table("system_settings")+`
		WHERE system_account_id = ? AND key IN (`+placeholders(len(SystemSettingKeys))+`)
		ORDER BY key ASC`), queryArgs(SystemSettingKeys, SystemSettingsAccountID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]any{}
	for rows.Next() {
		var key, valueJSON string
		if err := rows.Scan(&key, &valueJSON); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
			return nil, err
		}
		normalized, err := normalizeSystemSetting(key, value)
		if err != nil {
			return nil, err
		}
		settings[key] = normalized
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	applyCompatibleSystemSettingDefaults(settings, SystemSettingKeys)
	assertAllSettingsPresent(settings, SystemSettingKeys, "系统设置")
	return settings, nil
}

func queryArgs(keys []string, first string) []any {
	args := make([]any, 0, len(keys)+1)
	if first != "" {
		args = append(args, first)
	}
	for _, key := range keys {
		args = append(args, key)
	}
	return args
}

// Update mirrors updateSettingsAsync: strict whitelist + value validation,
// the online usageStatsTimezone guard, a single upsert transaction, cache
// clear + gateway runtime invalidation, then the fresh full snapshot.
func (s *Store) Update(ctx context.Context, input map[string]any) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	normalized, err := normalizeSystemSettingsInput(input)
	if err != nil {
		return nil, err
	}
	if err := s.assertUsageStatsTimezoneUpdateAllowed(ctx, normalized); err != nil {
		return nil, err
	}
	if err := s.upsertSystemSettings(ctx, normalized); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cached = nil
	s.cachedAt = time.Time{}
	s.mu.Unlock()
	// Node notifyGatewayRuntimeCacheInvalidation('settings_updated').
	if s.inval != nil {
		s.inval.Invalidate(TopicGatewayRuntime, settingsUpdatedReason)
	}
	return s.refreshSystemCache(ctx)
}

// upsertSystemSettings persists normalized system settings in a single
// transaction (the updateSettingsAsync / updateManagementSettingsSectionAsync
// system branch upsert).
func (s *Store) upsertSystemSettings(ctx context.Context, normalized map[string]any) error {
	keys := sortedKeys(normalized)
	nowISO := s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range keys {
		valueJSON, marshalErr := json.Marshal(normalized[key])
		if marshalErr != nil {
			return marshalErr
		}
		if _, execErr := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_settings")+`
			(system_account_id, key, value_json, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(system_account_id, key) DO UPDATE SET
				value_json = excluded.value_json,
				updated_at = excluded.updated_at`), SystemSettingsAccountID, key, string(valueJSON), nowISO); execErr != nil {
			return execErr
		}
	}
	return tx.Commit()
}

// refreshSystemCache mirrors refreshSystemSettingsCacheAfterSectionWrite's
// reload step: load the fresh snapshot and refill the 60s app cache.
func (s *Store) refreshSystemCache(ctx context.Context) (map[string]any, error) {
	settings, err := s.loadFromDatabase(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cached = settings
	s.cachedAt = s.now()
	s.mu.Unlock()
	return copySettings(settings), nil
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// LoadPublic mirrors listPublicGlobalSettingsAsync: pickGlobalSettings over
// listGlobalSettingsAsync — the cached global read projected to the same
// appName/appIcon pair (the key lists are identical, so the projection is
// the identity).
func (s *Store) LoadPublic(ctx context.Context) (map[string]any, error) {
	return s.LoadGlobal(ctx)
}

// LoadGlobal mirrors listGlobalSettingsAsync: the global brand pair behind
// the 60s settings:global app cache. Per-key normalization and the
// all-keys-present assertion apply; a stored anomaly renders as the generic
// 500.
func (s *Store) LoadGlobal(ctx context.Context) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	s.mu.Lock()
	if s.globalCached != nil && s.now().Sub(s.globalCachedAt) < settingsCacheTTL {
		cached := s.globalCached
		s.mu.Unlock()
		return copySettings(cached), nil
	}
	s.mu.Unlock()
	settings, err := s.loadGlobalFromDatabase(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.globalCached = settings
	s.globalCachedAt = s.now()
	s.mu.Unlock()
	return copySettings(settings), nil
}

func (s *Store) loadGlobalFromDatabase(ctx context.Context) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT key, value_json FROM `+s.table("global_settings")+`
		WHERE key IN (`+placeholders(len(GlobalSettingKeys))+`)
		ORDER BY key ASC`), queryArgs(GlobalSettingKeys, "")...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]any{}
	for rows.Next() {
		var key, valueJSON string
		if err := rows.Scan(&key, &valueJSON); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
			return nil, err
		}
		normalized, err := normalizeGlobalSetting(key, value)
		if err != nil {
			return nil, err
		}
		settings[key] = normalized
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	assertAllSettingsPresent(settings, GlobalSettingKeys, "全局设置")
	return settings, nil
}

// UpdateGlobal mirrors updateGlobalSettingsAsync: strict global whitelist +
// non-empty-string validation, a single upsert transaction, then the global
// cache refresh and the fresh full global snapshot. Node issues no gateway
// runtime invalidation for global writes.
func (s *Store) UpdateGlobal(ctx context.Context, input map[string]any) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	normalized, err := normalizeGlobalSettingsInput(input)
	if err != nil {
		return nil, err
	}
	if err := s.upsertGlobalSettings(ctx, normalized); err != nil {
		return nil, err
	}
	return s.refreshGlobalCache(ctx)
}

// upsertGlobalSettings persists normalized global settings in a single
// transaction (updateGlobalSettingsAsync / updateManagementSettingsSectionAsync
// global branch upsert).
func (s *Store) upsertGlobalSettings(ctx context.Context, normalized map[string]any) error {
	keys := sortedKeys(normalized)
	nowISO := s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range keys {
		valueJSON, marshalErr := json.Marshal(normalized[key])
		if marshalErr != nil {
			return marshalErr
		}
		if _, execErr := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("global_settings")+`
			(key, value_json, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value_json = excluded.value_json,
				updated_at = excluded.updated_at`), key, string(valueJSON), nowISO); execErr != nil {
			return execErr
		}
	}
	return tx.Commit()
}

// refreshGlobalCache mirrors refreshGlobalSettingsCacheAfterSectionWrite:
// clear + reload the settings:global snapshot.
func (s *Store) refreshGlobalCache(ctx context.Context) (map[string]any, error) {
	settings, err := s.loadGlobalFromDatabase(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.globalCached = settings
	s.globalCachedAt = s.now()
	s.mu.Unlock()
	return copySettings(settings), nil
}

// LoadSection mirrors getManagementSettingsSectionAsync: the section catalog
// drives the table (global_settings vs system_settings), per-key
// normalization, the compatible system defaults (section keys only) and the
// per-section presence assertion. No snapshot cache — Node reads the section
// projection directly from the database.
func (s *Store) LoadSection(ctx context.Context, sectionKey string) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	section, err := resolveSettingsSection(sectionKey)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if section.Domain == settingsSectionDomainGlobal {
		rows, err = s.db.QueryContext(ctx, s.bind(`SELECT key, value_json FROM `+s.table("global_settings")+`
			WHERE key IN (`+placeholders(len(section.Keys))+`)
			ORDER BY key ASC`), queryArgs(section.Keys, "")...)
	} else {
		rows, err = s.db.QueryContext(ctx, s.bind(`SELECT key, value_json FROM `+s.table("system_settings")+`
			WHERE system_account_id = ? AND key IN (`+placeholders(len(section.Keys))+`)
			ORDER BY key ASC`), queryArgs(section.Keys, SystemSettingsAccountID)...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]any{}
	for rows.Next() {
		var key, valueJSON string
		if err := rows.Scan(&key, &valueJSON); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
			return nil, err
		}
		normalized, err := normalizeSectionSetting(section, key, value)
		if err != nil {
			return nil, err
		}
		values[key] = normalized
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if section.Domain == settingsSectionDomainSystem {
		applyCompatibleSystemSettingDefaults(values, section.Keys)
	}
	if err := assertAllSettingsPresent(values, section.Keys, sectionKey+" 设置"); err != nil {
		return nil, err
	}
	return values, nil
}

// UpdateSection mirrors updateManagementSettingsSectionAsync: the section
// whitelist rejects unknown and prototype-polluting keys, per-key validation
// runs per domain, the online usageStatsTimezone guard applies to system
// sections, a single upsert transaction persists the values and the write
// refreshes the domain cache — system sections additionally fire the gateway
// runtime invalidation (Node notifyGatewayRuntimeCacheInvalidation). The
// Node account-health branch also enqueues account health jobs input
// snapshots inside the transaction; that outbox family has no gateway-side
// port yet, so the write stays settings-only (documented gap).
func (s *Store) UpdateSection(ctx context.Context, sectionKey string, input map[string]any) (map[string]any, error) {
	ctx = ensureCtx(ctx)
	section, err := resolveSettingsSection(sectionKey)
	if err != nil {
		return nil, err
	}
	if len(input) == 0 {
		return nil, &ValidationError{Message: "设置更新不能为空"}
	}
	allowed := make(map[string]bool, len(section.Keys))
	for _, key := range section.Keys {
		allowed[key] = true
	}
	for key := range input {
		if !allowed[key] || key == "__proto__" || key == "constructor" || key == "prototype" {
			return nil, &ValidationError{Message: sectionKey + " 包含不允许的字段"}
		}
	}
	normalized := make(map[string]any, len(input))
	for key, value := range input {
		value, err := normalizeSectionSetting(section, key, value)
		if err != nil {
			return nil, err
		}
		normalized[key] = value
	}
	if section.Domain == settingsSectionDomainSystem {
		if err := s.assertUsageStatsTimezoneUpdateAllowed(ctx, normalized); err != nil {
			return nil, err
		}
	}
	if section.Domain == settingsSectionDomainGlobal {
		if err := s.upsertGlobalSettings(ctx, normalized); err != nil {
			return nil, err
		}
		if _, err := s.refreshGlobalCache(ctx); err != nil {
			return nil, err
		}
	} else {
		if err := s.upsertSystemSettings(ctx, normalized); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cached = nil
		s.cachedAt = time.Time{}
		s.mu.Unlock()
		if s.inval != nil {
			s.inval.Invalidate(TopicGatewayRuntime, settingsUpdatedReason)
		}
		if _, err := s.refreshSystemCache(ctx); err != nil {
			return nil, err
		}
	}
	return s.LoadSection(ctx, sectionKey)
}

// normalizeSectionSetting dispatches the per-domain validator for section
// reads and writes (updateManagementSettingsSectionAsync).
func normalizeSectionSetting(section ManagementSettingsSection, key string, value any) (any, error) {
	if section.Domain == settingsSectionDomainGlobal {
		return normalizeGlobalSetting(key, value)
	}
	return normalizeSystemSetting(key, value)
}

// normalizeSystemSettingsInput mirrors normalizeSystemSettingsInput: every
// entry must be a whitelisted key with a valid value; an empty update is
// rejected.
func normalizeSystemSettingsInput(input map[string]any) (map[string]any, error) {
	output := map[string]any{}
	for key, value := range input {
		normalized, err := normalizeSystemSetting(key, value)
		if err != nil {
			return nil, err
		}
		output[key] = normalized
	}
	if len(output) == 0 {
		return nil, &ValidationError{Message: "系统设置更新不能为空"}
	}
	return output, nil
}

// normalizeSystemSetting mirrors normalizeSystemSetting + the validator table.
func normalizeSystemSetting(key string, value any) (any, error) {
	spec, ok := systemSettingSpecs[key]
	if !ok {
		return nil, &ValidationError{Message: "未知系统设置字段：" + key}
	}
	if !spec.integer {
		timezone, err := normalizeUsageStatsTimezone(value)
		if err != nil {
			return nil, &ValidationError{Message: key + " 无效：" + err.Error()}
		}
		return timezone, nil
	}
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return nil, &ValidationError{Message: key + " 必须是整数"}
	}
	if number < float64(spec.min) || number > float64(spec.max) {
		return nil, &ValidationError{Message: key + " 必须在 " + itoa(spec.min) + " 到 " + itoa(spec.max) + " 之间"}
	}
	return number, nil
}

// normalizeGlobalSetting mirrors normalizeGlobalSetting + nonEmptyStringSetting:
// unknown keys are rejected before the non-empty-string validation.
func normalizeGlobalSetting(key string, value any) (any, error) {
	if !globalSettingKeySet[key] {
		return nil, &ValidationError{Message: "未知全局设置字段：" + key}
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, &ValidationError{Message: key + " 必须是非空字符串"}
	}
	return strings.TrimSpace(text), nil
}

// normalizeGlobalSettingsInput mirrors normalizeGlobalSettingsInput: every
// entry must be a whitelisted global key with a non-empty string value; an
// empty update is rejected.
func normalizeGlobalSettingsInput(input map[string]any) (map[string]any, error) {
	output := map[string]any{}
	for key, value := range input {
		normalized, err := normalizeGlobalSetting(key, value)
		if err != nil {
			return nil, err
		}
		output[key] = normalized
	}
	if len(output) == 0 {
		return nil, &ValidationError{Message: "全局设置更新不能为空"}
	}
	return output, nil
}

// normalizeUsageStatsTimezone mirrors usage-stats-helpers.normalizeUsageStatsTimezone.
func normalizeUsageStatsTimezone(value any) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", &ValidationError{Message: "统计时区必须是非空字符串"}
	}
	timezone := strings.TrimSpace(text)
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", &ValidationError{Message: "统计时区不存在：" + timezone}
	}
	return timezone, nil
}

// applyCompatibleSystemSettingDefaults mirrors applyCompatibleSystemSettingDefaults:
// legacy databases may miss these rows and the loader fills them in for the
// requested key list only.
func applyCompatibleSystemSettingDefaults(settings map[string]any, keys []string) {
	for _, key := range keys {
		if _, ok := settings[key]; ok {
			continue
		}
		if fallback, ok := compatibleSystemSettingDefaults[key]; ok {
			settings[key] = float64(fallback)
		}
	}
}

func assertAllSettingsPresent(settings map[string]any, keys []string, label string) error {
	for _, key := range keys {
		if _, ok := settings[key]; !ok {
			return &ValidationError{Message: label + "缺少字段：" + key}
		}
	}
	return nil
}

// assertUsageStatsTimezoneUpdateAllowed mirrors
// assertUsageStatsTimezoneUpdateAllowed: PostgreSQL rejects every online
// timezone change; SQLite mode allows a no-op write or an empty stats store
// and refuses once usage stats data exists.
func (s *Store) assertUsageStatsTimezoneUpdateAllowed(ctx context.Context, normalized map[string]any) error {
	next, ok := normalized["usageStatsTimezone"]
	if !ok {
		return nil
	}
	if s.pg {
		return &ValidationError{Message: "PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整"}
	}
	snapshot, err := s.Load(ctx)
	if err != nil {
		return err
	}
	current, _ := snapshot["usageStatsTimezone"].(string)
	if next == current {
		return nil
	}
	exists, err := s.usageStatsDataExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return &ValidationError{Message: "已有统计数据后不能直接修改统计时区，请先备份并重建统计缓存"}
}

// usageStatsDataExists mirrors usageStatsDataExists: any row in any usage
// stats projection table blocks the timezone change. The probe runs against
// the dedicated stats database when wired (SetStatsDatabase) — the historical
// defect probed the business handle, where the usage_stats_* tables do not
// exist in the six-database split, turning every online timezone change into
// a 500.
func (s *Store) usageStatsDataExists(ctx context.Context) (bool, error) {
	probeDB := s.db
	if s.statsDB != nil {
		probeDB = s.statsDB
	}
	for _, tableName := range usageStatsDataTables {
		var probe int
		err := probeDB.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table(tableName)+` LIMIT 1`)).Scan(&probe)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func copySettings(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
