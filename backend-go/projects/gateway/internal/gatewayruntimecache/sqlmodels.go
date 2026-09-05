package gatewayruntimecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
)

// SQLReadModels is the default ReadModels implementation over the business
// database (SQLite + PostgreSQL dual mode via ? / $N binding). It owns the
// read-only queries that had no Go home at migration time:
//
//   - gateway settings projection (Node readGatewaySettings +
//     gatewaySettingsFromRawSettings) — reuses the completed settings store
//     for the whitelisted, defaulted raw snapshot,
//   - gateway API key row + active group bindings (Node validateGatewayApiKey /
//     loadActiveGatewayApiKeyGroupBindings),
//   - group usage access metadata (Node resolveGroupUsageAccessMetadata),
//   - active response inspection policies for the gateway (Node
//     listActiveResponseInspectionPoliciesForGateway, default rules included),
//   - the read_gateway_runtime composition (Node db-service readGatewayRuntime).
//
// The account selector (listOpenAIAccountsForGroupResult), the provider model
// catalog builder and the live concurrency tracker stay behind the
// AccountsSelector / CatalogSource / ConcurrencySource seams: they are
// separate migration slices (M08/J, C03, G13). Composition wires them here
// once those slices land; until then the corresponding reads fail fast with a
// wiring error instead of silently returning wrong data.
type SQLReadModels struct {
	db          *sql.DB
	pg          bool
	settings    *settings.Store
	now         func() time.Time
	accounts    AccountsSelector
	catalog     CatalogSource
	concurrency ConcurrencySource
}

// NewSQLReadModels builds the SQL read models. now defaults to time.Now; the
// three seams may stay nil until their slices land (behaviour documented on
// SQLReadModels).
func NewSQLReadModels(db *sql.DB, postgres bool, now func() time.Time, accounts AccountsSelector, catalog CatalogSource, concurrency ConcurrencySource) (*SQLReadModels, error) {
	if db == nil {
		return nil, errors.New("gatewayruntimecache SQL 读模型需要数据库")
	}
	if now == nil {
		now = time.Now
	}
	store, err := settings.NewStore(db, postgres, now, nil)
	if err != nil {
		return nil, err
	}
	return &SQLReadModels{
		db:          db,
		pg:          postgres,
		settings:    store,
		now:         now,
		accounts:    accounts,
		catalog:     catalog,
		concurrency: concurrency,
	}, nil
}

// SetAccountsSelector wires the account selector seam.
func (m *SQLReadModels) SetAccountsSelector(selector AccountsSelector) { m.accounts = selector }

// SetSettingsStore shares the composition settings repository with the read
// models. Node keeps ONE process-local system settings cache that
// updateSettingsAsync clears on every write; two stores over the same
// database leave the gateway runtime snapshot reading a stale 60s-cached
// snapshot after a management settings PATCH. Without an injected store the
// internally-created one stays in place (standalone test usage).
func (m *SQLReadModels) SetSettingsStore(store *settings.Store) {
	if store != nil {
		m.settings = store
	}
}

// SetCatalogSource wires the model catalog seam.
func (m *SQLReadModels) SetCatalogSource(source CatalogSource) { m.catalog = source }

// SetConcurrencySource wires the live concurrency seam.
func (m *SQLReadModels) SetConcurrencySource(source ConcurrencySource) { m.concurrency = source }

func (m *SQLReadModels) table(name string) string {
	if m.pg {
		return "juhe_business." + name
	}
	return name
}

func (m *SQLReadModels) bind(query string) string {
	if !m.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func ensureModelCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// ---------------------------------------------------------------------------
// seam delegations (account selector / catalog / concurrency)
// ---------------------------------------------------------------------------

// ListOpenAIAccountsForGroupResult delegates to the wired AccountsSelector.
func (m *SQLReadModels) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	if m.accounts == nil {
		return OpenAIAccountsForGroupResult{}, errors.New("gatewayruntimecache 账户选择器未接线（AccountsSelector）")
	}
	return m.accounts.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

// ListProviderModelCatalog delegates to the wired CatalogSource.
func (m *SQLReadModels) ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	if m.catalog == nil {
		return nil, errors.New("gatewayruntimecache 模型目录源未接线（CatalogSource）")
	}
	return m.catalog.ListProviderModelCatalog(ctx, input)
}

// LoadAccountCurrentConcurrencyByID delegates to the wired
// ConcurrencySource; without one the overlay reads as zero for every account,
// matching a Node runtime without concurrency state.
func (m *SQLReadModels) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if m.concurrency == nil {
		return map[string]int{}, nil
	}
	return m.concurrency.LoadAccountCurrentConcurrencyByID(ctx, accountIDs)
}

// ---------------------------------------------------------------------------
// gateway settings projection (Node gatewaySettingsFromRawSettings)
// ---------------------------------------------------------------------------

// ReadGatewaySettings mirrors readGatewaySettings: raw whitelisted settings
// snapshot projected to GatewaySettings with the Node clamps; invalid stored
// values surface the Node "系统设置" errors.
func (m *SQLReadModels) ReadGatewaySettings(ctx context.Context) (GatewaySettings, error) {
	ctx = ensureModelCtx(ctx)
	raw, err := m.settings.Load(ctx)
	if err != nil {
		return GatewaySettings{}, err
	}
	return projectGatewaySettings(raw)
}

// numberSetting mirrors numberSetting: integer required, range enforced.
func numberSetting(raw map[string]any, key string, min, max int64) (int64, error) {
	value, ok := raw[key].(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	if value < float64(min) || value > float64(max) {
		return 0, fmt.Errorf("系统设置 %s 必须在 %d 到 %d 之间", key, min, max)
	}
	return int64(value), nil
}

// projectGatewaySettings mirrors gatewaySettingsFromRawSettings.
func projectGatewaySettings(raw map[string]any) (GatewaySettings, error) {
	out := GatewaySettings{StreamCircuitBreakerEnabled: true}
	var err error
	if out.GatewayTextRawBodyLimitMegabytes, err = numberSetting(raw, "gatewayTextRawBodyLimitMegabytes", 1, 64); err != nil {
		return GatewaySettings{}, err
	}
	if out.AccountCircuitConfirmationFailuresRequired, err = numberSetting(raw, "accountCircuitConfirmationFailuresRequired", 1, 5); err != nil {
		return GatewaySettings{}, err
	}
	optional := func(key string) (*int64, error) {
		value, err := numberSetting(raw, key, 0, 1_000_000_000)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	if out.GatewayUserRequestLimitPerMinute, err = optional("gatewayUserRequestLimitPerMinute"); err != nil {
		return GatewaySettings{}, err
	}
	if out.GatewayUserRequestLimitPerDay, err = optional("gatewayUserRequestLimitPerDay"); err != nil {
		return GatewaySettings{}, err
	}
	if out.GatewayUserRequestLimitPerWeek, err = optional("gatewayUserRequestLimitPerWeek"); err != nil {
		return GatewaySettings{}, err
	}
	if out.GatewayUserRequestLimitPerMonth, err = optional("gatewayUserRequestLimitPerMonth"); err != nil {
		return GatewaySettings{}, err
	}
	if timezone, ok := raw["usageStatsTimezone"].(string); ok && strings.TrimSpace(timezone) != "" {
		out.UsageStatsTimezone = strings.TrimSpace(timezone)
	} else {
		out.UsageStatsTimezone = "UTC"
	}
	if out.DefaultTemporaryUnschedulableMinutes, err = numberSetting(raw, "defaultTemporaryUnschedulableMinutes", 1, 1440); err != nil {
		return GatewaySettings{}, err
	}
	if out.TemporaryUnschedulableRetryIntervalSeconds, err = numberSetting(raw, "temporaryUnschedulableRetryIntervalSeconds", 0, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.TemporaryUnschedulableRetryAttempts, err = numberSetting(raw, "temporaryUnschedulableRetryAttempts", 0, 10); err != nil {
		return GatewaySettings{}, err
	}
	if out.TextFirstResponseTimeoutSeconds, err = numberSetting(raw, "textFirstResponseTimeoutSeconds", 10, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.TextStreamIdleTimeoutSeconds, err = numberSetting(raw, "textStreamIdleTimeoutSeconds", 1, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.TextUncommittedAttemptMaxLifetimeSeconds, err = numberSetting(raw, "textUncommittedAttemptMaxLifetimeSeconds", 60, 86400); err != nil {
		return GatewaySettings{}, err
	}
	if out.ImageFirstResponseTimeoutSeconds, err = numberSetting(raw, "imageFirstResponseTimeoutSeconds", 10, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.ImageStreamIdleTimeoutSeconds, err = numberSetting(raw, "imageStreamIdleTimeoutSeconds", 1, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.ImageUncommittedAttemptMaxLifetimeSeconds, err = numberSetting(raw, "imageUncommittedAttemptMaxLifetimeSeconds", 60, 86400); err != nil {
		return GatewaySettings{}, err
	}
	if out.ImageRequestWallTimeoutSeconds, err = numberSetting(raw, "imageRequestWallTimeoutSeconds", 60, 86400); err != nil {
		return GatewaySettings{}, err
	}
	if out.NoAvailableAccountWaitTimeoutSeconds, err = numberSetting(raw, "noAvailableAccountWaitTimeoutSeconds", 10, 3600); err != nil {
		return GatewaySettings{}, err
	}
	if out.StreamFailureThresholdCount, err = numberSetting(raw, "streamFailureThresholdCount", 1, 100); err != nil {
		return GatewaySettings{}, err
	}
	if out.StreamFailureThresholdWindowMinutes, err = numberSetting(raw, "streamFailureThresholdWindowMinutes", 1, 1440); err != nil {
		return GatewaySettings{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// group usage access metadata (Node resolveGroupUsageAccessMetadata)
// ---------------------------------------------------------------------------

// ResolveGroupUsageAccessMetadata mirrors resolveGroupUsageAccessMetadata:
// owner short-circuit, active group authorization, local authorization
// settings override.
func (m *SQLReadModels) ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	ctx = ensureModelCtx(ctx)
	var ownerID, providerCode string
	var enabled int
	var groupType, schedulingJSON sql.NullString
	err := m.db.QueryRowContext(ctx, m.bind(`SELECT system_account_id, provider_code, enabled, group_type, scheduling_policy_json
		FROM `+m.table("groups")+` WHERE id = ?`), groupID).
		Scan(&ownerID, &providerCode, &enabled, &groupType, &schedulingJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ownerID == "" || providerCode == "" || enabled != 1 {
		return nil, nil
	}
	groupTypeValue, err := normalizeGroupTypeValue(groupType)
	if err != nil {
		return nil, err
	}
	schedulingPolicy, err := parseGroupSchedulingPolicyJSON(schedulingJSON, groupTypeValue)
	if err != nil {
		return nil, err
	}
	if ownerID == systemAccountID {
		return &GroupUsageAccessMetadata{
			GroupOwnerSystemAccountID: ownerID,
			ProviderCode:              providerCode,
			GroupAccessType:           GroupAccessTypeOwner,
			GroupType:                 strPtrIfSet(groupTypeValue),
			SchedulingPolicy:          schedulingPolicy,
		}, nil
	}
	authorization, err := m.activeGroupAuthorization(ctx, groupID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if authorization == nil {
		return nil, nil
	}
	var localEnabled sql.NullInt64
	var localGroupType, localSchedulingJSON sql.NullString
	err = m.db.QueryRowContext(ctx, m.bind(`SELECT enabled, group_type, scheduling_policy_json
		FROM `+m.table("group_authorization_settings")+`
		WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? LIMIT 1`),
		authorization.id, systemAccountID, groupID).
		Scan(&localEnabled, &localGroupType, &localSchedulingJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && localEnabled.Valid && localEnabled.Int64 == 0 {
		return nil, nil
	}
	effectiveGroupType := groupTypeValue
	if err == nil && localGroupType.Valid {
		effectiveGroupType, err = normalizeGroupTypeValue(localGroupType)
		if err != nil {
			return nil, err
		}
	}
	effectiveScheduling := schedulingJSON
	if err == nil && localSchedulingJSON.Valid {
		effectiveScheduling = localSchedulingJSON
	}
	localPolicy, err := parseGroupSchedulingPolicyJSON(effectiveScheduling, effectiveGroupType)
	if err != nil {
		return nil, err
	}
	return &GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      ownerID,
		ProviderCode:                   providerCode,
		GroupAccessType:                GroupAccessTypeAuthorized,
		GroupType:                      strPtrIfSet(effectiveGroupType),
		SchedulingPolicy:               localPolicy,
		GroupAuthorizationID:           strPtrIfSet(authorization.id),
		GroupAuthorizationExpiresAt:    authorization.expiresAt,
		GroupAuthorizationQuotaLimited: boolPtr(authorization.quotaLimited),
		GroupAuthorizationSourceType:   authorization.sourceType,
		GroupAuthorizationSourceTeamID: authorization.sourceTeamID,
	}, nil
}

// authorizationRow is the resource_authorizations subset the selector reads.
type authorizationRow struct {
	id           string
	expiresAt    *string
	quotaLimited bool
	sourceType   *string
	sourceTeamID *string
}

// activeGroupAuthorization mirrors activeResourceAuthorization('group', ...) +
// resourceAuthorizationQuotaLimited.
func (m *SQLReadModels) activeGroupAuthorization(ctx context.Context, groupID, granteeID string) (*authorizationRow, error) {
	nowISO := m.now().UTC().Format("2006-01-02T15:04:05.000") + "Z"
	var id string
	var expiresAt, sourceType, sourceTeamID, limitsJSON sql.NullString
	err := m.db.QueryRowContext(ctx, m.bind(`SELECT id, effective_source_type, effective_source_team_id, expires_at, limits_json
		FROM `+m.table("resource_authorizations")+`
		WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ?
			AND status = 'active' AND (expires_at IS NULL OR expires_at > ?)
		LIMIT 1`), groupID, granteeID, nowISO).
		Scan(&id, &sourceType, &sourceTeamID, &expiresAt, &limitsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &authorizationRow{
		id:           id,
		expiresAt:    nullToStrPtr(expiresAt),
		quotaLimited: limitsHaveEnabledQuota(limitsJSON),
		sourceType:   nullToStrPtr(sourceType),
		sourceTeamID: nullToStrPtr(sourceTeamID),
	}, nil
}

// limitsHaveEnabledQuota mirrors resourceAuthorizationQuotaLimited: an
// enabled entry anywhere in the parsed request quota limits marks the
// authorization quota limited; unparsable limits are treated as absent.
func limitsHaveEnabledQuota(limitsJSON sql.NullString) bool {
	if !limitsJSON.Valid || strings.TrimSpace(limitsJSON.String) == "" {
		return false
	}
	limits, err := apikeys.ParseQuotaLimitsJSON(limitsJSON.String)
	if err != nil {
		return false
	}
	return quotaLimitsHasEnabled(limits)
}

// quotaLimitsHasEnabled mirrors apikeys.QuotaLimits.hasEnabled (private in the
// owning package).
func quotaLimitsHasEnabled(limits apikeys.QuotaLimits) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled) ||
		(limits.Daily != nil && limits.Daily.Enabled) ||
		(limits.Weekly != nil && limits.Weekly.Enabled) ||
		(limits.Monthly != nil && limits.Monthly.Enabled) ||
		(limits.Total != nil && limits.Total.Enabled)
}

// normalizeGroupTypeValue mirrors normalizeGroupType.
func normalizeGroupTypeValue(value sql.NullString) (string, error) {
	if !value.Valid || value.String == "" {
		return "personal", nil
	}
	if value.String == "personal" || value.String == "high_concurrency" {
		return value.String, nil
	}
	return "", errors.New("分组类型无效")
}

// parseGroupSchedulingPolicyJSON mirrors parseGroupSchedulingPolicyJson for
// the cache read path: only high_concurrency groups carry one, absence is the
// Node "高并发分组调度策略缺失" error, stored JSON decodes opaquely.
func parseGroupSchedulingPolicyJSON(raw sql.NullString, groupType string) (*GroupSchedulingPolicy, error) {
	if groupType != "high_concurrency" {
		return nil, nil
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, errors.New("高并发分组调度策略缺失")
	}
	var decoded GroupSchedulingPolicy
	if err := json.Unmarshal([]byte(raw.String), &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func strPtrIfSet(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullToStrPtr(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}
