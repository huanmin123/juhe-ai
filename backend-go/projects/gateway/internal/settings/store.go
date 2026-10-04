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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
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
	"textNonStreamFirstResponseTimeoutSeconds",
	"textStreamIdleTimeoutSeconds",
	"textUncommittedAttemptMaxLifetimeSeconds",
	"imageFirstResponseTimeoutSeconds",
	"imageStreamIdleTimeoutSeconds",
	"imageUncommittedAttemptMaxLifetimeSeconds",
	"imageRequestWallTimeoutSeconds",
	"chatImageGenerationTotalTimeoutSeconds",
	"audioFirstResponseTimeoutSeconds",
	"videoCreateTimeoutSeconds",
	"realtimeIdleTimeoutSeconds",
	"realtimeMaxSessionSeconds",
	"realtimeMaxConnectionsPerApiKey",
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
	"upstreamClientVersionOverrides",
}

var systemSettingKeySet = func() map[string]bool {
	set := make(map[string]bool, len(SystemSettingKeys))
	for _, key := range SystemSettingKeys {
		set[key] = true
	}
	return set
}()

// settingSpec mirrors one SYSTEM_SETTING_VALIDATORS entry: an integer with a
// closed range, the timezone validator, or the upstreamClientVersionOverrides
// JSON object validator.
type settingSpec struct {
	integer bool
	min     int
	max     int
	// clientVersionsJSON 标记 upstreamClientVersionOverrides 的 JSON 对象
	// 校验（键 ⊆ 五个客户端家族，值为 ^\d+\.\d+\.\d+$；空对象合法）。
	clientVersionsJSON bool
	// sampleRate 标记 auditLogSuccessSampleRate：0..1 闭区间、最多 4 位小数
	// 的非整数数值键（env 侧 auditlog parseBoundedDecimal 同款约束）。
	sampleRate bool
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
	"textNonStreamFirstResponseTimeoutSeconds":   {integer: true, min: 10, max: 3600},
	"textStreamIdleTimeoutSeconds":               {integer: true, min: 1, max: 3600},
	"textUncommittedAttemptMaxLifetimeSeconds":   {integer: true, min: 60, max: 86400},
	"imageFirstResponseTimeoutSeconds":           {integer: true, min: 10, max: 3600},
	"imageStreamIdleTimeoutSeconds":              {integer: true, min: 1, max: 3600},
	"imageUncommittedAttemptMaxLifetimeSeconds":  {integer: true, min: 60, max: 86400},
	"imageRequestWallTimeoutSeconds":             {integer: true, min: 60, max: 86400},
	"chatImageGenerationTotalTimeoutSeconds":     {integer: true, min: 60, max: 86400},
	"audioFirstResponseTimeoutSeconds":           {integer: true, min: 10, max: 3600},
	"videoCreateTimeoutSeconds":                  {integer: true, min: 10, max: 3600},
	// M5b realtime 会话生命周期键（Realtime 设计 §3；消费面随 M5b2 WS
	// 桥接 handler 生效）。
	"realtimeIdleTimeoutSeconds":                 {integer: true, min: 10, max: 3600},
	"realtimeMaxSessionSeconds":                  {integer: true, min: 60, max: 86400},
	"realtimeMaxConnectionsPerApiKey":            {integer: true, min: 1, max: 100},
	"noAvailableAccountWaitTimeoutSeconds":       {integer: true, min: 10, max: 3600},
	"streamFailureThresholdCount":                {integer: true, min: 1, max: 100},
	"streamFailureThresholdWindowMinutes":        {integer: true, min: 1, max: 1440},
	"operationLogRetentionDays":                  {integer: true, min: 1, max: 3650},
	"operationLogMaxChangesPerRecord":            {integer: true, min: 1, max: 500},
	"auditLogSuccessRetentionDays":               {integer: true, min: 0, max: 3650},
	"auditLogProblemRetentionDays":               {integer: true, min: 1, max: 3650},
	"auditLogSuccessHotRetentionHours":           {integer: true, min: 0, max: 168},
	"auditLogSuccessSampleRate":                  {sampleRate: true},
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
	"upstreamClientVersionOverrides":             {clientVersionsJSON: true},
}

// compatibleSystemSettingDefaults mirrors compatibleSystemSettingDefaults:
// legacy databases may miss these rows and the loader fills them in. Integer
// entries keep the Node semantics (stored as float64); the object entry
// upstreamClientVersionOverrides defaults to the empty object (= 全部使用内置
// 客户端版本，与 jobssettings.DefaultSystemSettings 镜像一致)。
var compatibleSystemSettingDefaults = map[string]any{
	"gatewayUserRequestLimitPerMinute": 0,
	"gatewayUserRequestLimitPerDay":    0,
	"gatewayUserRequestLimitPerWeek":   0,
	"gatewayUserRequestLimitPerMonth":  0,
	"userAiAccountLimit":               100,
	"upstreamClientVersionOverrides":   map[string]any{},
	// 2026-10-02 日志与审计设置：存量库缺行时按代码默认补齐（与 env 未显式
	// 配置时的 LoadConfig 默认一致；见 docs/functions/日志与审计设置设计.md）。
	"auditLogSuccessRetentionDays":     3,
	"auditLogProblemRetentionDays":     7,
	"auditLogSuccessHotRetentionHours": 1,
	"auditLogSuccessSampleRate":        1.0,
	// 媒体车道独立超时档位（音频视频模型接入设计 §3）：存量库在 maintenance
	// 重新播种前缺行，按种子默认补齐（audio 120 / video create 60），与
	// pgSeedSystemSettings、jobssettings.DefaultSystemSettings 一致。
	"audioFirstResponseTimeoutSeconds": 120,
	"videoCreateTimeoutSeconds":        60,
	// M5b realtime 会话生命周期键（Realtime 设计 §3）：同语义按种子默认
	// 补齐（消费面随 M5b2 WS 桥接 handler 生效）。
	"realtimeIdleTimeoutSeconds":      120,
	"realtimeMaxSessionSeconds":       1800,
	"realtimeMaxConnectionsPerApiKey": 5,
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
	// settingsSectionLogRetention 是日志与审计分区键（联动校验入口判定用）。
	settingsSectionLogRetention = "log-retention"
)

// logRetentionAuditKeys 是 log-retention 分区里参与联动校验的四个审计键。
var logRetentionAuditKeys = []string{
	"auditLogSuccessRetentionDays",
	"auditLogProblemRetentionDays",
	"auditLogSuccessHotRetentionHours",
	"auditLogSuccessSampleRate",
}

// ManagementSettingsSection mirrors one managementSettingsSectionCatalog
// entry: the storage domain plus the exact setting key list served and
// accepted for the section.
type ManagementSettingsSection struct {
	Domain string
	Keys   []string
}

// ManagementSettingsSectionCatalog mirrors managementSettingsSectionCatalog
// (settings.repository.ts): the management sections and their key lists. brand
// lives in global_settings, everything else in system_settings.
var ManagementSettingsSectionCatalog = map[string]ManagementSettingsSection{
	"brand": {Domain: settingsSectionDomainGlobal, Keys: GlobalSettingKeys},
	"gateway-core": {Domain: settingsSectionDomainSystem, Keys: []string{
		"gatewayTextRawBodyLimitMegabytes",
		"accountCircuitConfirmationFailuresRequired",
		"defaultTemporaryUnschedulableMinutes",
		"temporaryUnschedulableRetryIntervalSeconds",
		"temporaryUnschedulableRetryAttempts",
		"textFirstResponseTimeoutSeconds",
		"textNonStreamFirstResponseTimeoutSeconds",
		"textStreamIdleTimeoutSeconds",
		"textUncommittedAttemptMaxLifetimeSeconds",
		"imageFirstResponseTimeoutSeconds",
		"imageStreamIdleTimeoutSeconds",
		"imageUncommittedAttemptMaxLifetimeSeconds",
		"imageRequestWallTimeoutSeconds",
		"chatImageGenerationTotalTimeoutSeconds",
		"audioFirstResponseTimeoutSeconds",
		"videoCreateTimeoutSeconds",
		"realtimeIdleTimeoutSeconds",
		"realtimeMaxSessionSeconds",
		"realtimeMaxConnectionsPerApiKey",
		"noAvailableAccountWaitTimeoutSeconds",
		"upstreamClientVersionOverrides",
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
	// log-retention（2026-10-02 日志与审计设置）：审计 retention 四键收编为
	// 系统设置热更新入口；operationLog 两键原已有白名单与 seed，本次仅获得
	// section 归属，行为不变（F4 每 tick 热读保持现状）。
	"log-retention": {Domain: settingsSectionDomainSystem, Keys: []string{
		"auditLogSuccessRetentionDays",
		"auditLogProblemRetentionDays",
		"auditLogSuccessHotRetentionHours",
		"auditLogSuccessSampleRate",
		"operationLogRetentionDays",
		"operationLogMaxChangesPerRecord",
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
	"log-retention",
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
	if err := assertAllSettingsPresent(settings, SystemSettingKeys, "系统设置"); err != nil {
		return nil, err
	}
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
	// 全量快照写路径共用同一 spec 表：写入审计保留键时同样执行联动校验，
	// 防止绕过 log-retention 分区校验破坏采样率/保留天数不变量。
	if err := s.assertLogRetentionLinkageAllowed(ctx, normalized); err != nil {
		return nil, err
	}
	// The legacy full-snapshot write mirrors Node updateSettingsAsync, which
	// has no account-health reservation branch; the empty sectionKey keeps
	// that arm settings-only.
	if err := s.upsertSystemSettings(ctx, "", normalized); err != nil {
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
	snapshot, err := s.refreshSystemCache(ctx)
	if err != nil {
		return nil, err
	}
	// upstreamClientVersionOverrides 覆盖的进程内即时生效点：写入与缓存
	// 刷新全部成功后同步刷新 upstreamidentity（upstream 请求 UA/版本头不经
	// runtime cache，需在此直接刷新）。
	s.refreshUpstreamClientVersionOverrides(ctx)
	return snapshot, nil
}

// upsertSystemSettings persists normalized system settings in a single
// transaction (the updateSettingsAsync / updateManagementSettingsSectionAsync
// system branch upsert). sectionKey carries the management section identity:
// only the account-health section additionally reserves the account health
// jobs input epochs inside the same transaction (D7 gap registration).
func (s *Store) upsertSystemSettings(ctx context.Context, sectionKey string, normalized map[string]any) error {
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
	if sectionKey == "account-health" {
		if err := s.reserveAccountHealthInputVersions(ctx, tx, nowISO); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reserveAccountHealthInputVersions registers the D7 gap inside the caller's
// transaction. The Node account-health settings branch
// (settings.repository.ts:308-327) scans the eligible OpenAI accounts and
// enqueues their health jobs input snapshots, writing BOTH
// account_health_jobs_input_versions (epoch reservation) and
// account_health_jobs_input_outbox (publish intent). This Go port reserves
// the versions rows only: the Go topology has no outbox consumer — jobs
// accounthealth discovers the direct input projection from an INNER JOIN on
// account_health_jobs_input_versions (accounthealth/direct_input_reader.go) —
// and the #7 ruling (juhe-ai-gateway/chain_error_policy_effects.go
// markCooldown) already established "never write a dead surface without a
// consumer" for this exact outbox family. The statement shape mirrors the
// sibling reservation in accounts/write.go
// (reserveAndEnqueueAccountHealthSnapshot) without the outbox insert, and the
// PG row-lock suffix follows authz/downstream.go. Any failure returns into
// the enclosing transaction and rolls the settings write back with it. No
// version upper-bound guard is added, matching the sibling writers.
func (s *Store) reserveAccountHealthInputVersions(ctx context.Context, tx *sql.Tx, now string) error {
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, config_revision, dispatch_revision FROM `+s.table("accounts")+`
		WHERE deleted_at IS NULL AND provider_code = 'openai' AND type IN ('api_key','oauth')
		ORDER BY id ASC`)+lockSuffix)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var configRevision, dispatchRevision int64
		if err := rows.Scan(&accountID, &configRevision, &dispatchRevision); err != nil {
			return err
		}
		var currentVersion sql.NullInt64
		err := tx.QueryRowContext(ctx, s.bind(`SELECT current_version FROM `+s.table("account_health_jobs_input_versions")+`
			WHERE account_id = ?`)+lockSuffix, accountID).Scan(&currentVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		nextVersion := int64(1)
		if err == nil && currentVersion.Valid {
			nextVersion = currentVersion.Int64 + 1
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_health_jobs_input_versions")+`
				SET current_version = ?, reserved_at = ? WHERE account_id = ?`), nextVersion, now, accountID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_versions")+`
				(account_id, current_version, reserved_at) VALUES (?, ?, ?)`), accountID, nextVersion, now); err != nil {
				return err
			}
		}
	}
	return rows.Err()
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
	if err := assertAllSettingsPresent(settings, GlobalSettingKeys, "全局设置"); err != nil {
		return nil, err
	}
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
// account-health section additionally reserves the account health jobs input
// epochs inside the same transaction (D7 gap registration): the Node branch
// writes versions + outbox, but the Go topology has no outbox consumer (jobs
// accounthealth schedules from the versions INNER JOIN) and the #7 ruling
// (chain_error_policy_effects.go markCooldown) established "never write a
// dead surface without a consumer", so only the versions reservation is
// ported (see reserveAccountHealthInputVersions).
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
		// log-retention 分区写入做 section 级联动校验：PATCH 可能只带部分键，
		// 未提供的键用 DB 现值（含兼容默认）合成完整视图再校验。
		if sectionKey == settingsSectionLogRetention {
			if err := s.assertLogRetentionLinkageAllowed(ctx, normalized); err != nil {
				return nil, err
			}
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
		if err := s.upsertSystemSettings(ctx, sectionKey, normalized); err != nil {
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
		// 与 Update 相同的覆盖即时生效点（分区写入路径，缓存刷新成功后）。
		s.refreshUpstreamClientVersionOverrides(ctx)
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
	if spec.clientVersionsJSON {
		return normalizeUpstreamClientVersionOverrides(key, value)
	}
	if spec.sampleRate {
		return normalizeSuccessSampleRateSetting(key, value)
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

// normalizeSuccessSampleRateSetting 校验 auditLogSuccessSampleRate：0..1 闭
// 区间、最多 4 位小数的数字（十进制文本判定，避免浮点噪声；env 侧
// JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 同款约束）。
func normalizeSuccessSampleRateSetting(key string, value any) (any, error) {
	message := key + " 必须在 0 到 1 之间且最多 4 位小数"
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 1 {
		return nil, &ValidationError{Message: message}
	}
	if decimalPlaces(number) > 4 {
		return nil, &ValidationError{Message: message}
	}
	return number, nil
}

// decimalPlaces 用最短十进制文本统计小数位数（strconv 'f'/-1 与 JSON 数字的
// 十进制原义一致，0.0001 → 4 位）。
func decimalPlaces(number float64) int {
	text := strconv.FormatFloat(number, 'f', -1, 64)
	if point := strings.IndexByte(text, '.'); point >= 0 {
		return len(text) - point - 1
	}
	return 0
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

// upstreamClientVersionOverridesKey 是系统设置键（白名单成员）。
const upstreamClientVersionOverridesKey = "upstreamClientVersionOverrides"

// upstreamClientVersionFamilies 是该键允许的客户端家族（与
// upstreamidentity 的家族键一致）。
var upstreamClientVersionFamilies = map[string]bool{
	"codex":      true,
	"claudeCode": true,
	"geminiCLI":  true,
	"zcode":      true,
	"grokCLI":    true,
}

// normalizeUpstreamClientVersionOverrides 是 upstreamClientVersionOverrides
// 的写入/读取严格校验：值必须是 JSON 对象；键 ⊆ 五个客户端家族；每个值是
// 匹配 ^\d+\.\d+\.\d+$ 的字符串；空对象合法（= 全部使用内置版本）。
func normalizeUpstreamClientVersionOverrides(key string, value any) (any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: key + " 必须是 JSON 对象（键为客户端家族，值为三段版本字符串，空对象表示全部使用内置版本）"}
	}
	for family, familyValue := range object {
		if !upstreamClientVersionFamilies[family] {
			return nil, &ValidationError{Message: key + " 包含不支持的客户端家族：" + family}
		}
		version, ok := familyValue.(string)
		if !ok || !isUpstreamSemverVersion(version) {
			return nil, &ValidationError{Message: key + "." + family + " 必须是三段语义化版本字符串（如 1.2.3）"}
		}
	}
	return object, nil
}

// filterUpstreamClientVersionOverrides 把已解码值宽容过滤为「家族 -> 版本」
// 表：未知键与非法值忽略（供运行时消费，防御存量脏数据；正常校验在
// normalizeUpstreamClientVersionOverrides 写入路径）。
func filterUpstreamClientVersionOverrides(value any) map[string]string {
	object, _ := value.(map[string]any)
	filtered := map[string]string{}
	for family, familyValue := range object {
		if !upstreamClientVersionFamilies[family] {
			continue
		}
		version, ok := familyValue.(string)
		if !ok || !isUpstreamSemverVersion(version) {
			continue
		}
		filtered[family] = version
	}
	return filtered
}

// isUpstreamSemverVersion 匹配 ^\d+\.\d+\.\d+$（三段纯数字版本）。
func isUpstreamSemverVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// UpstreamClientVersionOverrides 读取该键当前生效的覆盖表：缺行/空对象/无法
// 解析返回空 map（= 全部使用内置版本），数据库错误原样返回。直读数据库不经
// 过 60s 应用缓存，供组合根启动接线与设置写入后的即时刷新。
func (s *Store) UpstreamClientVersionOverrides(ctx context.Context) (map[string]string, error) {
	ctx = ensureCtx(ctx)
	var valueJSON sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT value_json FROM `+s.table("system_settings")+`
		WHERE system_account_id = ? AND key = ?`), SystemSettingsAccountID, upstreamClientVersionOverridesKey).Scan(&valueJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var decoded any
	if json.Unmarshal([]byte(valueJSON.String), &decoded) != nil {
		return map[string]string{}, nil
	}
	return filterUpstreamClientVersionOverrides(decoded), nil
}

// refreshUpstreamClientVersionOverrides 把该键当前值应用到
// upstreamidentity 的进程内覆盖（gateway 组合根在启动与每次系统设置写入
// 成功后调用，管理端保存后新请求立即使用新版本）。读取失败保持既有覆盖
// 不动（不影响写请求结果，下一轮写入或进程重启再对齐）。
func (s *Store) refreshUpstreamClientVersionOverrides(ctx context.Context) {
	overrides, err := s.UpstreamClientVersionOverrides(ctx)
	if err != nil {
		return
	}
	upstreamidentity.SetClientVersionOverrides(overrides)
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
			if number, isInt := fallback.(int); isInt {
				settings[key] = float64(number)
				continue
			}
			settings[key] = fallback
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

// assertLogRetentionLinkageAllowed 校验审计保留联动约束（写入路径 400 拒绝，
// 不落到读时静默回退）：采样率与成功保留天数必须同时为 0 或同时大于 0；
// 成功保留天数 > 0 时 天数×24 不得小于热窗小时数。normalized 未携带任何
// 审计键时直接通过；携带部分键时未提供的键用 DB 现值（LoadSection 已合并
// 兼容默认）合成完整视图再校验。
func (s *Store) assertLogRetentionLinkageAllowed(ctx context.Context, normalized map[string]any) error {
	touched := false
	for _, key := range logRetentionAuditKeys {
		if _, ok := normalized[key]; ok {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	current, err := s.LoadSection(ctx, settingsSectionLogRetention)
	if err != nil {
		return err
	}
	view := make(map[string]any, len(logRetentionAuditKeys))
	for _, key := range logRetentionAuditKeys {
		if value, ok := normalized[key]; ok {
			view[key] = value
		} else {
			view[key] = current[key]
		}
	}
	days, _ := view["auditLogSuccessRetentionDays"].(float64)
	rate, _ := view["auditLogSuccessSampleRate"].(float64)
	hotHours, _ := view["auditLogSuccessHotRetentionHours"].(float64)
	if (rate == 0) != (days == 0) {
		return &ValidationError{Message: "auditLogSuccessSampleRate 与 auditLogSuccessRetentionDays 必须同时为 0 或同时大于 0"}
	}
	if days > 0 && days*24 < hotHours {
		return &ValidationError{Message: "auditLogSuccessRetentionDays 必须覆盖 auditLogSuccessHotRetentionHours（成功保留天数 × 24 不得小于热窗小时数）"}
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
